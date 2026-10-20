package server

import (
	"os"
	"strconv"
	"time"
)

// Virgin's late-cancellation rule (reported by members, not from an official
// document): a booking (or a waitlist place) can't be cancelled within 2 h of
// the class, and 3 bookings not cancelled in 30 days block new bookings for 15
// days. Everything that books or reminds is planned around this line.

// cancelCutoff is how long before a class it can no longer be cancelled.
// VIRGINBOT_CANCEL_CUTOFF_MIN overrides it.
func cancelCutoff() time.Duration {
	return envMinutes("VIRGINBOT_CANCEL_CUTOFF_MIN", 2*time.Hour)
}

// watchCutoff is how long before the class watching stops: one hour more than
// the cancel line, so whoever gets a freed spot still has time to cancel it.
// VIRGINBOT_WATCH_CUTOFF_MIN overrides it.
func watchCutoff() time.Duration {
	return envMinutes("VIRGINBOT_WATCH_CUTOFF_MIN", cancelCutoff()+time.Hour)
}

// cancelDeadline is the last moment the class can be cancelled.
func cancelDeadline(start time.Time) time.Time { return start.Add(-cancelCutoff()) }

func envMinutes(key string, def time.Duration) time.Duration {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n >= 0 {
		return time.Duration(n) * time.Minute
	}
	return def
}
