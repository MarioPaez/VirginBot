// Package i18n centraliza las cadenas traducibles que ve el usuario en
// it/es/en: los nombres de fecha y los textos de los emails. La interfaz web
// tiene su propio catálogo en el frontend (server/web/index.html).
package i18n

import (
	"fmt"
	"strings"
	"time"
)

// Lang es un idioma soportado (código ISO 639-1 en minúscula).
type Lang string

const (
	IT Lang = "it"
	ES Lang = "es"
	EN Lang = "en"
)

// Default es el idioma de respaldo cuando no se reconoce ninguno.
const Default = EN

var supported = map[Lang]bool{IT: true, ES: true, EN: true}

// Normalize convierte un valor de cabecera Accept-Language (o un código suelto)
// al idioma soportado más adecuado; si ninguno encaja, devuelve Default.
// Ej.: "es-ES,es;q=0.9,en;q=0.8" → ES.
func Normalize(accept string) Lang {
	for _, part := range strings.Split(accept, ",") {
		code := strings.TrimSpace(part)
		if i := strings.IndexByte(code, ';'); i >= 0 { // quita ";q=0.9"
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

// Coerce devuelve el idioma si está soportado; si no, Default.
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

// Weekday devuelve el nombre del día de la semana (0=Domingo) en el idioma dado.
func Weekday(lang Lang, wd int) string { return weekdays[Coerce(string(lang))][wd] }

// FormatDateTime: "Lunedì 28 giugno 2026, 17:00" (según idioma).
func FormatDateTime(lang Lang, t time.Time) string {
	l := Coerce(string(lang))
	return fmt.Sprintf("%s %d %s %d, %02d:%02d",
		weekdays[l][int(t.Weekday())], t.Day(), months[l][int(t.Month())], t.Year(), t.Hour(), t.Minute())
}

// FormatDate: "Lunedì 28 giugno 2026" (según idioma).
func FormatDate(lang Lang, t time.Time) string {
	l := Coerce(string(lang))
	return fmt.Sprintf("%s %d %s %d", weekdays[l][int(t.Weekday())], t.Day(), months[l][int(t.Month())], t.Year())
}

// messages: catálogo de cadenas con marcadores estilo fmt. Las claves son
// estables; el fallback es Default si falta una traducción.
var messages = map[Lang]map[string]string{
	ES: {
		"email.booked.subject":  "VirginBot: ✓ reservada %s",
		"email.booked.body":     "¡Reserva conseguida! Te he apuntado automáticamente:\n\n%s\n",
		"email.bookednow.body":  "¡Reserva conseguida al automatizar! Te he apuntado a:\n\n%s\n\nLa automatización queda activa para reservar también las próximas semanas.\n",
		"email.attempts":        "Intentos: %d\n",
		"email.booked.outro":    "\nNos vemos en clase 💪\n",
		"email.failed.subject":  "VirginBot: ✗ NO se pudo reservar %s",
		"email.failed.body":     "No he conseguido reservar esta clase:\n\n%s\nIntentos: %d\nÚltimo motivo: %s\n\nLo intenté a la hora de la clase desde que abrió el plazo hasta 24h antes.\n",
		"email.failed.noreason": "no llegó a haber plaza reservable",
		"email.added.subject":   "VirginBot: automatización añadida — %s",
		"email.added.body":      "Nueva automatización activada. Reservaré esta clase automáticamente cada semana:\n\n%s\n\nIntentaré reservar en cuanto abra el plazo (a la hora de la clase) y te avisaré del resultado.\n",
		"email.removed.subject": "VirginBot: automatización quitada — %s",
		"email.removed.body":    "Has desactivado esta automatización. Ya no se reservará:\n\n%s\n",
		"block.class":           "Clase: %s\nClub: %s\nDía: %s",
		"summary.class":         "Clase: %s",
		"summary.club":          "Club: %s",
		"summary.next":          "Próxima clase: %s",
		"summary.window":        "Plazo de reserva: abre %d días antes (%s)",
		"summary.when":          "Cuándo: cada %s a las %s",
	},
	EN: {
		"email.booked.subject":  "VirginBot: ✓ booked %s",
		"email.booked.body":     "Booking confirmed! I've signed you up automatically:\n\n%s\n",
		"email.bookednow.body":  "Booked while automating! I've signed you up for:\n\n%s\n\nThe automation stays active to book the coming weeks too.\n",
		"email.attempts":        "Attempts: %d\n",
		"email.booked.outro":    "\nSee you in class 💪\n",
		"email.failed.subject":  "VirginBot: ✗ couldn't book %s",
		"email.failed.body":     "I couldn't book this class:\n\n%s\nAttempts: %d\nLast reason: %s\n\nI tried at class time, from when booking opened until 24h before.\n",
		"email.failed.noreason": "no bookable spot ever opened",
		"email.added.subject":   "VirginBot: automation added — %s",
		"email.added.body":      "New automation enabled. I'll book this class automatically every week:\n\n%s\n\nI'll try to book as soon as it opens (at class time) and let you know the result.\n",
		"email.removed.subject": "VirginBot: automation removed — %s",
		"email.removed.body":    "You've turned off this automation. It won't be booked anymore:\n\n%s\n",
		"block.class":           "Class: %s\nClub: %s\nDay: %s",
		"summary.class":         "Class: %s",
		"summary.club":          "Club: %s",
		"summary.next":          "Next class: %s",
		"summary.window":        "Booking window: opens %d days before (%s)",
		"summary.when":          "When: every %s at %s",
	},
	IT: {
		"email.booked.subject":  "VirginBot: ✓ prenotata %s",
		"email.booked.body":     "Prenotazione riuscita! Ti ho iscritto automaticamente:\n\n%s\n",
		"email.bookednow.body":  "Prenotata durante l'automazione! Ti ho iscritto a:\n\n%s\n\nL'automazione resta attiva per prenotare anche le prossime settimane.\n",
		"email.attempts":        "Tentativi: %d\n",
		"email.booked.outro":    "\nCi vediamo in lezione 💪\n",
		"email.failed.subject":  "VirginBot: ✗ impossibile prenotare %s",
		"email.failed.body":     "Non sono riuscito a prenotare questa lezione:\n\n%s\nTentativi: %d\nUltimo motivo: %s\n\nHo provato all'ora della lezione, da quando si sono aperte le prenotazioni fino a 24h prima.\n",
		"email.failed.noreason": "non si è mai liberato un posto prenotabile",
		"email.added.subject":   "VirginBot: automazione aggiunta — %s",
		"email.added.body":      "Nuova automazione attiva. Prenoterò questa lezione automaticamente ogni settimana:\n\n%s\n\nProverò a prenotare appena si aprono le prenotazioni (all'ora della lezione) e ti avviserò del risultato.\n",
		"email.removed.subject": "VirginBot: automazione rimossa — %s",
		"email.removed.body":    "Hai disattivato questa automazione. Non verrà più prenotata:\n\n%s\n",
		"block.class":           "Lezione: %s\nClub: %s\nGiorno: %s",
		"summary.class":         "Lezione: %s",
		"summary.club":          "Club: %s",
		"summary.next":          "Prossima lezione: %s",
		"summary.window":        "Prenotazioni: aprono %d giorni prima (%s)",
		"summary.when":          "Quando: ogni %s alle %s",
	},
}

// T busca la cadena por clave en el idioma dado (con fallback a Default) y le
// aplica fmt.Sprintf con los argumentos.
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
