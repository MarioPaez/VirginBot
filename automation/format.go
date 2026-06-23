package automation

import (
	"fmt"
	"strings"
	"time"
)

var monthsIT = [...]string{
	"", "gennaio", "febbraio", "marzo", "aprile", "maggio", "giugno",
	"luglio", "agosto", "settembre", "ottobre", "novembre", "dicembre",
}

// FormatDateTimeIT formatea una fecha/hora en italiano: "Domenica 28 giugno 2026, 17:00".
func FormatDateTimeIT(t time.Time) string {
	return fmt.Sprintf("%s %d %s %d, %02d:%02d",
		weekdaysIT[int(t.Weekday())], t.Day(), monthsIT[int(t.Month())], t.Year(),
		t.Hour(), t.Minute())
}

// nextOccurrence devuelve la próxima fecha/hora (en loc) que casa con el día de
// la semana y la hora de la regla, a partir de `from` (incluido si aún no pasó).
func nextOccurrence(r Rule, from time.Time, loc *time.Location) (time.Time, bool) {
	for d := 0; d <= 7; d++ {
		day := from.AddDate(0, 0, d)
		if int(day.Weekday()) != r.Weekday {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+r.Start, loc)
		if err != nil {
			return time.Time{}, false
		}
		if t.Before(from) {
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// windowDaysOf devuelve los días de antelación con que abre el plazo de la regla.
func windowDaysOf(r Rule) int {
	if r.OpensDaysBefore > 0 {
		return r.OpensDaysBefore
	}
	return WindowDays(r.Name)
}

// Summary devuelve un bloque legible con la info relevante de una regla:
// clase, club, próxima ocurrencia exacta y cuándo abre el plazo de reserva.
func (r Rule) Summary(loc *time.Location) string {
	lines := []string{
		"Clase: " + r.Name,
		"Club: " + r.Club,
	}
	if occ, ok := nextOccurrence(r, time.Now().In(loc), loc); ok {
		w := windowDaysOf(r)
		open := occ.Add(-time.Duration(w) * 24 * time.Hour)
		lines = append(lines,
			"Próxima clase: "+FormatDateTimeIT(occ),
			fmt.Sprintf("Plazo de reserva: abre %d días antes (%s)", w, FormatDateTimeIT(open)),
		)
	} else {
		lines = append(lines, fmt.Sprintf("Cuándo: cada %s a las %s", weekdaysIT[r.Weekday], r.Start))
	}
	return strings.Join(lines, "\n")
}

// classLines formatea las líneas clase/club/día de una ocurrencia concreta.
func classLines(name, club string, when time.Time) string {
	return fmt.Sprintf("Clase: %s\nClub: %s\nDía: %s", name, club, FormatDateTimeIT(when))
}
