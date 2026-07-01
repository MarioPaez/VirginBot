// Package vapi talks to Virgin Active's modern mobile API
// (vapi.virginactive.com) — the source of truth the app uses, in JSON.
// It replaces the old scraping of www.virginactive.it.
package vapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

const (
	base       = "https://vapi.virginactive.com/vapi/2.1.0"
	appID      = "2BoKHJPfAB"
	appVersion = "7.3.11 (726042304)"
	region     = "ITA"
	userAgent  = "Ktor client"
)

// ErrUnauthorized means an expired/invalid token (HTTP 401) → re-login.
var ErrUnauthorized = errors.New("vapi: unauthorized (expired token)")

// Client calls the API with the app's static API key + the user's session token.
type Client struct {
	http   *http.Client
	apiKey string
	token  string
}

// Login creates a session (POST /sessions) and stores the user's token.
func Login(apiKey, email, pass string) (*Client, error) {
	c := &Client{http: &http.Client{Timeout: 30 * time.Second}, apiKey: apiKey}
	var out struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refreshToken"`
	}
	body := map[string]string{"memberId": email, "password": pass}
	if err := c.do(http.MethodPost, "/sessions", nil, body, &out); err != nil {
		return nil, err
	}
	if out.Token == "" {
		return nil, fmt.Errorf("vapi login: response without token")
	}
	c.token = out.Token
	return c, nil
}

// ---- domain calls ----

type apiInstructor struct {
	FirstName string `json:"firstName"` // holds the instructor's full name
}

type apiClass struct {
	ID              int             `json:"id"`
	Name            string          `json:"name"`
	SessionID       int             `json:"sessionId"`
	ClassTime       string          `json:"classTime"` // "HH:MM:SS"
	SessionDuration int             `json:"sessionDuration"`
	BookableStatus  string          `json:"bookableStatus"`
	StudioName      string          `json:"studioName"`
	Instructors     []apiInstructor `json:"instructors"`
}

func instructorsOf(ins []apiInstructor) string {
	names := make([]string, 0, len(ins))
	for _, i := range ins {
		if i.FirstName != "" {
			names = append(names, i.FirstName)
		}
	}
	return strings.Join(names, ", ")
}

type apiDay struct {
	Date    string     `json:"date"`
	Classes []apiClass `json:"classes"`
}

// Classes returns a club's classes in the [start, end] range (vapi accepts a
// range, so the whole calendar is just a few calls).
func (c *Client) Classes(clubID int, start, end time.Time) ([]calendar.Class, error) {
	q := url.Values{
		"startDate": {start.Format("2006-01-02")},
		"endDate":   {end.Format("2006-01-02")},
	}
	var days []apiDay
	if err := c.do(http.MethodGet, fmt.Sprintf("/clubs/%d/classes", clubID), q, nil, &days); err != nil {
		return nil, err
	}
	var out []calendar.Class
	for _, ad := range days {
		for _, k := range ad.Classes {
			start := hhmm(k.ClassTime)
			out = append(out, calendar.Class{
				Date: ad.Date, Start: start, End: addMinutes(start, k.SessionDuration),
				Name: k.Name, Club: calendar.ClubNames[clubID], Studio: k.StudioName,
				Instructor: instructorsOf(k.Instructors),
				ClubID:     clubID, ClassID: k.ID, SessionID: k.SessionID,
				Status: mapStatus(k.BookableStatus),
			})
		}
	}
	return out, nil
}

type apiBooking struct {
	ID          int    `json:"id"` // member's booking id
	BookingDate string `json:"bookingDate"`
	Class       struct {
		ID              int             `json:"id"`
		ClubID          int             `json:"clubId"`
		Name            string          `json:"name"`
		SessionID       int             `json:"sessionId"`
		ClassTime       string          `json:"classTime"`
		SessionDuration int             `json:"sessionDuration"`
		Instructors     []apiInstructor `json:"instructors"`
	} `json:"class"`
}

// MyBookings returns the member's active bookings in the given range, as Class
// with Booked=true and the IDs needed to cancel.
func (c *Client) MyBookings(start, end time.Time) ([]calendar.Class, error) {
	q := url.Values{
		"startDate": {start.Format("2006-01-02")},
		"endDate":   {end.Format("2006-01-02")},
	}
	var resp struct {
		ActiveBookings []apiBooking `json:"activeBookings"`
	}
	if err := c.do(http.MethodGet, "/member/bookings", q, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]calendar.Class, 0, len(resp.ActiveBookings))
	for _, b := range resp.ActiveBookings {
		start := hhmm(b.Class.ClassTime)
		out = append(out, calendar.Class{
			Date: b.BookingDate, Start: start, End: addMinutes(start, b.Class.SessionDuration),
			Name: b.Class.Name, Club: calendar.ClubNames[b.Class.ClubID],
			Instructor: instructorsOf(b.Class.Instructors),
			ClubID:     b.Class.ClubID, ClassID: b.Class.ID, SessionID: b.Class.SessionID,
			BookingID: b.ID, Booked: true, Status: "booked",
		})
	}
	return out, nil
}

// Book books a class (POST with an empty body).
func (c *Client) Book(clubID, classID, sessionID int, date string) error {
	q := url.Values{"date": {date}}
	path := fmt.Sprintf("/clubs/%d/classes/%d/sessions/%d", clubID, classID, sessionID)
	return c.do(http.MethodPost, path, q, nil, nil)
}

// Cancel cancels a booking.
func (c *Client) Cancel(sessionID, bookingID, clubID, classID int, date string) error {
	q := url.Values{
		"date":               {date},
		"bookingId":          {strconv.Itoa(bookingID)},
		"clubId":             {strconv.Itoa(clubID)},
		"scheduledSessionId": {strconv.Itoa(classID)},
	}
	return c.do(http.MethodDelete, fmt.Sprintf("/member/bookings/sessions/%d", sessionID), q, nil, nil)
}

// ---- transport ----

func (c *Client) do(method, path string, query url.Values, body, out any) error {
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("x-va-region", region)
	req.Header.Set("x-app-id", appID)
	req.Header.Set("x-app-version", appVersion)
	req.Header.Set("user-agent", userAgent)
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	if c.token != "" {
		req.Header.Set("x-auth-token", c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vapi %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vapi %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(data, 200))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("vapi %s %s: invalid JSON: %w", method, path, err)
		}
	}
	return nil
}

// ---- helpers ----

func mapStatus(bookable string) string {
	switch bookable {
	case "Available":
		return "bookable"
	case "Waitlist":
		return "waitlist"
	case "Booked":
		return "booked"
	default:
		return "unavailable"
	}
}

// hhmm trims "HH:MM:SS" → "HH:MM".
func hhmm(t string) string {
	if len(t) >= 5 {
		return t[:5]
	}
	return t
}

// addMinutes adds minutes to an "HH:MM" time and returns "HH:MM".
func addMinutes(start string, mins int) string {
	t, err := time.Parse("15:04", start)
	if err != nil {
		return ""
	}
	return t.Add(time.Duration(mins) * time.Minute).Format("15:04")
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
