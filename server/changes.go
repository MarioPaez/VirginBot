package server

import (
	"log"
	"strings"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/push"
	"github.com/MarioPaez/VirginBot/vapi"
)

// Changes Virgin makes to a booking on its own: the club cancels the class,
// moves it to another time or swaps the instructor. Reconciling only sees the
// before and after, so they're told apart here and the user gets a push instead
// of finding out at the gym.

// bookingChanges is what changed between the stored active bookings and the
// ones Virgin reports now.
type bookingChanges struct {
	instructor []instructorChange // same class, different instructor
	moved      []movedBooking     // same booking, another time
	vanished   []calendar.Class   // gone: cancelled by the club or by the user elsewhere (checked later)
}

type instructorChange struct {
	class calendar.Class // as it is now
	was   string
}

type movedBooking struct {
	from, to calendar.Class
}

func bookingKey(c calendar.Class) string { return c.Name + "|" + c.Club + "|" + c.Date + "|" + c.Start }

// diffBookings compares the active bookings stored before (old) with the active
// ones Virgin reports now (cur). Only classes starting after now count: started
// ones leave the list on their own. A booking that left its slot is "moved"
// when the same booking id (or, if Virgin issued a new one, the only new
// booking of that class and day) shows up at another time.
func diffBookings(old map[string]calendar.Class, cur []calendar.Class, started func(calendar.Class) bool) bookingChanges {
	var ch bookingChanges
	now := map[string]calendar.Class{}
	for _, c := range cur {
		if c.Booked {
			now[bookingKey(c)] = c
		}
	}
	// New bookings, a candidate destination for a moved one.
	fresh := map[string]calendar.Class{}
	for k, c := range now {
		if _, ok := old[k]; !ok {
			fresh[k] = c
		}
	}
	for k, o := range old {
		if started(o) {
			continue
		}
		if c, ok := now[k]; ok {
			if o.Instructor != "" && c.Instructor != "" && !strings.EqualFold(o.Instructor, c.Instructor) {
				ch.instructor = append(ch.instructor, instructorChange{class: c, was: o.Instructor})
			}
			continue
		}
		if to, ok := movedTo(o, fresh); ok {
			delete(fresh, bookingKey(to))
			ch.moved = append(ch.moved, movedBooking{from: o, to: to})
			continue
		}
		ch.vanished = append(ch.vanished, o)
	}
	return ch
}

// movedTo finds where a booking that left its slot went: the new booking with
// its id, else the only new one of the same class, club and day.
func movedTo(o calendar.Class, fresh map[string]calendar.Class) (calendar.Class, bool) {
	var same []calendar.Class
	for _, c := range fresh {
		if o.BookingID != 0 && c.BookingID == o.BookingID {
			return c, true
		}
		if c.Name == o.Name && c.Club == o.Club && c.Date == o.Date {
			same = append(same, c)
		}
	}
	if len(same) == 1 {
		return same[0], true
	}
	return calendar.Class{}, false
}

// notifyChanges pushes what Virgin changed. A vanished booking is only news if
// the club dropped the class: if it's still in the club's schedule, the user
// cancelled it from the official app (or from here, a moment before this sync).
func (s *Server) notifyChanges(userID int64, ch bookingChanges) {
	if s.push == nil {
		return // nobody to tell: skip the schedule checks too
	}
	for _, c := range ch.instructor {
		log.Printf("changes: u%d %s %s %s instructor %q → %q", userID, c.class.Name, c.class.Date, c.class.Start, c.was, c.class.Instructor)
		s.pushChange(userID, "push.change.instructor.title", c.class, c.class.Instructor)
	}
	for _, m := range ch.moved {
		log.Printf("changes: u%d %s %s moved %s → %s %s", userID, m.from.Name, m.from.Date, m.from.Start, m.to.Date, m.to.Start)
		s.pushChange(userID, "push.change.moved.title", m.to, m.from.Start)
	}
	for _, c := range ch.vanished {
		if !s.droppedByClub(userID, c) {
			continue
		}
		log.Printf("changes: u%d %s · %s %s %s cancelled by the club", userID, c.Name, c.Club, c.Date, c.Start)
		s.pushChange(userID, "push.change.cancelled.title", c)
	}
}

// droppedByClub reports whether c is no longer a class in its club's schedule.
// The schedule is read raw (not curated): the user may have booked a class
// VirginBot doesn't show. Any doubt (no club, an error) means no push.
func (s *Server) droppedByClub(userID int64, c calendar.Class) bool {
	if c.ClubID == 0 {
		return false
	}
	day, err := time.ParseInLocation("2006-01-02", c.Date, s.loc)
	if err != nil {
		return false
	}
	var classes []calendar.Class
	if err := s.auth.Do(userID, func(client *vapi.Client) (err error) {
		classes, err = client.Classes(c.ClubID, day, day)
		return err
	}); err != nil {
		log.Printf("changes u%d: checking %s %s: %v", userID, c.Name, c.Date, err)
		return false
	}
	return !inSchedule(classes, c)
}

// inSchedule reports whether c (same class, day and time) is still scheduled.
// vapi has no "cancelled" status we've seen; its only unknown status maps to
// "unavailable", which is treated as dropped too.
func inSchedule(classes []calendar.Class, c calendar.Class) bool {
	for _, k := range classes {
		if k.Date == c.Date && k.Start == c.Start && strings.EqualFold(k.Name, c.Name) && k.Status != "unavailable" {
			return true
		}
	}
	return false
}

func (s *Server) pushChange(userID int64, titleKey string, c calendar.Class, args ...any) {
	if s.push == nil {
		return
	}
	lang := s.langOf(userID)
	when, _ := s.startOf(c.Date, c.Start)
	s.push.Notify(userID, push.Message{
		Title: i18n.T(lang, titleKey, args...),
		Body:  i18n.T(lang, "push.class", c.Name, c.Club, i18n.FormatDateTime(lang, when)),
		URL:   "/?view=bookings",
	})
}
