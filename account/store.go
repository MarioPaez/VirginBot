package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// Store guarda las credenciales de Virgin Active del usuario, CIFRADAS en reposo
// (AES-256-GCM). Hay que poder recuperarlas en claro para re-loguear de forma
// desatendida (la sesión caduca a las 2h y reservar 48h antes es automático).
type Store struct {
	path string
	gcm  cipher.AEAD
	mu   sync.Mutex
	cur  *creds
}

type creds struct {
	Email string `json:"email"`
	Pass  string `json:"pass"`
}

// NewStore abre (o crea) el almacén. `secret` deriva la clave de cifrado; debe
// ser estable entre reinicios (variable de entorno APP_SECRET).
func NewStore(path, secret string) (*Store, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, gcm: gcm}
	s.load()
	return s, nil
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	ns := s.gcm.NonceSize()
	if len(data) < ns {
		return
	}
	pt, err := s.gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return // clave incorrecta o fichero corrupto: arrancamos sin credenciales
	}
	var c creds
	if json.Unmarshal(pt, &c) == nil {
		s.cur = &c
	}
}

// Set cifra y persiste las credenciales.
func (s *Store) Set(email, pass string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pt, err := json.Marshal(creds{Email: email, Pass: pass})
	if err != nil {
		return err
	}
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ct := s.gcm.Seal(nonce, nonce, pt, nil)
	if err := os.WriteFile(s.path, ct, 0o600); err != nil {
		return fmt.Errorf("guardar credenciales: %w", err)
	}
	s.cur = &creds{Email: email, Pass: pass}
	return nil
}

// Get devuelve las credenciales guardadas.
func (s *Store) Get() (email, pass string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return "", "", false
	}
	return s.cur.Email, s.cur.Pass, true
}

// Email devuelve el email configurado (o "").
func (s *Store) Email() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ""
	}
	return s.cur.Email
}
