package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// Store keeps users' Virgin Active accounts in SQLite, with the password
// ENCRYPTED (AES-256-GCM). We must be able to recover it in clear text to
// re-login unattended (the Virgin session expires after ~2h).
//
// A user's identity is their (unique) Virgin email. Encryption is stateless:
// every read decrypts from the DB, so we don't keep secrets in memory longer
// than necessary.
type Store struct {
	db  *sql.DB
	gcm cipher.AEAD
}

// NewStore opens the store on the DB. `secret` derives the encryption key; it
// must be stable across restarts (APP_SECRET environment variable).
func NewStore(db *sql.DB, secret string) (*Store, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, gcm: gcm}, nil
}

// Upsert creates or updates a user by email (encrypting the password) and
// returns their id.
func (s *Store) Upsert(email, pass string) (int64, error) {
	enc, err := s.encrypt(pass)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(
		`INSERT INTO users (email, pass, created) VALUES (?, ?, ?)
		 ON CONFLICT(email) DO UPDATE SET pass = excluded.pass`,
		email, enc, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("save user: %w", err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// Get returns a user's credentials (email + clear-text password).
func (s *Store) Get(userID int64) (email, pass string, ok bool) {
	var enc []byte
	if err := s.db.QueryRow(`SELECT email, pass FROM users WHERE id = ?`, userID).Scan(&email, &enc); err != nil {
		return "", "", false
	}
	pt, err := s.decrypt(enc)
	if err != nil {
		return "", "", false
	}
	return email, pt, true
}

// Email returns a user's email (or "").
func (s *Store) Email(userID int64) string {
	var email string
	if err := s.db.QueryRow(`SELECT email FROM users WHERE id = ?`, userID).Scan(&email); err != nil {
		return ""
	}
	return email
}

// Lang returns the user's preferred language (or "" if not set yet).
func (s *Store) Lang(userID int64) string {
	var lang string
	if err := s.db.QueryRow(`SELECT lang FROM users WHERE id = ?`, userID).Scan(&lang); err != nil {
		return ""
	}
	return lang
}

// SetLang sets the user's preferred language (it/es/en code).
func (s *Store) SetLang(userID int64, lang string) error {
	_, err := s.db.Exec(`UPDATE users SET lang = ? WHERE id = ?`, lang, userID)
	return err
}

// HomeClub returns the vapi clubId of the user's home club (0 = unknown).
func (s *Store) HomeClub(userID int64) int {
	var id int
	s.db.QueryRow(`SELECT home_club FROM users WHERE id = ?`, userID).Scan(&id)
	return id
}

// SetHomeClub stores the user's home club (vapi clubId).
func (s *Store) SetHomeClub(userID int64, clubID int) error {
	_, err := s.db.Exec(`UPDATE users SET home_club = ? WHERE id = ?`, clubID, userID)
	return err
}

// ReminderHours returns how many hours before each booking the user wants a
// reminder (0 = off).
func (s *Store) ReminderHours(userID int64) int {
	var h int
	s.db.QueryRow(`SELECT reminder_hours FROM users WHERE id = ?`, userID).Scan(&h)
	return h
}

// FullAction returns what to do when an automated class is still full at its
// last attempt ("" = just notify, "waitlist", "watch").
func (s *Store) FullAction(userID int64) string {
	var a string
	s.db.QueryRow(`SELECT full_action FROM users WHERE id = ?`, userID).Scan(&a)
	return a
}

// SetPrefs stores the user's reminder lead time and full-class fallback.
func (s *Store) SetPrefs(userID int64, reminderHours int, fullAction string) error {
	_, err := s.db.Exec(`UPDATE users SET reminder_hours = ?, full_action = ? WHERE id = ?`, reminderHours, fullAction, userID)
	return err
}

// CalToken returns the secret of the user's private calendar feed, creating it
// the first time.
func (s *Store) CalToken(userID int64) (string, error) {
	var tok string
	if err := s.db.QueryRow(`SELECT cal_token FROM users WHERE id = ?`, userID).Scan(&tok); err != nil {
		return "", err
	}
	if tok != "" {
		return tok, nil
	}
	return s.RotateCalToken(userID)
}

// RotateCalToken replaces the feed's secret: the old link stops working.
func (s *Store) RotateCalToken(userID int64) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	_, err := s.db.Exec(`UPDATE users SET cal_token = ? WHERE id = ?`, tok, userID)
	return tok, err
}

// UserByCalToken resolves a calendar feed secret to its user.
func (s *Store) UserByCalToken(tok string) (int64, bool) {
	if tok == "" {
		return 0, false
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM users WHERE cal_token = ?`, tok).Scan(&id)
	return id, err == nil
}

// ListUserIDs returns the ids of all registered users.
func (s *Store) ListUserIDs() []int64 {
	rows, err := s.db.Query(`SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// ---- encryption ----

func (s *Store) encrypt(pass string) ([]byte, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.gcm.Seal(nonce, nonce, []byte(pass), nil), nil
}

func (s *Store) decrypt(enc []byte) (string, error) {
	ns := s.gcm.NonceSize()
	if len(enc) < ns {
		return "", fmt.Errorf("invalid ciphertext")
	}
	pt, err := s.gcm.Open(nil, enc[:ns], enc[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
