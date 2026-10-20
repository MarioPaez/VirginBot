package automation

import "time"

// defaultLength is assumed when a class's end time is unknown (rules created
// before the end was stored).
const defaultLength = time.Hour

// span parses "HH:MM" start/end into minutes since midnight; an unknown or
// non-positive end means start + defaultLength.
func span(start, end string) (from, to int, ok bool) {
	s, err := time.Parse("15:04", start)
	if err != nil {
		return 0, 0, false
	}
	from = s.Hour()*60 + s.Minute()
	to = from + int(defaultLength.Minutes())
	if e, err := time.Parse("15:04", end); err == nil {
		if m := e.Hour()*60 + e.Minute(); m > from {
			to = m
		}
	}
	return from, to, true
}

// Overlaps reports whether two classes on the same day overlap in time
// (touching ends, 18:00–19:00 and 19:00–20:00, don't).
func Overlaps(aStart, aEnd, bStart, bEnd string) bool {
	af, at, ok1 := span(aStart, aEnd)
	bf, bt, ok2 := span(bStart, bEnd)
	return ok1 && ok2 && af < bt && bf < at
}

// Overlapping returns the user's enabled rule (other than id) that would run
// at the same time as a rule for weekday start–end (once = its one-off date,
// "" = weekly): two weekly rules on the same weekday, or a one-off one on a
// date the other covers.
func (s *Store) Overlapping(userID int64, id string, weekday int, start, end, once string) (Rule, bool) {
	for _, r := range s.List(userID) {
		if r.ID == id || !r.Enabled || r.Weekday != weekday {
			continue
		}
		if once != "" && r.Once != "" && once != r.Once { // two different weeks
			continue
		}
		if Overlaps(start, end, r.Start, r.End) {
			return r, true
		}
	}
	return Rule{}, false
}
