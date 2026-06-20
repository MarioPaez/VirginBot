package notification

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"
)

// Notifier envía un aviso (p. ej. el resultado de una reserva automática).
type Notifier interface {
	Notify(subject, body string) error
}

// NoOp no envía nada (cuando no hay SMTP configurado).
type NoOp struct{}

func (NoOp) Notify(string, string) error { return nil }

// SMTP envía emails por SMTP (p. ej. Gmail con app password).
type SMTP struct {
	host, port, user, pass, from, to string
}

// FromEnv construye un notificador SMTP a partir de variables de entorno, o
// NoOp si no están configuradas. Variables: SMTP_HOST, SMTP_PORT (def. 587),
// SMTP_USER, SMTP_PASS, SMTP_FROM (def. SMTP_USER), SMTP_TO.
func FromEnv() Notifier {
	host := os.Getenv("SMTP_HOST")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	to := os.Getenv("SMTP_TO")
	if host == "" || user == "" || pass == "" || to == "" {
		log.Println("notificaciones email desactivadas (faltan variables SMTP_*)")
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
	log.Printf("notificaciones email activas → %s", to)
	return &SMTP{host: host, port: port, user: user, pass: pass, from: from, to: to}
}

func (s *SMTP) Notify(subject, body string) error {
	msg := strings.Join([]string{
		"From: " + s.from,
		"To: " + s.to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	auth := smtp.PlainAuth("", s.user, s.pass, s.host)
	addr := s.host + ":" + s.port
	if err := smtp.SendMail(addr, auth, s.from, []string{s.to}, []byte(msg)); err != nil {
		return fmt.Errorf("enviar email: %w", err)
	}
	return nil
}
