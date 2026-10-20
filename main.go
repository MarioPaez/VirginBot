package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	_ "time/tzdata" // embeds the timezone database (Europe/Rome) into the binary

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/db"
	"github.com/MarioPaez/VirginBot/push"
	"github.com/MarioPaez/VirginBot/server"
	"github.com/MarioPaez/VirginBot/vapi"
)

const defaultDBFile = "virginbot.db"

func main() {
	// `virginbot vapid` prints a fresh key pair for Web Push and exits.
	if len(os.Args) > 1 && os.Args[1] == "vapid" {
		pub, priv, err := push.GenerateVAPID()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("VAPID_PUBLIC_KEY=%s\nVAPID_PRIVATE_KEY=%s\n", pub, priv)
		return
	}
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

	clubs := []int{calendar.ClubCorsoComo, calendar.ClubCavour, calendar.ClubPalermo}
	// Classes we show: calisthenics, lift, pilates (reformer), full body, TRX and
	// cycle in every club; solarium only in Corso Como.
	keep := func(c calendar.Class) bool {
		name := strings.ToLower(c.Name)
		switch {
		case strings.Contains(name, "calisthenics"),
			strings.Contains(name, "lift club"),
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

	srv := server.New(database, auth, acc, clubs, keep, store)
	notifier := pushFromEnv(database) // nil = no notifications at all
	srv.SetPush(notifier)

	// Admin alerts go to the admin's devices (looked up at alert time: the admin
	// may sign in after startup). nil = no admin alerts.
	var adminAlert func(title, body string)
	if admin := strings.TrimSpace(os.Getenv("VA_ADMIN_EMAIL")); admin != "" && notifier != nil {
		adminAlert = func(title, body string) {
			for _, uid := range acc.ListUserIDs() {
				if strings.EqualFold(acc.Email(uid), admin) && notifier.Notify(uid, push.Message{Title: title, Body: body, URL: "/?view=admin"}) > 0 {
					return
				}
			}
			log.Printf("admin alert not delivered: %s has no device with notifications on", admin)
		}
		// Outdated x-api-key / app version breaks everyone.
		auth.OnClientRejected(adminAlert)
	} else {
		log.Println("warning: no admin alerts (needs VA_ADMIN_EMAIL and push notifications)")
	}

	// Automation engine: precise fires (fresh fetch of the class's club), books
	// with each user's authenticated client and pushes the outcome.
	engine := automation.NewEngine(store, srv.FreshDayFor, func(userID int64, clubID, classID, sessionID int, date string) error {
		// auth.Do re-logs in and retries if the token is expired (401).
		var res vapi.BookResult
		bookErr := auth.Do(userID, func(client *vapi.Client) error {
			var err error
			res, err = client.Book(clubID, classID, sessionID, date)
			return err
		})
		if bookErr == nil && res.Waitlisted {
			bookErr = automation.ErrWaitlisted{Position: res.Position}
		}
		// ALWAYS reconcile the bookings: even if the API returns an error, the
		// booking may have gone through; the engine's next round detects it (overlay).
		srv.InvalidateUser(userID)
		return bookErr
	}, acc.Lang)
	if notifier != nil {
		engine.SetPush(func(userID int64, title, body, url string) {
			go notifier.Notify(userID, push.Message{Title: title, Body: body, URL: url}) // never delay the engine
		})
	}
	// VIRGINBOT_NO_ENGINE=1 (local UI work against the real accounts): don't run
	// the engine, or it would book alongside production.
	if os.Getenv("VIRGINBOT_NO_ENGINE") == "1" {
		log.Println("automation engine DISABLED (VIRGINBOT_NO_ENGINE=1)")
	} else {
		go engine.Run(make(chan struct{}))
		srv.SetEngineHealth(engine.LastCycle)
		if adminAlert != nil {
			go automation.Watchdog(engine, adminAlert)
		}
	}

	log.Printf("API listening on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}

// pushFromEnv builds the Web Push notifier from VAPID_* (generate them with
// `virginbot vapid`), or nil if they're missing. The subject defaults to the
// admin's email: push services use it to contact the operator.
func pushFromEnv(database *sql.DB) *push.Notifier {
	pub, priv := os.Getenv("VAPID_PUBLIC_KEY"), os.Getenv("VAPID_PRIVATE_KEY")
	if pub == "" || priv == "" {
		log.Println("push notifications disabled (VAPID_PUBLIC_KEY / VAPID_PRIVATE_KEY not set)")
		return nil
	}
	subject := os.Getenv("VAPID_SUBJECT")
	if subject == "" {
		if admin := strings.TrimSpace(os.Getenv("VA_ADMIN_EMAIL")); admin != "" {
			subject = "mailto:" + admin
		}
	}
	vapid, err := push.LoadVAPID(pub, priv, subject)
	if err != nil {
		log.Printf("push notifications disabled: %v", err)
		return nil
	}
	log.Println("push notifications enabled")
	return push.NewNotifier(database, vapid)
}
