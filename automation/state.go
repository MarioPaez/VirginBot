package automation

import (
	"log"
	"time"
)

// Engine state and attempt log, persisted so a restart or a deploy doesn't
// forget what was already booked, tried or notified.

// loadStates reads the persisted per-occurrence state of today and later.
func (s *Store) loadStates(from string) map[string]*occState {
	out := map[string]*occState{}
	rows, err := s.db.Query(`SELECT key, booked, waitlisted, attempts, last_error, miss_notified, last_fired
		FROM engine_state WHERE date >= ?`, from)
	if err != nil {
		log.Printf("load engine state: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, lastErr, lastFired string
		var booked, waitlisted, missNotified int
		st := &occState{}
		if err := rows.Scan(&key, &booked, &waitlisted, &st.attempts, &lastErr, &missNotified, &lastFired); err != nil {
			continue
		}
		st.booked, st.waitlisted, st.missNotified, st.lastError = booked != 0, waitlisted != 0, missNotified != 0, lastErr
		st.lastFired, _ = time.Parse(time.RFC3339, lastFired)
		out[key] = st
	}
	return out
}

func (s *Store) saveState(key, date string, st occState) {
	lastFired := ""
	if !st.lastFired.IsZero() {
		lastFired = st.lastFired.Format(time.RFC3339)
	}
	if _, err := s.db.Exec(`INSERT INTO engine_state (key, date, booked, waitlisted, attempts, last_error, miss_notified, last_fired)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET booked = excluded.booked, waitlisted = excluded.waitlisted,
		  attempts = excluded.attempts, last_error = excluded.last_error,
		  miss_notified = excluded.miss_notified, last_fired = excluded.last_fired`,
		key, date, boolToInt(st.booked), boolToInt(st.waitlisted), st.attempts, st.lastError,
		boolToInt(st.missNotified), lastFired); err != nil {
		log.Printf("save engine state %s: %v", key, err)
	}
}

func (s *Store) deleteState(key string) {
	if _, err := s.db.Exec(`DELETE FROM engine_state WHERE key = ?`, key); err != nil {
		log.Printf("delete engine state %s: %v", key, err)
	}
}

// Attempt is one booking attempt (an engine fire or a watch grab), for the admin
// view and the health checks.
type Attempt struct {
	Time    time.Time `json:"time"`
	UserID  int64     `json:"userId"`
	Name    string    `json:"name"`
	Club    string    `json:"club"`
	Date    string    `json:"date"`
	Start   string    `json:"start"`
	Source  string    `json:"source"`  // "engine" | "watch"
	Outcome string    `json:"outcome"` // see the Outcome* constants
	Detail  string    `json:"detail"`
}

// Attempt outcomes.
const (
	OutcomeBooked      = "booked"
	OutcomeWaitlisted  = "waitlisted"
	OutcomeUnavailable = "unavailable" // not bookable (window not open, full) or not in the calendar
	OutcomeError       = "error"       // the booking or the calendar call failed
	OutcomeMissed      = "missed"      // a watch ended without a spot
	OutcomeOverlap     = "overlap"     // not booked: the user has another class at that time
)

// RecordAttempt appends an attempt to the log.
func (s *Store) RecordAttempt(a Attempt) {
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	if len(a.Detail) > 300 {
		a.Detail = a.Detail[:300] + "…"
	}
	if _, err := s.db.Exec(`INSERT INTO engine_attempts (ts, user_id, name, club, date, start, source, outcome, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Time.UTC().Format(time.RFC3339), a.UserID, a.Name, a.Club, a.Date, a.Start, a.Source, a.Outcome, a.Detail); err != nil {
		log.Printf("record attempt: %v", err)
	}
}

// RecentAttempts returns the latest attempts, newest first.
func (s *Store) RecentAttempts(limit int) []Attempt {
	rows, err := s.db.Query(`SELECT ts, user_id, name, club, date, start, source, outcome, detail
		FROM engine_attempts ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		log.Printf("recent attempts: %v", err)
		return []Attempt{}
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		var ts string
		if err := rows.Scan(&ts, &a.UserID, &a.Name, &a.Club, &a.Date, &a.Start, &a.Source, &a.Outcome, &a.Detail); err != nil {
			continue
		}
		a.Time, _ = time.Parse(time.RFC3339, ts)
		out = append(out, a)
	}
	return out
}

// OutcomesSince counts the attempts per outcome since t, of one source
// ("engine", "watch") or all ("").
func (s *Store) OutcomesSince(t time.Time, source string) map[string]int {
	out := map[string]int{}
	rows, err := s.db.Query(`SELECT outcome, COUNT(*) FROM engine_attempts WHERE ts >= ? AND (? = '' OR source = ?) GROUP BY outcome`,
		t.UTC().Format(time.RFC3339), source, source)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var o string
		var n int
		if rows.Scan(&o, &n) == nil {
			out[o] = n
		}
	}
	return out
}

// PurgeEngine drops the state of past occurrences and attempts older than keep.
func (s *Store) PurgeEngine(today string, keep time.Duration) {
	if _, err := s.db.Exec(`DELETE FROM engine_state WHERE date < ?`, today); err != nil {
		log.Printf("purge engine state: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM engine_attempts WHERE ts < ?`,
		time.Now().Add(-keep).UTC().Format(time.RFC3339)); err != nil {
		log.Printf("purge engine attempts: %v", err)
	}
}
