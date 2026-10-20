package vapi

import "testing"

// Bodies captured from vapi in Oct 2026.
func TestClientRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"bad api key", 401, `{ "statusCode": 401, "message": "Unauthorized" }`, true},
		{"expired token", 401, "\n {\n \"error\": \"\"\n }\n", false},
		{"wrong password", 401, `{"error": "Si è verificato un problema durante l'accesso."}`, false},
		{"min app version", 426, ``, true},
		{"outage", 503, `{ "statusCode": 503 }`, false},
		{"empty 401", 401, ``, false},
	}
	for _, c := range cases {
		if got := clientRejected(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBookResponse(t *testing.T) {
	cases := []struct {
		body           string
		waitlisted     bool
		pos, bookingID int
	}{
		{`{"bookingId": 4048130, "status": "OVERBOOKED_WAITINGLIST", "waitingListIndex": 3}`, true, 3, 4048130},
		{`{"bookingId": 4036610, "status": "BOOKED"}`, false, 0, 4036610},
		{`not json`, false, 0, 0}, // the booking happened: never an error
	}
	for _, c := range cases {
		var r bookResponse
		if err := r.UnmarshalJSON([]byte(c.body)); err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if res := r.result(); res.Waitlisted != c.waitlisted || res.Position != c.pos || res.BookingID != c.bookingID {
			t.Fatalf("%s → %+v", c.body, r.result())
		}
	}
}
