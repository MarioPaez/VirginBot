package server

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/vapi"
)

// maxClubs caps the clubs a member follows: each one is a vapi call (~0.5 MB)
// on every calendar load.
const maxClubs = 5

// clubsRetry spaces out the club listing after a failure: it's on the path of
// every calendar and bookings sync.
const clubsRetry = 5 * time.Minute

// clubsFor returns the clubs whose classes the user sees: their choice, else
// their home club, else the server's default clubs.
func (s *Server) clubsFor(userID int64) []int {
	if ids := s.accounts.Clubs(userID); len(ids) > 0 {
		return ids
	}
	if home := s.accounts.HomeClub(userID); home != 0 {
		return []int{home}
	}
	return s.clubs
}

// ensureClubs registers every club vapi lists, once (with the user's session:
// the listing needs one). Until then only the clubs VirginBot started with have
// a name, and a class or booking elsewhere would come with an empty club.
func (s *Server) ensureClubs(userID int64) {
	if s.clubsLoaded.Load() || s.auth == nil {
		return
	}
	s.clubsMu.Lock()
	defer s.clubsMu.Unlock()
	if s.clubsLoaded.Load() || time.Since(s.clubsTried) < clubsRetry {
		return
	}
	var clubs []calendar.Club
	if err := s.auth.Do(userID, func(c *vapi.Client) (err error) { clubs, err = c.Clubs(); return err }); err != nil {
		s.clubsTried = time.Now()
		log.Printf("clubs: listing with u%d: %v", userID, err)
		return
	}
	calendar.AddClubs(clubs)
	s.clubsLoaded.Store(true)
	log.Printf("clubs: %d clubs registered", len(clubs))
}

type clubsView struct {
	All  []calendar.Club `json:"all"`  // every club, by name
	Mine []int           `json:"mine"` // the ones the user follows
	Home int             `json:"home"` // their home club (0 = unknown)
	Max  int             `json:"max"`
}

// handleClubs: GET the clubs to pick from and the user's; PUT {clubs: [ids]}.
func (s *Server) handleClubs(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
		s.ensureClubs(userID)
	case http.MethodPut:
		var req struct {
			Clubs []int `json:"clubs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		slices.Sort(req.Clubs)
		req.Clubs = slices.Compact(req.Clubs)
		if len(req.Clubs) == 0 || len(req.Clubs) > maxClubs {
			http.Error(w, "pick between 1 and 5 clubs", http.StatusBadRequest)
			return
		}
		for _, id := range req.Clubs {
			if calendar.ClubName(id) == "" {
				http.Error(w, "unknown club", http.StatusBadRequest)
				return
			}
		}
		if err := s.accounts.SetClubs(userID, req.Clubs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, clubsView{All: calendar.Clubs(), Mine: s.clubsFor(userID), Home: s.accounts.HomeClub(userID), Max: maxClubs})
}
