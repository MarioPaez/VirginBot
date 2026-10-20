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

// ErrUnauthorized means an expired/invalid token (HTTP 401 on an authenticated
// call) → re-login.
var ErrUnauthorized = errors.New("vapi: unauthorized (expired token)")

// ErrBadCredentials means Virgin rejected the email/password (HTTP 401 on
// POST /sessions). Retrying won't help: only a new password will.
var ErrBadCredentials = errors.New("vapi: Virgin Active rejected the email/password")

// ErrClientRejected means Virgin's gateway rejected the APP itself, not the user:
// the static x-api-key was rotated, or the app id/version we impersonate is no
// longer accepted. Every user fails until the constants/secret are updated.
var ErrClientRejected = errors.New("vapi: Virgin Active rejected the app credentials (x-api-key / app id / app version): update them")

// StatusError is a non-2xx answer from vapi (other than the 401s above). Callers
// use Status to tell a lockout/rate limit (403/423/429) from an outage (5xx).
type StatusError struct {
	Method, Path string
	Status       int
	Body         string // truncated
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("vapi %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

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
		if errors.Is(err, ErrUnauthorized) {
			// A 401 here is the password, not a token: don't let callers mistake
			// it for an expired session and re-login in a loop.
			return nil, ErrBadCredentials
		}
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
	WaitlistCount   int             `json:"waitlistCount"`
	WaitlistCap     int             `json:"waitlistCapacity"`
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
				Status:        mapStatus(k.BookableStatus),
				WaitlistCount: k.WaitlistCount, WaitlistCap: k.WaitlistCap,
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
		WaitlistPos     int             `json:"waitlistPosition"` // 1 = next in line; 0 on active bookings
	} `json:"class"`
}

// MyBookings returns the member's bookings in the given range as Class values
// with the IDs needed to cancel: active ones (Booked) and waitlist places
// (Waitlisted, with the position).
func (c *Client) MyBookings(start, end time.Time) ([]calendar.Class, error) {
	q := url.Values{
		"startDate": {start.Format("2006-01-02")},
		"endDate":   {end.Format("2006-01-02")},
	}
	var resp struct {
		ActiveBookings   []apiBooking `json:"activeBookings"`
		WaitlistBookings []apiBooking `json:"waitinglistBookings"`
	}
	if err := c.do(http.MethodGet, "/member/bookings", q, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]calendar.Class, 0, len(resp.ActiveBookings)+len(resp.WaitlistBookings))
	add := func(b apiBooking, waitlisted bool) {
		start := hhmm(b.Class.ClassTime)
		c := calendar.Class{
			Date: b.BookingDate, Start: start, End: addMinutes(start, b.Class.SessionDuration),
			Name: b.Class.Name, Club: calendar.ClubNames[b.Class.ClubID],
			Instructor: instructorsOf(b.Class.Instructors),
			ClubID:     b.Class.ClubID, ClassID: b.Class.ID, SessionID: b.Class.SessionID,
			BookingID: b.ID, Booked: true, Status: "booked",
		}
		if waitlisted {
			c.Booked, c.Status = false, "waitlisted"
			c.Waitlisted, c.WaitlistPos = true, b.Class.WaitlistPos
		}
		out = append(out, c)
	}
	for _, b := range resp.ActiveBookings {
		add(b, false)
	}
	for _, b := range resp.WaitlistBookings {
		add(b, true)
	}
	return out, nil
}

// BookResult is what a booking POST did: on a full class with an open waitlist
// Virgin doesn't fail, it puts the member on the waitlist instead.
type BookResult struct {
	BookingID  int
	Waitlisted bool
	Position   int // waitlist position (1 = next), if Waitlisted
}

// Book books a class (POST with an empty body). On a full class with an open
// waitlist the same call joins the waitlist: the response says so
// ({"status":"OVERBOOKED_WAITINGLIST","waitingListIndex":3}).
func (c *Client) Book(clubID, classID, sessionID int, date string) (BookResult, error) {
	q := url.Values{"date": {date}}
	path := fmt.Sprintf("/clubs/%d/classes/%d/sessions/%d", clubID, classID, sessionID)
	var resp bookResponse
	if err := c.do(http.MethodPost, path, q, nil, &resp); err != nil {
		return BookResult{}, err
	}
	return resp.result(), nil
}

// bookResponse decodes the booking POST's answer leniently: the booking has
// already happened, so an unexpected body must not turn it into an error.
type bookResponse struct {
	BookingID int    `json:"bookingId"`
	Status    string `json:"status"`
	Index     int    `json:"waitingListIndex"`
}

func (r bookResponse) result() BookResult {
	return BookResult{
		BookingID:  r.BookingID,
		Waitlisted: strings.Contains(strings.ToUpper(r.Status), "WAITINGLIST"),
		Position:   r.Index,
	}
}

func (r *bookResponse) UnmarshalJSON(b []byte) error {
	type plain bookResponse
	var p plain
	if json.Unmarshal(b, &p) == nil {
		*r = bookResponse(p)
	}
	return nil
}

// Cancel cancels a booking, or leaves a waitlist (same call, the waitlist
// booking's id).
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

	if clientRejected(resp.StatusCode, data) {
		return fmt.Errorf("%w (%s %s: HTTP %d: %s)", ErrClientRejected, method, path, resp.StatusCode, truncate(data, 200))
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{Method: method, Path: path, Status: resp.StatusCode, Body: truncate(data, 200)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("vapi %s %s: invalid JSON: %w", method, path, err)
		}
	}
	return nil
}

// ---- helpers ----

// clientRejected tells a gateway rejection of the app from a user-level 401.
// Observed (Oct 2026): bad/missing x-api-key or app headers → 401
// `{ "statusCode": 401, "message": "Unauthorized" }` (Azure gateway shape);
// expired token / wrong password → 401 `{"error": "..."}` (backend shape).
// 426 is the standard answer for a minimum app version, should they add one.
func clientRejected(status int, body []byte) bool {
	if status == http.StatusUpgradeRequired {
		return true
	}
	if status != http.StatusUnauthorized {
		return false
	}
	var gw struct {
		StatusCode *int `json:"statusCode"`
	}
	return json.Unmarshal(body, &gw) == nil && gw.StatusCode != nil
}

func mapStatus(bookable string) string {
	switch bookable {
	case "Available":
		return "bookable"
	case "Waitlist":
		return "waitlist"
	case "Full":
		return "full"
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
