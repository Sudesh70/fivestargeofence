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

type DeviceEventSummary struct {
	DeviceID   int
	FirstEnter string
	LastExit   string
}

func main() {
	loginURL := "https://fivestaralliance.com.au/api/session"
	deviceURL := "https://fivestaralliance.com.au/api/devices"
	reportURL := "https://fivestaralliance.com.au/api/reports/events"

	username := "portal@fivestaralliance.com.au"
	password := "five5star@25"

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

	groupIDs := []string{"1"}
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

func filterFirstEnterLastExit(reports []EventReport) []DeviceEventSummary {
	deviceEvents := make(map[int]*DeviceEventSummary)

	for _, report := range reports {
		if _, exists := deviceEvents[report.DeviceID]; !exists {
			deviceEvents[report.DeviceID] = &DeviceEventSummary{
				DeviceID:   report.DeviceID,
				FirstEnter: "-",
				LastExit:   "-",
			}
		}

		if report.Type == "geofenceEnter" {
			if deviceEvents[report.DeviceID].FirstEnter == "-" {
				deviceEvents[report.DeviceID].FirstEnter = convertToMelbourneTime(report.EventTime)
			}
		} else if report.Type == "geofenceExit" {
			deviceEvents[report.DeviceID].LastExit = convertToMelbourneTime(report.EventTime)
		}
	}

	result := make([]DeviceEventSummary, 0, len(deviceEvents))
	for _, summary := range deviceEvents {
		result = append(result, *summary)
	}
	return result
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

	err = writer.Write([]string{"Device Name", "First Enter Time (Melbourne)", "Last Exit Time (Melbourne)"})
	if err != nil {
		return err
	}

	filteredReports := filterFirstEnterLastExit(reports)
	for _, report := range filteredReports {
		device := deviceMap[report.DeviceID]
		err := writer.Write([]string{
			device.Name,
			report.FirstEnter,
			report.LastExit,
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

	filtered := filterFirstEnterLastExit(reports)
	totalDevices := len(filtered)
	totalEvents := len(reports)

	buffer.WriteString(`
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Geofence Entry/Exit Report</title>
</head>
<body style="margin:0; padding:0; background-color:#f4f7fa; font-family:Arial, sans-serif; color:#333333;">
    <table width="100%" border="0" cellpadding="0" cellspacing="0" bgcolor="#f4f7fa">
    <tr>
        <td align="center">
            <table width="600" border="0" cellpadding="0" cellspacing="0" style="background-color:#ffffff; margin:20px auto; border-radius:8px; overflow:hidden; box-shadow:0 4px 12px rgba(0,0,0,0.1);">
                <!-- Header -->
                <tr>
                    <td align="center" style="background: linear-gradient(135deg,#1e73be 0%,#3ab0f3 100%); color:#ffffff; padding:40px 30px;">
                        <h1 style="margin:0; font-size:28px; color:#BF1330;">🚗 Geofence Entry/Exit Report</h1>
                        <p style="margin:12px 0 0 0; font-size:16px; color:#BF1330;">` + currentDate + `</p>
                    </td>
                </tr>

                <!-- Stats section -->
                <tr>
                    <td style="padding:30px;">
                        <table width="100%" border="0" cellpadding="0" cellspacing="0">
                            <tr>
                                <td align="center" style="vertical-align:top; width:50%; padding:0 10px 20px 10px;">
                                    <div style="background-color:#eaf4ff; border-radius:8px; padding:20px;">
                                        <h2 style="margin:0; font-size:26px; color:#1e73be;">` + fmt.Sprintf("%d", totalDevices) + `</h2>
                                        <p style="margin:4px 0 0; font-size:14px; color:#555555; text-transform:uppercase; letter-spacing:1px;">Devices Tracked</p>
                                    </div>
                                </td>
                                <td align="center" style="vertical-align:top; width:50%; padding:0 10px 20px 10px;">
                                    <div style="background-color:#fff4e6; border-radius:8px; padding:20px;">
                                        <h2 style="margin:0; font-size:26px; color:#ff7a00;">` + fmt.Sprintf("%d", totalEvents) + `</h2>
                                        <p style="margin:4px 0 0; font-size:14px; color:#555555; text-transform:uppercase; letter-spacing:1px;">Events Today</p>
                                    </div>
                                </td>
                            </tr>
                        </table>
                    </td>
                </tr>

                <!-- Table section -->
                <tr>
                    <td style="padding:0 30px 30px 30px;">
                        <table width="100%" border="0" cellpadding="0" cellspacing="0" style="border-collapse:collapse;">
                            <thead>
                                <tr>
                                    <th align="left" style="padding:12px; font-size:14px; background-color:#1e73be; color:#ffffff; text-transform:uppercase;">Device Name</th>
                                    <th align="left" style="padding:12px; font-size:14px; background-color:#1e73be; color:#ffffff; text-transform:uppercase;">Oklength Dc Enter Time</th>
                                    <th align="left" style="padding:12px; font-size:14px; background-color:#1e73be; color:#ffffff; text-transform:uppercase;">Oklength Dc Exit Time</th>
                                </tr>
                            </thead>
                            <tbody>`)

	for _, summary := range filtered {
		device := deviceMap[summary.DeviceID]
		enterClass := "enter-time"
		exitClass := "exit-time"
		if summary.FirstEnter == "-" {
			enterClass = "no-data"
		}
		if summary.LastExit == "-" {
			exitClass = "no-data"
		}

		buffer.WriteString(`
                                <tr>
                                    <td style="padding:12px; font-size:14px; color:#333333; border-bottom:1px solid #e1e5e8;">` + device.Name + `</td>
                                    <td style="padding:12px; font-size:14px; color:#333333; border-bottom:1px solid #e1e5e8;">
                                        <span style="display:inline-block; padding:6px 12px; border-radius:12px; font-size:12px; color:` +
			func() string {
				if enterClass == "no-data" {
					return "#a0a0a0"
				}
				return "#0d4b1a"
			}() +
			`; background:` +
			func() string {
				if enterClass == "no-data" {
					return "#f0f0f0"
				}
				return "linear-gradient(135deg,#d4fc79 0%,#96e6a1 100%)"
			}() +
			`;">` + summary.FirstEnter + `</span>
                                    </td>
                                    <td style="padding:12px; font-size:14px; color:#333333; border-bottom:1px solid #e1e5e8;">
                                        <span style="display:inline-block; padding:6px 12px; border-radius:12px; font-size:12px; color:` +
			func() string {
				if exitClass == "no-data" {
					return "#5a0f16"
				}
				return "#6d4c00"
			}() +
			`; background:` +
			func() string {
				if exitClass == "no-data" {
					return "#f7d8d8"
				}
				return "linear-gradient(135deg,#ffeaa7 0%,#fdcb6e 100%)"
			}() +
			`;">` + summary.LastExit + `</span>
                                    </td>
                                </tr>`)
	}

	buffer.WriteString(`
                            </tbody>
                        </table>
                    </td>
                </tr>

                <!-- Footer -->
                <tr>
                    <td align="center" style="padding:30px; background-color:#f4f7fa; color:#777777; font-size:12px;">
                        <p style="margin:0;"><strong>SunRu Fleet Management</strong></p>
                        <p style="margin:6px 0 0;">© 2025 SunTrack GPS ‑ Automated Daily Report</p>
                    </td>
                </tr>

            </table>
        </td>
    </tr>
    </table>
</body>
</html>`)

	return buffer.String()
}

func sendEmail(csvFile, emailBody string) {
	m := mail.NewMessage()
	m.SetAddressHeader("From", "info@suntrack.com.au", "SunTrack-GPS Geofence Report")
	m.SetHeader("To", "ausparcels@gmail.com")
	m.SetHeader("Cc", "malien@sunru.com.au", "asankagmr@gmail.com")
	m.SetHeader("Subject", "Daily Geofence Entry/Exit Report")
	m.SetBody("text/html", emailBody)
	m.Attach(csvFile)

	d := mail.NewDialer("smtp.titan.email", 465, "info@suntrack.com.au", "Dehan@2009228")
	if err := d.DialAndSend(m); err != nil {
		log.Fatalf("Failed to send email: %v", err)
	}

	fmt.Println("✅ Email sent successfully!")
}
