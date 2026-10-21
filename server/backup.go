package server

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// handleAdminBackup: GET downloads a consistent copy of the whole database, to
// keep one outside Fly (its volume snapshots live next to the volume). VACUUM
// INTO writes a compact snapshot while the app keeps running; the file holds the
// session tokens and the (encrypted) passwords, hence admin only.
func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir, err := os.MkdirTemp("", "virginbot-backup-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "virginbot.db") // VACUUM INTO needs a file that doesn't exist yet
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		log.Printf("backup: %v", err)
		http.Error(w, "backup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	name := "virginbot-" + time.Now().In(s.loc).Format("20060102-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
	if st, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	}
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("backup: sending: %v", err)
	}
}
