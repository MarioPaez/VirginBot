package server

import (
	"testing"

	"github.com/MarioPaez/VirginBot/calendar"
)

// Virgin's own changes are told apart: a new instructor on the same booking, the
// same booking at another time (same id, or the only new booking of that class
// and day), and a booking that's simply gone. Started classes are left alone.
func TestDiffBookings(t *testing.T) {
	c := func(name, date, start, instr string, id int) calendar.Class {
		return calendar.Class{Name: name, Club: "Milano Cavour", Date: date, Start: start, Instructor: instr, BookingID: id, Booked: true}
	}
	old := map[string]calendar.Class{}
	for _, o := range []calendar.Class{
		c("Calisthenics", "2026-10-22", "18:30", "Ale", 1), // instructor swapped
		c("Reformer", "2026-10-22", "09:00", "Bea", 2),     // moved, same id
		c("Cycle", "2026-10-23", "07:00", "Carla", 3),      // moved, new id
		c("TRX", "2026-10-24", "12:00", "Dani", 4),         // gone
		c("Lift Club", "2026-10-21", "08:00", "Eva", 5),    // started: ignored
		c("Full Body", "2026-10-25", "10:00", "", 6),       // no instructor before: not a swap
	} {
		old[bookingKey(o)] = o
	}
	cur := []calendar.Class{
		c("Calisthenics", "2026-10-22", "18:30", "Fede", 1),
		c("Reformer", "2026-10-22", "10:00", "Bea", 2),
		c("Cycle", "2026-10-23", "08:00", "Carla", 33),
		c("Full Body", "2026-10-25", "10:00", "Gio", 6),
		c("Calisthenics", "2026-10-26", "18:30", "Ale", 7), // a new booking, not a move
	}
	started := func(k calendar.Class) bool { return k.Date == "2026-10-21" }
	ch := diffBookings(old, cur, started)

	if len(ch.instructor) != 1 || ch.instructor[0].was != "Ale" || ch.instructor[0].class.Instructor != "Fede" {
		t.Fatalf("instructor changes: %+v", ch.instructor)
	}
	moves := map[string]string{}
	for _, m := range ch.moved {
		moves[m.from.Name] = m.to.Start
	}
	if len(moves) != 2 || moves["Reformer"] != "10:00" || moves["Cycle"] != "08:00" {
		t.Fatalf("moves: %+v", ch.moved)
	}
	if len(ch.vanished) != 1 || ch.vanished[0].Name != "TRX" {
		t.Fatalf("vanished: %+v", ch.vanished)
	}
}

// Two new bookings of the same class and day can't tell which one a vanished
// booking became: it's left as vanished (the schedule check decides).
func TestDiffBookingsAmbiguousMove(t *testing.T) {
	o := calendar.Class{Name: "Solarium", Club: "Milano Corso Como", Date: "2026-10-22", Start: "10:00", BookingID: 1, Booked: true}
	a, b := o, o
	a.Start, a.BookingID = "11:00", 2
	b.Start, b.BookingID = "12:00", 3
	ch := diffBookings(map[string]calendar.Class{bookingKey(o): o}, []calendar.Class{a, b}, func(calendar.Class) bool { return false })
	if len(ch.moved) != 0 || len(ch.vanished) != 1 {
		t.Fatalf("got %+v", ch)
	}
}

// A class still in the schedule wasn't dropped by the club (the user cancelled
// it elsewhere); an "unavailable" one (vapi's unknown status) counts as dropped.
func TestInSchedule(t *testing.T) {
	c := calendar.Class{Name: "Calisthenics", Date: "2026-10-22", Start: "18:30"}
	day := []calendar.Class{
		{Name: "calisthenics", Date: "2026-10-22", Start: "18:30", Status: "bookable"},
		{Name: "TRX", Date: "2026-10-22", Start: "19:30", Status: "unavailable"},
	}
	if !inSchedule(day, c) {
		t.Fatal("scheduled class reported as dropped")
	}
	c.Name, c.Start = "TRX", "19:30"
	if inSchedule(day, c) {
		t.Fatal("unavailable class reported as scheduled")
	}
	c.Start = "20:30"
	if inSchedule(day, c) {
		t.Fatal("missing class reported as scheduled")
	}
}
