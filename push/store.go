package push

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"
)

// Notifier keeps each user's push subscriptions (one per device) and sends to
// all of them. A nil *Notifier (push not configured) does nothing.
type Notifier struct {
	vapid  *VAPID
	db     *sql.DB
	client *http.Client
}

func NewNotifier(db *sql.DB, vapid *VAPID) *Notifier {
	return &Notifier{vapid: vapid, db: db, client: &http.Client{Timeout: 15 * time.Second}}
}

// PublicKey is the VAPID key the browser subscribes with ("" if disabled).
func (n *Notifier) PublicKey() string {
	if n == nil {
		return ""
	}
	return n.vapid.PublicKey()
}

// Subscribe stores (or moves to userID) a device's subscription.
func (n *Notifier) Subscribe(userID int64, s Subscription) error {
	if n == nil {
		return errors.New("push notifications are not configured")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("invalid push endpoint")
	}
	if _, err := unb64(s.Keys.P256dh); err != nil || s.Keys.Auth == "" {
		return errors.New("invalid push keys")
	}
	_, err = n.db.Exec(
		`INSERT INTO push_subscriptions (endpoint, user_id, p256dh, auth, created) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh, auth = excluded.auth`,
		s.Endpoint, userID, s.Keys.P256dh, s.Keys.Auth, time.Now().Format(time.RFC3339))
	return err
}

// Unsubscribe removes a device's subscription (only if it belongs to userID).
func (n *Notifier) Unsubscribe(userID int64, endpoint string) error {
	if n == nil {
		return nil
	}
	_, err := n.db.Exec(`DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

// Count returns how many devices the user has subscribed.
func (n *Notifier) Count(userID int64) int {
	if n == nil {
		return 0
	}
	var c int
	n.db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, userID).Scan(&c)
	return c
}

// Notify sends msg to every device of the user and returns how many accepted
// it. Subscriptions the push service reports as gone are deleted.
func (n *Notifier) Notify(userID int64, msg Message) int {
	if n == nil {
		return 0
	}
	rows, err := n.db.Query(`SELECT endpoint, p256dh, auth FROM push_subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		log.Printf("push u%d: %v", userID, err)
		return 0
	}
	var subs []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.Endpoint, &s.Keys.P256dh, &s.Keys.Auth); err == nil {
			subs = append(subs, s)
		}
	}
	rows.Close()

	sent := 0
	for _, s := range subs {
		switch err := n.vapid.Send(n.client, s, msg); {
		case err == nil:
			sent++
		case errors.Is(err, ErrGone):
			n.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, s.Endpoint)
			log.Printf("push u%d: subscription gone, removed", userID)
		default:
			log.Printf("push u%d: %v", userID, err)
		}
	}
	return sent
}
