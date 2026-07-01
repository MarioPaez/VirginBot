package notification

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"
)

// Sender sends an email notification. The recipient is passed on every call so
// each message can be routed to the right user (the app is multi-user and shares
// a single SMTP transport).
type Sender interface {
	Send(to, subject, body string) error
}

// NoOp sends nothing (used when no SMTP is configured).
type NoOp struct{}

func (NoOp) Send(string, string, string) error { return nil }

// SMTP sends emails through a shared SMTP server (e.g. Brevo).
type SMTP struct {
	host, port, user, pass, from string
	override                     string // SMTP_TO: forces the recipient (for testing)
}

// FromEnv builds an SMTP Sender from environment variables, or NoOp if the
// required ones (SMTP_HOST, SMTP_USER, SMTP_PASS) are missing.
//
// SMTP_FROM must be a verified sender at the provider. SMTP_TO, if set, forces
// the recipient of ALL emails (handy for testing).
func FromEnv() Sender {
	host := os.Getenv("SMTP_HOST")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	if host == "" || user == "" || pass == "" {
		log.Println("email notifications disabled (missing SMTP_* variables)")
		return NoOp{}
	}
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	from := os.Getenv("SMTP_FROM")
	if from == "" {
		from = user
	}
	override := os.Getenv("SMTP_TO")
	dest := "per-user recipient"
	if override != "" {
		dest = override + " (SMTP_TO override)"
	}
	log.Printf("email notifications enabled → %s", dest)
	return &SMTP{host: host, port: port, user: user, pass: pass, from: from, override: override}
}

func (s *SMTP) Send(to, subject, body string) error {
	if s.override != "" {
		to = s.override
	}
	if to == "" {
		return nil // no known recipient
	}
	msg := strings.Join([]string{
		"From: " + s.from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	auth := smtp.PlainAuth("", s.user, s.pass, s.host)
	if err := smtp.SendMail(s.host+":"+s.port, auth, s.from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	return nil
}
