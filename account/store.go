package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"time"
)

// Store guarda las cuentas de Virgin Active de los usuarios en SQLite, con la
// contraseña CIFRADA (AES-256-GCM). Hay que poder recuperarla en claro para
// re-loguear de forma desatendida (la sesión de Virgin caduca a las ~2h).
//
// La identidad de un usuario es su email de Virgin (único). El cifrado es
// stateless: cada lectura descifra desde la BD, así no mantenemos secretos en
// memoria más de lo necesario.
type Store struct {
	db  *sql.DB
	gcm cipher.AEAD
}

// NewStore abre el almacén sobre la BD. `secret` deriva la clave de cifrado;
// debe ser estable entre reinicios (variable de entorno APP_SECRET).
func NewStore(db *sql.DB, secret string) (*Store, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, gcm: gcm}, nil
}

// Upsert crea o actualiza un usuario por email (cifrando la contraseña) y
// devuelve su id.
func (s *Store) Upsert(email, pass string) (int64, error) {
	enc, err := s.encrypt(pass)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(
		`INSERT INTO users (email, pass, created) VALUES (?, ?, ?)
		 ON CONFLICT(email) DO UPDATE SET pass = excluded.pass`,
		email, enc, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("guardar usuario: %w", err)
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// Get devuelve las credenciales (email + contraseña en claro) de un usuario.
func (s *Store) Get(userID int64) (email, pass string, ok bool) {
	var enc []byte
	if err := s.db.QueryRow(`SELECT email, pass FROM users WHERE id = ?`, userID).Scan(&email, &enc); err != nil {
		return "", "", false
	}
	pt, err := s.decrypt(enc)
	if err != nil {
		return "", "", false
	}
	return email, pt, true
}

// Email devuelve el email de un usuario (o "").
func (s *Store) Email(userID int64) string {
	var email string
	if err := s.db.QueryRow(`SELECT email FROM users WHERE id = ?`, userID).Scan(&email); err != nil {
		return ""
	}
	return email
}

// Lang devuelve el idioma preferido del usuario (o "" si aún no se ha fijado).
func (s *Store) Lang(userID int64) string {
	var lang string
	if err := s.db.QueryRow(`SELECT lang FROM users WHERE id = ?`, userID).Scan(&lang); err != nil {
		return ""
	}
	return lang
}

// SetLang fija el idioma preferido del usuario (código it/es/en).
func (s *Store) SetLang(userID int64, lang string) error {
	_, err := s.db.Exec(`UPDATE users SET lang = ? WHERE id = ?`, lang, userID)
	return err
}

// ListUserIDs devuelve los ids de todos los usuarios registrados.
func (s *Store) ListUserIDs() []int64 {
	rows, err := s.db.Query(`SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// ---- cifrado ----

func (s *Store) encrypt(pass string) ([]byte, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.gcm.Seal(nonce, nonce, []byte(pass), nil), nil
}

func (s *Store) decrypt(enc []byte) (string, error) {
	ns := s.gcm.NonceSize()
	if len(enc) < ns {
		return "", fmt.Errorf("dato cifrado inválido")
	}
	pt, err := s.gcm.Open(nil, enc[:ns], enc[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
