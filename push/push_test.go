package push

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// RFC 8291 Appendix A: the encryption must reproduce the published message.
func TestEncryptRFC8291Vector(t *testing.T) {
	mustB64 := func(s string) []byte {
		b, err := unb64(s)
		if err != nil {
			t.Fatalf("decode %q: %v", s, err)
		}
		return b
	}
	asKey, err := ecdh.P256().NewPrivateKey(mustB64("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := b64(asKey.PublicKey().Bytes()),
		"BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"; got != want {
		t.Fatalf("as_public = %s, want %s", got, want)
	}
	body, err := encrypt(
		[]byte("When I grow up, I want to be a watermelon"),
		mustB64("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		mustB64("BTBZMqHH6r4Tts7J_aSIgg"),
		asKey,
		mustB64("DGv6ra1nlYgDCS1FRnbzlw"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64(body); got != want {
		t.Fatalf("encrypted body mismatch\n got  %s\n want %s", got, want)
	}
}

func TestVAPIDRoundTripAndJWT(t *testing.T) {
	pub, priv, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	v, err := LoadVAPID(pub, priv, "mailto:admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVAPID(pub, priv, ""); err == nil {
		t.Fatal("a subject must be required")
	}
	other, _, _ := GenerateVAPID()
	if _, err := LoadVAPID(other, priv, "mailto:a@b.c"); err == nil {
		t.Fatal("a mismatched public key must be rejected")
	}

	now := time.Unix(1_700_000_000, 0)
	h, err := v.authorization("https://web.push.apple.com/QGuQyavXutnMH7?x=1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, ", k="+pub) {
		t.Fatalf("bad header: %s", h)
	}
	jwt := strings.TrimSuffix(strings.TrimPrefix(h, "vapid t="), ", k="+pub)
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	raw, _ := unb64(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://web.push.apple.com" || claims.Sub != "mailto:admin@example.com" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims = %+v", claims)
	}
	// The ES256 signature (r||s) must verify with the public key.
	sig, _ := unb64(parts[2])
	pubBytes, _ := unb64(pub)
	x, y := new(big.Int).SetBytes(pubBytes[1:33]), new(big.Int).SetBytes(pubBytes[33:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:],
		new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("JWT signature doesn't verify")
	}
}
