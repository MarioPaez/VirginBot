// Package push sends Web Push notifications (RFC 8030) to the browsers/devices
// users subscribe from: payload encrypted with aes128gcm (RFC 8291) and the
// server identified with VAPID (RFC 8292). Standard library only.
package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// recordSize is the aes128gcm record size we declare; the whole payload must fit
// in a single record (plaintext + 1 delimiter byte + 16 tag bytes).
const recordSize = 4096

// maxPayload keeps us well under recordSize and the push services' 4 KB limit.
const maxPayload = 3000

// ErrGone means the push service no longer knows the subscription (the user
// revoked permission or uninstalled the app): it must be deleted.
var ErrGone = errors.New("push: subscription expired")

// Subscription is what the browser's PushManager.subscribe() returns
// (PushSubscription.toJSON()).
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Message is the JSON payload the service worker turns into a notification.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"` // page to open on tap
}

// VAPID holds the application server's key pair and contact.
type VAPID struct {
	priv    *ecdsa.PrivateKey
	public  string // base64url, uncompressed P-256 point: what the browser needs as applicationServerKey
	subject string // "mailto:…" or "https://…": how push services reach the operator
}

// GenerateVAPID returns a new key pair, base64url-encoded (public = uncompressed
// point, private = raw 32-byte scalar), to put in the environment once.
func GenerateVAPID() (public, private string, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return "", "", err
	}
	priv, err := k.Bytes()
	if err != nil {
		return "", "", err
	}
	return b64(pub), b64(priv), nil
}

// LoadVAPID parses the key pair produced by GenerateVAPID.
func LoadVAPID(public, private, subject string) (*VAPID, error) {
	raw, err := unb64(private)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	if b64(pub) != strings.TrimRight(public, "=") {
		return nil, errors.New("vapid: the public key doesn't match the private key")
	}
	if subject == "" {
		return nil, errors.New("vapid: a subject (mailto: or https: URL) is required")
	}
	return &VAPID{priv: k, public: b64(pub), subject: subject}, nil
}

// PublicKey is the applicationServerKey the browser subscribes with.
func (v *VAPID) PublicKey() string { return v.public }

// Send encrypts msg for sub and posts it to the push service. A 404/410 answer
// returns ErrGone.
func (v *VAPID) Send(client *http.Client, sub Subscription, msg Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(payload) > maxPayload {
		return fmt.Errorf("push: payload too large (%d bytes)", len(payload))
	}
	uaPublic, err := unb64(sub.Keys.P256dh)
	if err != nil {
		return fmt.Errorf("push: p256dh: %w", err)
	}
	auth, err := unb64(sub.Keys.Auth)
	if err != nil {
		return fmt.Errorf("push: auth: %w", err)
	}
	asKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	body, err := encrypt(payload, uaPublic, auth, asKey, salt)
	if err != nil {
		return err
	}
	authz, err := v.authorization(sub.Endpoint, time.Now())
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400") // a booking result is still worth showing for a day
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", authz)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("push: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// encrypt builds an aes128gcm body (RFC 8291 §3–4 over RFC 8188) for a single
// record. asKey and salt are parameters so the RFC test vector can pin them.
func encrypt(payload, uaPublic, authSecret []byte, asKey *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("push: invalid p256dh: %w", err)
	}
	secret, err := asKey.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := asKey.PublicKey().Bytes()

	// IKM = HKDF(auth, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	prkKey, err := hkdf.Extract(sha256.New, secret, authSecret)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Single (and therefore last) record: payload followed by the 0x02 delimiter.
	plaintext := append(append([]byte{}, payload...), 0x02)
	if len(plaintext)+gcm.Overhead() > recordSize {
		return nil, errors.New("push: payload doesn't fit in one record")
	}

	// Header: salt(16) || rs(4) || idlen(1) || keyid(as_public)
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(recordSize))
	out.WriteByte(byte(len(asPublic)))
	out.Write(asPublic)
	out.Write(gcm.Seal(nil, nonce, plaintext, nil))
	return out.Bytes(), nil
}

// authorization returns the VAPID header: an ES256 JWT for the push service's
// origin plus our public key.
func (v *VAPID) authorization(endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("push: invalid endpoint %q", endpoint)
	}
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(), // RFC 8292: at most 24 h
		"sub": v.subject,
	})
	if err != nil {
		return "", err
	}
	unsigned := b64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, v.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64) // JWS ES256: r || s, 32 bytes each
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + unsigned + "." + b64(sig) + ", k=" + v.public, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// unb64 accepts base64url with or without padding (browsers vary).
func unb64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
