package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MarioPaez/VirginBot/db"
)

// decrypt is the receiving side (the browser) of RFC 8291, written from the
// spec to check what Send puts on the wire.
func decrypt(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPublic, ciphertext := body[21:21+idlen], body[21+idlen:]
	if rs != recordSize {
		t.Fatalf("rs = %d", rs)
	}
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := ua.ECDH(as)
	ikm, _ := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPublic), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestNotifyEndToEnd(t *testing.T) {
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbh.Close()
	pub, priv, _ := GenerateVAPID()
	vapid, err := LoadVAPID(pub, priv, "mailto:admin@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// The device: its key pair and auth secret, as PushManager.subscribe() would create.
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)

	var mu sync.Mutex
	var got []byte
	var hdr http.Header
	svc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		mu.Lock()
		got, _ = io.ReadAll(r.Body)
		hdr = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer svc.Close()

	n := NewNotifier(dbh, vapid)
	n.client = svc.Client()
	sub := func(path string) Subscription {
		var s Subscription
		s.Endpoint = svc.URL + path
		s.Keys.P256dh = b64(ua.PublicKey().Bytes())
		s.Keys.Auth = b64(auth)
		return s
	}
	if err := n.Subscribe(7, sub("/ok")); err != nil {
		t.Fatal(err)
	}
	if err := n.Subscribe(7, sub("/gone")); err != nil {
		t.Fatal(err)
	}
	if c := n.Count(7); c != 2 {
		t.Fatalf("devices = %d", c)
	}

	if sent := n.Notify(7, Message{Title: "Reservada ✓", Body: "Calisthenics · Corso Como", URL: "/?view=bookings"}); sent != 1 {
		t.Fatalf("sent = %d, want 1", sent)
	}
	if c := n.Count(7); c != 1 {
		t.Fatalf("the gone subscription must be deleted; devices = %d", c)
	}

	mu.Lock()
	defer mu.Unlock()
	if hdr.Get("Content-Encoding") != "aes128gcm" || hdr.Get("TTL") == "" ||
		!strings.HasPrefix(hdr.Get("Authorization"), "vapid t=") || !strings.HasSuffix(hdr.Get("Authorization"), ", k="+pub) {
		t.Fatalf("headers = %v", hdr)
	}
	var msg Message
	if err := json.Unmarshal(decrypt(t, got, ua, auth), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Title != "Reservada ✓" || msg.URL != "/?view=bookings" {
		t.Fatalf("message = %+v", msg)
	}

	// Validation and ownership.
	bad := sub("/x")
	bad.Endpoint = "http://insecure.example/x"
	if err := n.Subscribe(7, bad); err == nil {
		t.Fatal("non-https endpoints must be rejected")
	}
	n.Unsubscribe(8, svc.URL+"/ok") // someone else's endpoint: no effect
	if c := n.Count(7); c != 1 {
		t.Fatalf("another user must not remove the subscription; devices = %d", c)
	}
	var nilN *Notifier
	if nilN.Notify(7, Message{}) != 0 || nilN.PublicKey() != "" {
		t.Fatal("a nil notifier must be a no-op")
	}
}
