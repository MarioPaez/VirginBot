// Package calendar defines the model of a class. The data comes from the vapi
// package (Virgin's JSON mobile API); only the Class type and the club mapping
// live here.
package calendar

// Clubs (numeric vapi clubId).
const (
	ClubCorsoComo = 209
	ClubCavour    = 224
)

// ClubNames maps clubId → human-readable name.
var ClubNames = map[int]string{
	ClubCorsoComo: "Milano Corso Como",
	ClubCavour:    "Milano Cavour",
}

// Class is a class scheduled in the calendar.
type Class struct {
	Date       string `json:"date"`  // YYYY-MM-DD
	Start      string `json:"start"` // HH:MM
	End        string `json:"end"`   // HH:MM
	Name       string `json:"name"`  // e.g. "Calisthenics Performance"
	Club       string `json:"club"`  // human-readable club name
	Studio     string `json:"studio"`
	Instructor string `json:"instructor"` // e.g. "Alessandro Restelli"
	ClubID     int    `json:"clubId"`     // 209 / 224
	ClassID    int    `json:"classId"`    // class id (used to book)
	SessionID  int    `json:"sessionId"`  // session id (used to book/cancel)
	BookingID  int    `json:"bookingId"`  // member's booking id (used to cancel), only if Booked
	Booked     bool   `json:"booked"`     // true if already booked by me
	// Status: "bookable" (bookable now), "booked" (booked by me),
	// "waitlist" (full, waiting list), "unavailable" (window not open / other).
	Status string `json:"status"`
}
