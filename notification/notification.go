package notification

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"
)

// Sender envía un aviso por email. El destinatario se pasa en cada llamada para
// poder dirigir cada correo al usuario correspondiente (la app es multi-usuario
// y comparte un único transporte SMTP).
type Sender interface {
	Send(to, subject, body string) error
}

// NoOp no envía nada (cuando no hay SMTP configurado).
type NoOp struct{}

func (NoOp) Send(string, string, string) error { return nil }

// SMTP envía emails por un servidor SMTP compartido (p. ej. Brevo).
type SMTP struct {
	host, port, user, pass, from string
	override                     string // SMTP_TO: fuerza el destinatario (pruebas)
}

// FromEnv construye un Sender SMTP a partir de variables de entorno, o NoOp si
// faltan las obligatorias (SMTP_HOST, SMTP_USER, SMTP_PASS).
//
// SMTP_FROM debe ser un remitente verificado en el proveedor. SMTP_TO, si se
// define, fuerza el destinatario de TODOS los correos (útil en pruebas).
func FromEnv() Sender {
	host := os.Getenv("SMTP_HOST")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	if host == "" || user == "" || pass == "" {
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
	override := os.Getenv("SMTP_TO")
	dest := "destinatario por usuario"
	if override != "" {
		dest = override + " (override SMTP_TO)"
	}
	log.Printf("notificaciones email activas → %s", dest)
	return &SMTP{host: host, port: port, user: user, pass: pass, from: from, override: override}
}

func (s *SMTP) Send(to, subject, body string) error {
	if s.override != "" {
		to = s.override
	}
	if to == "" {
		return nil // sin destinatario conocido
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
		return fmt.Errorf("enviar email: %w", err)
	}
	return nil
}
