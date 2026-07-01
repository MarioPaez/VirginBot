package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	_ "time/tzdata" // embeds the timezone database (Europe/Rome) into the binary

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/db"
	"github.com/MarioPaez/VirginBot/notification"
	"github.com/MarioPaez/VirginBot/server"
	"github.com/MarioPaez/VirginBot/vapi"
)

const defaultDBFile = "virginbot.db"

func main() {
	loadDotEnv(".env") // loads .env if present (doesn't override already-exported variables)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	dbPath := os.Getenv("VIRGINBOT_DB") // DB path (persistent volume in hosting)
	if dbPath == "" {
		dbPath = defaultDBFile
	}
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	secret := os.Getenv("APP_SECRET")
	if secret == "" {
		log.Fatal("APP_SECRET not set: it's required (encrypts users' passwords). " +
			"Generate one with `openssl rand -hex 32` and put it in the environment (.env locally, a secret in hosting).")
	}
	acc, err := account.NewStore(database, secret)
	if err != nil {
		log.Fatalf("account store: %v", err)
	}
	// Compatibility: if there are no users but there are credentials in the
	// environment, seed them as the first user (so it boots without the FE login).
	if len(acc.ListUserIDs()) == 0 {
		if e, p := os.Getenv("VA_EMAIL"), os.Getenv("VA_PASS"); e != "" && p != "" {
			if _, err := acc.Upsert(e, p); err != nil {
				log.Printf("warning: couldn't seed the initial user: %v", err)
			}
		}
	}
	apiKey := os.Getenv("VA_API_KEY")
	if apiKey == "" {
		log.Fatal("VA_API_KEY not set: it's the static key of Virgin's mobile app (x-api-key header). " +
			"Put it in the environment (.env locally, a secret in hosting).")
	}
	auth := server.NewAuth(acc, apiKey)

	if al := strings.TrimSpace(os.Getenv("VA_ALLOWED_EMAILS")); al != "" {
		log.Printf("restricted access: %d email(s) in the allowlist", len(strings.Split(al, ",")))
	} else {
		log.Println("OPEN access: any valid Virgin account can register (set VA_ALLOWED_EMAILS to restrict)")
	}

	clubs := []int{calendar.ClubCorsoComo, calendar.ClubCavour}
	// Classes we show: calisthenics, pilates (reformer), full body, TRX and cycle
	// in both clubs; solarium only in Corso Como.
	keep := func(c calendar.Class) bool {
		name := strings.ToLower(c.Name)
		switch {
		case strings.Contains(name, "calisthenics"),
			strings.Contains(name, "reformer"),
			strings.Contains(name, "pilates"),
			strings.Contains(name, "full body"),
			strings.Contains(name, "trx"),
			strings.Contains(name, "cycle"):
			return true
		case strings.Contains(name, "solarium"):
			return strings.Contains(c.Club, "Corso Como")
		default:
			return false
		}
	}

	store := automation.NewStore(database)

	sender := notification.FromEnv()
	srv := server.New(database, auth, acc, clubs, keep, store, sender)

	// notifyUser routes each notification to the matching user's email.
	notifyUser := func(userID int64, subject, body string) {
		if err := sender.Send(auth.Email(userID), subject, body); err != nil {
			log.Printf("email notification failed (u%d): %v", userID, err)
		}
	}

	// Automation engine: polls with precise timing (fresh fetch per day and user),
	// books with each user's authenticated client and notifies by email.
	engine := automation.NewEngine(store, srv.FreshDayFor, func(userID int64, clubID, classID, sessionID int, date string) error {
		// auth.Do re-logs in and retries if the token is expired (401).
		bookErr := auth.Do(userID, func(client *vapi.Client) error {
			return client.Book(clubID, classID, sessionID, date)
		})
		// ALWAYS reconcile the bookings: even if the API returns an error, the
		// booking may have gone through; the engine's next round detects it (overlay).
		srv.InvalidateUser(userID)
		return bookErr
	}, notifyUser, acc.Lang)
	go engine.Run(make(chan struct{}))

	log.Printf("API listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
