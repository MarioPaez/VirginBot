package automation

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Rule define una clase recurrente a reservar automáticamente, identificada por
// nombre + club + día de la semana + hora de inicio (el bookingId cambia en cada
// ocurrencia, así que no sirve para identificar la regla).
type Rule struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`    // p. ej. "Calisthenics Performance"
	Club    string    `json:"club"`    // p. ej. "Milano Corso Como"
	Weekday int       `json:"weekday"` // time.Weekday: 0=Domingo .. 6=Sábado
	Start   string    `json:"start"`   // "HH:MM"
	Enabled bool      `json:"enabled"`
	Created time.Time `json:"created"`
}

// key es la identidad lógica de una regla (sin contar el estado).
func ruleKey(name, club string, weekday int, start string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%s", name, club, weekday, start)))
	return hex.EncodeToString(sum[:])[:12]
}

// Store persiste las reglas de automatización en un fichero JSON.
type Store struct {
	path  string
	mu    sync.Mutex
	rules []Rule
}

// NewStore carga las reglas existentes (si las hay) del fichero.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leer %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s.rules); err != nil {
		return nil, fmt.Errorf("parsear reglas: %w", err)
	}
	return s, nil
}

// List devuelve una copia de las reglas.
func (s *Store) List() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, len(s.rules))
	copy(out, s.rules)
	return out
}

// Add inserta (o reactiva) una regla. Es idempotente: misma identidad lógica =
// misma ID, no se duplica.
func (s *Store) Add(name, club string, weekday int, start string) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := ruleKey(name, club, weekday, start)
	for i := range s.rules {
		if s.rules[i].ID == id {
			s.rules[i].Enabled = true
			return s.rules[i], s.save()
		}
	}
	r := Rule{ID: id, Name: name, Club: club, Weekday: weekday, Start: start, Enabled: true, Created: time.Now()}
	s.rules = append(s.rules, r)
	return r, s.save()
}

// Remove elimina una regla por ID.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(id)
}

// RemoveMatching elimina la regla que coincide con la clase indicada (usado al
// desapuntarse para que no se vuelva a automatizar). Devuelve true si borró algo.
func (s *Store) RemoveMatching(name, club string, weekday int, start string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ruleKey(name, club, weekday, start)
	for _, r := range s.rules {
		if r.ID == id {
			return true, s.removeLocked(id)
		}
	}
	return false, nil
}

func (s *Store) removeLocked(id string) error {
	out := s.rules[:0]
	for _, r := range s.rules {
		if r.ID != id {
			out = append(out, r)
		}
	}
	s.rules = out
	return s.save()
}

// save escribe las reglas a disco (llamar con el lock tomado).
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
