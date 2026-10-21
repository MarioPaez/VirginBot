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

// ShortWeekday: "Lun", "Mié", "Wed" (three letters of the weekday).
func ShortWeekday(lang Lang, t time.Time) string {
	r := []rune(weekdays[Coerce(string(lang))][int(t.Weekday())])
	return string(r[:min(3, len(r))])
}

// DayWord names t's day relative to now: "today", "tomorrow" or the weekday
// (lowercase, to go mid-sentence).
func DayWord(lang Lang, t, now time.Time) string {
	l := Coerce(string(lang))
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	switch int(day(t).Sub(day(now)).Hours() / 24) {
	case 0:
		return T(l, "day.today")
	case 1:
		return T(l, "day.tomorrow")
	}
	if l == EN {
		return weekdays[l][int(t.Weekday())]
	}
	return strings.ToLower(weekdays[l][int(t.Weekday())])
}

// messages: catalog of strings with fmt-style placeholders. The keys are
// stable; the fallback is Default when a translation is missing.
var messages = map[Lang]map[string]string{
	ES: {
		"push.booked.title":            "Reservada ✓",
		"push.promoted.title":          "¡Has entrado! ✓",
		"push.waitlisted.title":        "En lista de espera · posición %d",
		"push.failed.title":            "No se pudo reservar",
		"push.class":                   "%s · %s · %s",
		"push.test.title":              "Notificaciones activadas",
		"push.test.body":               "Así te avisaré cuando reserve o no consiga plaza.",
		"push.watch.booked.title":      "¡Se liberó una plaza! Reservada ✓ · cancelable hasta las %s",
		"push.watch.missed.title":      "No se liberó ninguna plaza",
		"push.watch.failed.title":      "Virgin rechaza la reserva: dejo de vigilarla",
		"push.watch.started.title":     "Sigue llena: la vigilo por si se libera una plaza",
		"push.reminder.title":          "%s · %s %s",
		"push.reminder.body":           "¿No puedes ir? Toca para cancelarla antes de las %s y que no cuente como falta.",
		"day.today":                    "hoy",
		"day.tomorrow":                 "mañana",
		"push.friend.title":            "%s también va a tu clase",
		"push.creds.title":             "Virgin ha rechazado tu contraseña",
		"push.creds.body":              "Tus automatizaciones están paradas. Entra en VirginBot con tu contraseña actual de Virgin Active.",
		"push.change.cancelled.title":  "Virgin ha cancelado tu clase",
		"push.change.moved.title":      "Tu clase cambia de hora (era a las %s)",
		"push.change.instructor.title": "Cambio de instructor: ahora %s",
		"push.week.title":              "La semana que viene tienes %d clases",
		"push.week.title.one":          "La semana que viene tienes 1 clase",
		"push.week.title.none":         "La semana que viene aún no tienes clases",
		"push.week.more":               "y %d más",
		"push.week.pending":            "%d automatizaciones aún por reservar",
		"push.week.pending.one":        "1 automatización aún por reservar",
		"push.week.waitlisted":         "En lista de espera: %d",
	},
	EN: {
		"push.booked.title":            "Booked ✓",
		"push.promoted.title":          "You're in! ✓",
		"push.waitlisted.title":        "On the waitlist · position %d",
		"push.failed.title":            "Couldn't book",
		"push.class":                   "%s · %s · %s",
		"push.test.title":              "Notifications are on",
		"push.test.body":               "This is how I'll tell you when I book a class or can't get a spot.",
		"push.watch.booked.title":      "A spot freed up! Booked ✓ · cancellable until %s",
		"push.watch.missed.title":      "No spot freed up",
		"push.watch.failed.title":      "Virgin keeps refusing the booking: stopped watching",
		"push.watch.started.title":     "Still full: I'm watching it for a freed spot",
		"push.reminder.title":          "%s · %s %s",
		"push.reminder.body":           "Can't make it? Tap to cancel before %s so it doesn't count as a no-show.",
		"day.today":                    "today",
		"day.tomorrow":                 "tomorrow",
		"push.friend.title":            "%s is going to your class too",
		"push.creds.title":             "Virgin rejected your password",
		"push.creds.body":              "Your automations are stopped. Sign in to VirginBot with your current Virgin Active password.",
		"push.change.cancelled.title":  "Virgin cancelled your class",
		"push.change.moved.title":      "Your class moved (it was at %s)",
		"push.change.instructor.title": "New instructor: %s",
		"push.week.title":              "Next week you have %d classes",
		"push.week.title.one":          "Next week you have 1 class",
		"push.week.title.none":         "No classes booked for next week yet",
		"push.week.more":               "and %d more",
		"push.week.pending":            "%d automations still to book",
		"push.week.pending.one":        "1 automation still to book",
		"push.week.waitlisted":         "On the waitlist: %d",
	},
	IT: {
		"push.booked.title":            "Prenotata ✓",
		"push.promoted.title":          "Sei dentro! ✓",
		"push.waitlisted.title":        "In lista d'attesa · posizione %d",
		"push.failed.title":            "Prenotazione non riuscita",
		"push.class":                   "%s · %s · %s",
		"push.test.title":              "Notifiche attivate",
		"push.test.body":               "Così ti avviserò quando prenoto una lezione o non trovo posto.",
		"push.watch.booked.title":      "Si è liberato un posto! Prenotata ✓ · annullabile fino alle %s",
		"push.watch.missed.title":      "Non si è liberato nessun posto",
		"push.watch.failed.title":      "Virgin rifiuta la prenotazione: smetto di osservarla",
		"push.watch.started.title":     "Ancora piena: la tengo d'occhio se si libera un posto",
		"push.reminder.title":          "%s · %s %s",
		"push.reminder.body":           "Non puoi venire? Tocca per annullarla entro le %s e non conterà come assenza.",
		"day.today":                    "oggi",
		"day.tomorrow":                 "domani",
		"push.friend.title":            "Anche %s viene alla tua lezione",
		"push.creds.title":             "Virgin ha rifiutato la tua password",
		"push.creds.body":              "Le tue automazioni sono ferme. Accedi a VirginBot con la password attuale di Virgin Active.",
		"push.change.cancelled.title":  "Virgin ha annullato la tua lezione",
		"push.change.moved.title":      "La tua lezione cambia orario (era alle %s)",
		"push.change.instructor.title": "Cambio istruttore: ora %s",
		"push.week.title":              "La settimana prossima hai %d lezioni",
		"push.week.title.one":          "La settimana prossima hai 1 lezione",
		"push.week.title.none":         "La settimana prossima non hai ancora lezioni",
		"push.week.more":               "e altre %d",
		"push.week.pending":            "%d automazioni ancora da prenotare",
		"push.week.pending.one":        "1 automazione ancora da prenotare",
		"push.week.waitlisted":         "In lista d'attesa: %d",
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
