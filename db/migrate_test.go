package db

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateRealDB validates the single→multi-user migration on a COPY of the
// project's real database (../virginbot.db), without touching the original. If it
// doesn't exist, it's skipped (e.g. in CI with a clean DB).
func TestMigrateRealDB(t *testing.T) {
	src := filepath.Join("..", "virginbot.db")
	if _, err := os.Stat(src); err != nil {
		t.Skip("no virginbot.db; skipping the real migration test")
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	// Copy the three files (db + WAL + SHM) to preserve data not yet flushed to the
	// main file.
	for _, suf := range []string{"", "-wal", "-shm"} {
		if err := copyFile(src+suf, dst+suf); err != nil && suf == "" {
			t.Fatalf("copy %s: %v", src+suf, err)
		}
	}

	d, err := Open(dst)
	if err != nil {
		t.Fatalf("Open (migration): %v", err)
	}
	defer d.Close()

	// Tables present.
	for _, tbl := range []string{"users", "bookings", "automations", "sessions"} {
		if ok, _ := tableExists(d, tbl); !ok {
			t.Errorf("missing table %q after migrating", tbl)
		}
	}
	// The DB calendar cache no longer exists (calendar is live from vapi).
	if ok, _ := tableExists(d, "day_cache"); ok {
		t.Errorf("day_cache should have been dropped")
	}
	// user_id columns added.
	for _, tc := range []struct{ table, col string }{
		{"automations", "user_id"}, {"sessions", "user_id"},
	} {
		if ok, _ := hasColumn(d, tc.table, tc.col); !ok {
			t.Errorf("%s is missing the %s column", tc.table, tc.col)
		}
	}
	// The legacy user must have been migrated (id=1) if there were credentials.
	if ok, _ := tableExists(d, "credentials"); ok {
		var credCount int
		d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id = 1`).Scan(&credCount)
		if credCount > 0 {
			var email string
			if err := d.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email); err != nil {
				t.Errorf("legacy user not migrated to users(id=1): %v", err)
			} else if email == "" {
				t.Errorf("migrated user without email")
			}
		}
	}
	// Re-opening must be idempotent (no error).
	d.Close()
	d2, err := Open(dst)
	if err != nil {
		t.Fatalf("second open (idempotency): %v", err)
	}
	d2.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// Members from before per-member clubs keep the three clubs everyone saw; a
// fresh install starts empty (each member's home club).
func TestMigrateSeedsClubs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Turn it into a database from before the column existed.
	for _, q := range []string{
		`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`,
		`ALTER TABLE users DROP COLUMN clubs`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	d.Close()
	if d, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var clubs string
	d.QueryRow(`SELECT clubs FROM users WHERE id = 1`).Scan(&clubs)
	if clubs != "209,224,231" {
		t.Fatalf("old member's clubs %q", clubs)
	}
	d.Exec(`INSERT INTO users (id, email, pass, created) VALUES (2, 'b@x', x'00', '')`)
	d.Close()
	if d, err = Open(path); err != nil { // reopening seeds nobody else
		t.Fatal(err)
	}
	defer d.Close()
	d.QueryRow(`SELECT clubs FROM users WHERE id = 2`).Scan(&clubs)
	if clubs != "" {
		t.Fatalf("new member's clubs %q", clubs)
	}
}
