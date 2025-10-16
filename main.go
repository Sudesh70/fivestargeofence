package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/go-mail/mail/v2"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Device struct {
	ID       int    `json:"id"`
	UniqueID string `json:"uniqueId"`
	Name     string `json:"name"`
}

type EventReport struct {
	Type      string `json:"type"`
	EventTime string `json:"eventTime"`
	DeviceID  int    `json:"deviceId"`
}

func main() {
	loginURL := "https://vts.suntrack.com.au/api/session"
	deviceURL := "https://vts.suntrack.com.au/api/devices"
	reportURL := "https://vts.suntrack.com.au/api/reports/events"

	username := "ausparcels@gmail.com"
	password := "Five*2025"

	sessionCookie, err := authenticate(loginURL, username, password)
	if err != nil {
		log.Fatalf("Authentication failed: %v", err)
	}

	var wg sync.WaitGroup
	deviceChan := make(chan map[int]Device)
	reportChan := make(chan []EventReport)

	wg.Add(1)
	go func() {
		defer wg.Done()
		devices, err := fetchDevices(deviceURL, sessionCookie)
		if err != nil {
			log.Printf("Error fetching devices: %v", err)
			deviceChan <- nil
			return
		}
		deviceChan <- devices
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		reports, err := fetchEventReports(reportURL, sessionCookie)
		if err != nil {
			log.Printf("Error fetching reports: %v", err)
			reportChan <- nil
			return
		}
		reportChan <- reports
	}()

	go func() {
		wg.Wait()
		close(deviceChan)
		close(reportChan)
	}()

	deviceMap := <-deviceChan
	reports := <-reportChan

	if deviceMap == nil || reports == nil {
		log.Fatal("Failed to fetch necessary data.")
	}

	csvFile := "geofence_report.csv"
	if err := generateCSV(csvFile, reports, deviceMap); err != nil {
		log.Fatalf("Error generating CSV: %v", err)
	}

	emailBody := generateEmailBody(reports, deviceMap)
	sendEmail(csvFile, emailBody)
}

func authenticate(urlStr, username, password string) (*http.Cookie, error) {
	formData := url.Values{}
	formData.Set("email", username)
	formData.Set("password", password)

	req, err := http.NewRequest("POST", urlStr, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("login failed: %s", body)
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "JSESSIONID" {
			return cookie, nil
		}
	}
	return nil, fmt.Errorf("session cookie not found")
}

func fetchDevices(apiURL string, sessionCookie *http.Cookie) (map[int]Device, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.AddCookie(sessionCookie)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	var devices []Device
	if err := json.NewDecoder(resp.Body).Decode(&devices); err != nil {
		return nil, err
	}

	deviceMap := make(map[int]Device)
	for _, device := range devices {
		deviceMap[device.ID] = device
	}
	return deviceMap, nil
}

func fetchEventReports(apiURL string, sessionCookie *http.Cookie) ([]EventReport, error) {
	location, _ := time.LoadLocation("Australia/Melbourne")
	today := time.Now()
	from := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location).Format(time.RFC3339)
	to := time.Date(today.Year(), today.Month(), today.Day(), 23, 59, 59, 0, location).Format(time.RFC3339)

	params := url.Values{}
	params.Set("from", from)
	params.Set("to", to)
	params.Add("type", "geofenceEnter")
	params.Add("type", "geofenceExit")

	groupIDs := []string{"45"}
	for _, gid := range groupIDs {
		params.Add("groupId", gid)
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s?%s", apiURL, params.Encode()), nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(sessionCookie)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	var reports []EventReport
	if err := json.NewDecoder(resp.Body).Decode(&reports); err != nil {
		return nil, err
	}

	return reports, nil
}

func convertToMelbourneTime(utcTime string) string {
	t, err := time.Parse(time.RFC3339, utcTime)
	if err != nil {
		return "Invalid Time"
	}
	location, _ := time.LoadLocation("Australia/Melbourne")
	return t.In(location).Format("2006-01-02 15:04:05")
}

func generateCSV(filename string, reports []EventReport, deviceMap map[int]Device) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {

		}
	}(file)

	writer := csv.NewWriter(file)
	defer writer.Flush()

	err = writer.Write([]string{"Device Name", "Event Time (UTC)", "Event Time (Melbourne)", "Geofence Status"})
	if err != nil {
		return err
	}

	for _, report := range reports {
		device := deviceMap[report.DeviceID]
		localTime := convertToMelbourneTime(report.EventTime)
		err := writer.Write([]string{
			device.Name,
			report.EventTime,
			localTime,
			report.Type,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func generateEmailBody(reports []EventReport, deviceMap map[int]Device) string {
	var buffer bytes.Buffer
	currentDate := time.Now().Format("Monday, January 2, 2006")

	buffer.WriteString(`
	<html>
	<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	</head>
	<body style="font-family: Arial, sans-serif; background-color: #f4f4f4; padding: 20px;">
		<div style="max-width: 800px; margin: auto; background: #fff; padding: 20px; border-radius: 6px; box-shadow: 0 2px 5px rgba(0,0,0,0.1);">
			<h2 style="text-align: center; color: #cc0000;">Geofence Entry/Exit Report</h2>
			<p style="text-align: center; color: #555;">` + currentDate + `</p>
			<table style="width: 100%; border-collapse: collapse;" border="1">
				<tr style="background-color: #f2f2f2; text-align: center;">
					<th style="padding: 8px;">Device Name</th>
					<th style="padding: 8px;">Event Time (UTC)</th>
					<th style="padding: 8px;">Event Time (Melbourne)</th>
					<th style="padding: 8px;">Geofence Status</th>
				</tr>`)

	for _, report := range reports {
		device := deviceMap[report.DeviceID]
		localTime := convertToMelbourneTime(report.EventTime)

		rowStyle := ""
		if report.Type == "geofenceExit" {
			rowStyle = `style="background-color: #e6ffe6;"` // light green
		}

		buffer.WriteString(fmt.Sprintf(`
			<tr %s>
				<td style="padding: 8px; text-align: center;">%s</td>
				<td style="padding: 8px; text-align: center;">%s</td>
				<td style="padding: 8px; text-align: center;">%s</td>
				<td style="padding: 8px; text-align: center;">%s</td>
			</tr>`, rowStyle, device.Name, report.EventTime, localTime, report.Type))
	}

	buffer.WriteString(`
			</table>
			<p style="text-align: center; font-size: 12px; color: #888; margin-top: 20px;">© 2025 SunRu Fleet Management</p>
		</div>
	</body>
	</html>`)

	return buffer.String()
}

func sendEmail(csvFile, emailBody string) {
	m := mail.NewMessage()
	m.SetAddressHeader("From", "info@suntrack.com.au", "SunTrack-GPS Geofence Report")
	m.SetHeader("To", "asankagmr@gmail.com")
	m.SetHeader("Cc", "malien.n@sunru.com.au")
	m.SetHeader("Subject", "Daily Geofence Entry/Exit Report")
	m.SetBody("text/html", emailBody)
	m.Attach(csvFile)

	d := mail.NewDialer("smtp.titan.email", 465, "info@suntrack.com.au", "Dehan@2009228")
	if err := d.DialAndSend(m); err != nil {
		log.Fatalf("Failed to send email: %v", err)
	}

	fmt.Println("✅ Email sent successfully!")
}
