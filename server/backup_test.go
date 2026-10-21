package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MarioPaez/VirginBot/db"
)

// The backup is a complete SQLite database that opens with the same data.
func TestAdminBackup(t *testing.T) {
	s := testServer(t)
	if _, err := s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handleAdminBackup(rec, httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), ".db") {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}
	path := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(path, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	copyDB, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var email string
	if err := copyDB.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email); err != nil || email != "a@x" {
		t.Fatalf("restored email %q: %v", email, err)
	}
}
