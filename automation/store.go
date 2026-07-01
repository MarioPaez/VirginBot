package automation

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"
)

// Rule defines a recurring class to book automatically for a user, identified by
// name + club + weekday + start time (the bookingId changes on every occurrence,
// so it can't identify the rule).
type Rule struct {
	UserID          int64     `json:"-"`
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Club            string    `json:"club"`
	Weekday         int       `json:"weekday"` // time.Weekday: 0=Sunday .. 6=Saturday
	Start           string    `json:"start"`   // "HH:MM"
	OpensDaysBefore int       `json:"opensDaysBefore"`
	Enabled         bool      `json:"enabled"`
	Created         time.Time `json:"created"`
}

// WindowDays reports how many days in advance the booking window opens by class
// type: calisthenics 7 days before; solarium, 2.
func WindowDays(className string) int {
	n := strings.ToLower(className)
	switch {
	case strings.Contains(n, "calisthenics"):
		return 7
	case strings.Contains(n, "solarium"):
		return 2
	default:
		return 2
	}
}

func ruleKey(name, club string, weekday int, start string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%s", name, club, weekday, start)))
	return hex.EncodeToString(sum[:])[:12]
}

// Store persists the automation rules (per user) in SQLite.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const ruleCols = `user_id, id, name, club, weekday, start, opens_days_before, enabled, created`

func scanRule(sc interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var enabled int
	var created string
	if err := sc.Scan(&r.UserID, &r.ID, &r.Name, &r.Club, &r.Weekday, &r.Start,
		&r.OpensDaysBefore, &enabled, &created); err != nil {
		return Rule{}, err
	}
	r.Enabled = enabled != 0
	r.Created, _ = time.Parse(time.RFC3339, created)
	return r, nil
}

func (s *Store) insert(r Rule) error {
	_, err := s.db.Exec(
		`INSERT INTO automations (user_id, id, name, club, weekday, start, opens_days_before, enabled, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, id) DO UPDATE SET enabled = 1, opens_days_before = excluded.opens_days_before`,
		r.UserID, r.ID, r.Name, r.Club, r.Weekday, r.Start, r.OpensDaysBefore, boolToInt(r.Enabled),
		r.Created.Format(time.RFC3339))
	return err
}

// List returns a user's rules.
func (s *Store) List(userID int64) []Rule {
	rows, err := s.db.Query(
		`SELECT `+ruleCols+` FROM automations WHERE user_id = ? ORDER BY created`, userID)
	if err != nil {
		log.Printf("list automations: %v", err)
		return []Rule{}
	}
	defer rows.Close()
	out := []Rule{} // non-nil: the JSON must be [] (not null) for the frontend
	for rows.Next() {
		if r, err := scanRule(rows); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// ListAll returns every rule of every user (for the engine).
func (s *Store) ListAll() []Rule {
	rows, err := s.db.Query(`SELECT ` + ruleCols + ` FROM automations ORDER BY user_id, created`)
	if err != nil {
		log.Printf("list all automations: %v", err)
		return nil
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		if r, err := scanRule(rows); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// Add inserts (or re-enables) a user's rule. Idempotent by logical identity.
func (s *Store) Add(userID int64, name, club string, weekday int, start string) (Rule, error) {
	r := Rule{
		UserID: userID, ID: ruleKey(name, club, weekday, start), Name: name, Club: club,
		Weekday: weekday, Start: start, OpensDaysBefore: WindowDays(name),
		Enabled: true, Created: time.Now(),
	}
	if err := s.insert(r); err != nil {
		return Rule{}, err
	}
	if got, ok := s.Get(userID, r.ID); ok {
		return got, nil
	}
	return r, nil
}

// Get returns a user's rule by ID.
func (s *Store) Get(userID int64, id string) (Rule, bool) {
	row := s.db.QueryRow(
		`SELECT `+ruleCols+` FROM automations WHERE user_id = ? AND id = ?`, userID, id)
	r, err := scanRule(row)
	if err != nil {
		return Rule{}, false
	}
	return r, true
}

// Remove deletes a user's rule by ID.
func (s *Store) Remove(userID int64, id string) error {
	_, err := s.db.Exec(`DELETE FROM automations WHERE user_id = ? AND id = ?`, userID, id)
	return err
}

// SetEnabled enables or pauses a rule without losing its configuration. A paused
// rule (enabled=0) stays listed but the engine skips it (never fires).
func (s *Store) SetEnabled(userID int64, id string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE automations SET enabled = ? WHERE user_id = ? AND id = ?`,
		boolToInt(enabled), userID, id)
	return err
}

// RemoveMatching deletes the rule for the given class (when unbooking).
func (s *Store) RemoveMatching(userID int64, name, club string, weekday int, start string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM automations WHERE user_id = ? AND id = ?`,
		userID, ruleKey(name, club, weekday, start))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
