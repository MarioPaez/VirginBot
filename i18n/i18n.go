// Package i18n centralizes the user-facing translatable strings in it/es/en:
// the date names and the push notification texts. The web UI has its own catalog in the
// frontend (server/web/index.html).
package i18n

import (
	"fmt"
	"strings"
	"time"
)

// Lang is a supported language (lowercase ISO 639-1 code).
type Lang string

const (
	IT Lang = "it"
	ES Lang = "es"
	EN Lang = "en"
)

// Default is the fallback language when none is recognized.
const Default = EN

var supported = map[Lang]bool{IT: true, ES: true, EN: true}

// Normalize converts an Accept-Language header value (or a lone code) to the
// best supported language; if none fits, it returns Default.
// E.g.: "es-ES,es;q=0.9,en;q=0.8" → ES.
func Normalize(accept string) Lang {
	for _, part := range strings.Split(accept, ",") {
		code := strings.TrimSpace(part)
		if i := strings.IndexByte(code, ';'); i >= 0 { // strip ";q=0.9"
			code = code[:i]
		}
		code = strings.ToLower(strings.TrimSpace(code))
		if i := strings.IndexByte(code, '-'); i >= 0 { // "es-ES" → "es"
			code = code[:i]
		}
		if l := Lang(code); supported[l] {
			return l
		}
	}
	return Default
}

// Coerce returns the language if supported; otherwise Default.
func Coerce(code string) Lang {
	if l := Lang(strings.ToLower(strings.TrimSpace(code))); supported[l] {
		return l
	}
	return Default
}

var weekdays = map[Lang][7]string{
	IT: {"Domenica", "Lunedì", "Martedì", "Mercoledì", "Giovedì", "Venerdì", "Sabato"},
	ES: {"Domingo", "Lunes", "Martes", "Miércoles", "Jueves", "Viernes", "Sábado"},
	EN: {"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
}

var months = map[Lang][13]string{
	IT: {"", "gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno", "luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre"},
	ES: {"", "enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
	EN: {"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
}

// FormatDateTime: "Lunedì 28 giugno 2026, 17:00" (per language).
func FormatDateTime(lang Lang, t time.Time) string {
	l := Coerce(string(lang))
	return fmt.Sprintf("%s %d %s %d, %02d:%02d",
		weekdays[l][int(t.Weekday())], t.Day(), months[l][int(t.Month())], t.Year(), t.Hour(), t.Minute())
}

// messages: catalog of strings with fmt-style placeholders. The keys are
// stable; the fallback is Default when a translation is missing.
var messages = map[Lang]map[string]string{
	ES: {
		"push.booked.title":     "Reservada ✓",
		"push.promoted.title":   "¡Has entrado! ✓",
		"push.waitlisted.title": "En lista de espera · posición %d",
		"push.failed.title":     "No se pudo reservar",
		"push.class":            "%s · %s · %s",
		"push.test.title":       "Notificaciones activadas",
		"push.test.body":        "Así te avisaré cuando reserve o no consiga plaza.",
	},
	EN: {
		"push.booked.title":     "Booked ✓",
		"push.promoted.title":   "You're in! ✓",
		"push.waitlisted.title": "On the waitlist · position %d",
		"push.failed.title":     "Couldn't book",
		"push.class":            "%s · %s · %s",
		"push.test.title":       "Notifications are on",
		"push.test.body":        "This is how I'll tell you when I book a class or can't get a spot.",
	},
	IT: {
		"push.booked.title":     "Prenotata ✓",
		"push.promoted.title":   "Sei dentro! ✓",
		"push.waitlisted.title": "In lista d'attesa · posizione %d",
		"push.failed.title":     "Prenotazione non riuscita",
		"push.class":            "%s · %s · %s",
		"push.test.title":       "Notifiche attivate",
		"push.test.body":        "Così ti avviserò quando prenoto una lezione o non trovo posto.",
	},
}

// T looks up the string by key in the given language (falling back to Default)
// and applies fmt.Sprintf with the arguments.
func T(lang Lang, key string, args ...any) string {
	l := Coerce(string(lang))
	s, ok := messages[l][key]
	if !ok {
		s = messages[Default][key]
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
