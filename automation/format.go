package automation

import (
	"strings"
	"time"

	"github.com/MarioPaez/VirginBot/i18n"
)

// nextOccurrence returns the next date/time (in loc) matching the rule's weekday
// and time, starting from `from` (included if it hasn't passed yet).
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

// windowDaysOf returns how many days ahead the rule's booking window opens.
func windowDaysOf(r Rule) int {
	if r.OpensDaysBefore > 0 {
		return r.OpensDaysBefore
	}
	return WindowDays(r.Name)
}

// Summary returns a readable block (in the given language) with a rule's key
// info: class, club, exact next occurrence and when the window opens.
func (r Rule) Summary(lang i18n.Lang, loc *time.Location) string {
	lines := []string{
		i18n.T(lang, "summary.class", r.Name),
		i18n.T(lang, "summary.club", r.Club),
	}
	if occ, ok := nextOccurrence(r, time.Now().In(loc), loc); ok {
		w := windowDaysOf(r)
		open := occ.Add(-time.Duration(w) * 24 * time.Hour)
		lines = append(lines,
			i18n.T(lang, "summary.next", i18n.FormatDateTime(lang, occ)),
			i18n.T(lang, "summary.window", w, i18n.FormatDateTime(lang, open)),
		)
	} else {
		lines = append(lines, i18n.T(lang, "summary.when", i18n.Weekday(lang, r.Weekday), r.Start))
	}
	return strings.Join(lines, "\n")
}

// classLines formats the class/club/day lines of a specific occurrence.
func classLines(lang i18n.Lang, name, club string, when time.Time) string {
	return i18n.T(lang, "block.class", name, club, i18n.FormatDateTime(lang, when))
}
