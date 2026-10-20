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
	// Once (YYYY-MM-DD) limits the rule to that single occurrence; "" = every
	// week. A one-off rule is dropped once its date has passed.
	Once string `json:"once,omitempty"`
}

// Covers reports whether the rule applies to the occurrence on date.
func (r Rule) Covers(date string) bool { return r.Once == "" || r.Once == date }

// WindowDays reports how many days in advance the booking window opens by class
// type: every class 8 days before, except the solarium (2).
func WindowDays(className string) int {
	if strings.Contains(strings.ToLower(className), "solarium") {
		return 2
	}
	return 8
}

func ruleKey(name, club string, weekday int, start string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%s", name, club, weekday, start)))
	return hex.EncodeToString(sum[:])[:12]
}

// Store persists the automation rules (per user) in SQLite.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	// Existing rules froze opens_days_before at creation with the old windows
	// (calisthenics 7, everything else 2). Reconcile them with the current ones;
	// idempotent, and there's no UI for per-rule windows that this could clobber.
	if _, err := db.Exec(
		`UPDATE automations SET opens_days_before = 8
		 WHERE opens_days_before IN (2, 7) AND lower(name) NOT LIKE '%solarium%'`); err != nil {
		log.Printf("reconcile automation windows: %v", err)
	}
	return &Store{db: db}
}

const ruleCols = `user_id, id, name, club, weekday, start, opens_days_before, enabled, created, once_date`

func scanRule(sc interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var enabled int
	var created string
	if err := sc.Scan(&r.UserID, &r.ID, &r.Name, &r.Club, &r.Weekday, &r.Start,
		&r.OpensDaysBefore, &enabled, &created, &r.Once); err != nil {
		return Rule{}, err
	}
	r.Enabled = enabled != 0
	r.Created, _ = time.Parse(time.RFC3339, created)
	return r, nil
}

func (s *Store) insert(r Rule) error {
	_, err := s.db.Exec(
		`INSERT INTO automations (user_id, id, name, club, weekday, start, opens_days_before, enabled, created, once_date)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, id) DO UPDATE SET enabled = 1, opens_days_before = excluded.opens_days_before,
		   once_date = excluded.once_date`,
		r.UserID, r.ID, r.Name, r.Club, r.Weekday, r.Start, r.OpensDaysBefore, boolToInt(r.Enabled),
		r.Created.Format(time.RFC3339), r.Once)
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

// Add inserts (or re-enables) a user's rule. Idempotent by logical identity: a
// weekly and a one-off rule for the same class are the same rule, the last Add
// decides which (once = "" makes it weekly, a date makes it one-off).
func (s *Store) Add(userID int64, name, club string, weekday int, start, once string) (Rule, error) {
	r := Rule{
		UserID: userID, ID: ruleKey(name, club, weekday, start), Name: name, Club: club,
		Weekday: weekday, Start: start, OpensDaysBefore: WindowDays(name),
		Enabled: true, Created: time.Now(), Once: once,
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

// Remove deletes a user's rule by ID (and its skipped dates).
func (s *Store) Remove(userID int64, id string) error {
	if _, err := s.db.Exec(`DELETE FROM automations WHERE user_id = ? AND id = ?`, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM skips WHERE user_id = ? AND rule_id = ?`, userID, id)
	return err
}

// SetEnabled enables or pauses a rule without losing its configuration. A paused
// rule (enabled=0) stays listed but the engine skips it (never fires).
func (s *Store) SetEnabled(userID int64, id string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE automations SET enabled = ? WHERE user_id = ? AND id = ?`,
		boolToInt(enabled), userID, id)
	return err
}

// Find returns the user's rule for a class (any week), if there is one.
func (s *Store) Find(userID int64, name, club string, weekday int, start string) (Rule, bool) {
	return s.Get(userID, ruleKey(name, club, weekday, start))
}

// PurgeOnce deletes the one-off rules whose date is before `before` (YYYY-MM-DD):
// their only occurrence is over.
func (s *Store) PurgeOnce(before string) error {
	_, err := s.db.Exec(`DELETE FROM automations WHERE once_date <> '' AND once_date < ?`, before)
	return err
}

// Skip marks one occurrence (date) of the matching rule as not to be booked: the
// user cancelled it, so the engine must not re-book it, but the rule keeps
// booking the other weeks. No-op if there's no matching rule; reports whether
// the date got marked.
func (s *Store) Skip(userID int64, name, club string, weekday int, start, date string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO skips (user_id, rule_id, date)
		 SELECT user_id, id, ? FROM automations WHERE user_id = ? AND id = ?`,
		date, userID, ruleKey(name, club, weekday, start))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Unskip undoes Skip (the user booked or automated that occurrence again).
func (s *Store) Unskip(userID int64, name, club string, weekday int, start, date string) error {
	_, err := s.db.Exec(`DELETE FROM skips WHERE user_id = ? AND rule_id = ? AND date = ?`,
		userID, ruleKey(name, club, weekday, start), date)
	return err
}

// Skipped reports whether the rule's occurrence on date was cancelled by the user.
func (s *Store) Skipped(userID int64, ruleID, date string) bool {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM skips WHERE user_id = ? AND rule_id = ? AND date = ?`,
		userID, ruleID, date).Scan(&one)
	return err == nil
}

// PurgeSkips deletes the skipped dates before `before` (YYYY-MM-DD).
func (s *Store) PurgeSkips(before string) error {
	_, err := s.db.Exec(`DELETE FROM skips WHERE date < ?`, before)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
