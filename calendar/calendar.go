// Package calendar defines the model of a class. The data comes from the vapi
// package (Virgin's JSON mobile API); only the Class type and the club mapping
// live here.
package calendar

import (
	"maps"
	"sort"
	"strings"
	"sync"
)

// Clubs (numeric vapi clubId).
const (
	ClubCorsoComo = 209
	ClubCavour    = 224
	ClubPalermo   = 231
)

// knownClubs are the clubs VirginBot started with. Their names key stored rows
// (bookings, rules, history), so the names vapi reports never replace them.
var knownClubs = map[int]string{
	ClubCorsoComo: "Milano Corso Como",
	ClubCavour:    "Milano Cavour",
	ClubPalermo:   "Palermo",
}

// clubNames maps clubId → human-readable name: the known clubs plus every club
// vapi lists (AddClubs).
var (
	clubsMu   sync.RWMutex
	clubNames = maps.Clone(knownClubs)
)

// Club is a club members can pick.
type Club struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ClubName returns a club's name ("" if unknown).
func ClubName(id int) string {
	clubsMu.RLock()
	defer clubsMu.RUnlock()
	return clubNames[id]
}

// ClubIDByName is the reverse of ClubName (case-insensitive).
func ClubIDByName(name string) (int, bool) {
	clubsMu.RLock()
	defer clubsMu.RUnlock()
	for id, n := range clubNames {
		if strings.EqualFold(n, name) {
			return id, true
		}
	}
	return 0, false
}

// AddClubs registers the clubs vapi lists, keeping the known clubs' names.
func AddClubs(clubs []Club) {
	clubsMu.Lock()
	defer clubsMu.Unlock()
	for _, c := range clubs {
		if _, known := knownClubs[c.ID]; !known && c.ID != 0 && c.Name != "" {
			clubNames[c.ID] = c.Name
		}
	}
}

// Clubs returns every registered club, by name.
func Clubs() []Club {
	clubsMu.RLock()
	out := make([]Club, 0, len(clubNames))
	for id, n := range clubNames {
		out = append(out, Club{ID: id, Name: n})
	}
	clubsMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	ClubID     int    `json:"clubId"`     // 209 / 224 / 231
	ClassID    int    `json:"classId"`    // class id (used to book)
	SessionID  int    `json:"sessionId"`  // session id (used to book/cancel)
	BookingID  int    `json:"bookingId"`  // member's booking id (used to cancel), only if Booked
	Booked     bool   `json:"booked"`     // true if already booked by me
	// Status: "bookable" (bookable now), "booked" (booked by me), "waitlisted"
	// (I'm on its waitlist), "waitlist" (full, waitlist open), "full" (full and
	// waitlist full or closed), "unavailable" (window not open / other).
	Status string `json:"status"`

	Spots    int `json:"spots"`    // free places (capacity − booked); summed over the Solarium beds
	Capacity int `json:"capacity"` // places in the class (all beds, for the Solarium)
	// OpensAt (RFC 3339) is when the booking window opens, set only on
	// "unavailable" classes whose window is still in the future.
	OpensAt string `json:"opensAt,omitempty"`
	// CancelBy (RFC 3339) is the last moment a booking or waitlist place can be
	// cancelled, set on the user's own ones.
	CancelBy string `json:"cancelBy,omitempty"`

	Watched bool `json:"watched"` // I'm watching it: VirginBot books it if a spot frees up

	Waitlisted    bool `json:"waitlisted"`    // I'm on its waitlist
	WaitlistPos   int  `json:"waitlistPos"`   // my position (1 = next in line), if Waitlisted
	WaitlistCount int  `json:"waitlistCount"` // people waiting
	WaitlistCap   int  `json:"waitlistCap"`   // waitlist size
}
