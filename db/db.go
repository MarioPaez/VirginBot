// Package db opens the SQLite database and creates the schema. It knows no domain
// types: each store (account, automation, server) runs its own SQL on the shared
// *sql.DB.
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// schema is the current (multi-user) schema. On fresh installs it creates
// everything directly; on existing databases, legacy tables are adjusted in
// migrate().
const schema = `
CREATE TABLE IF NOT EXISTS users (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    email   TEXT NOT NULL UNIQUE,
    pass    BLOB NOT NULL,
    created TEXT NOT NULL,
    lang    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS automations (
    user_id           INTEGER NOT NULL,
    id                TEXT NOT NULL,
    name              TEXT NOT NULL,
    club              TEXT NOT NULL,
    weekday           INTEGER NOT NULL,
    start             TEXT NOT NULL,
    opens_days_before INTEGER NOT NULL,
    enabled           INTEGER NOT NULL,
    created           TEXT NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE TABLE IF NOT EXISTS sessions (
    token   TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    created TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bookings (
    user_id    INTEGER NOT NULL,
    name       TEXT NOT NULL,
    club       TEXT NOT NULL,
    date       TEXT NOT NULL,
    start      TEXT NOT NULL,
    end_time   TEXT NOT NULL DEFAULT '',
    instructor TEXT NOT NULL DEFAULT '',
    club_id    INTEGER NOT NULL DEFAULT 0,
    class_id   INTEGER NOT NULL DEFAULT 0,
    session_id INTEGER NOT NULL DEFAULT 0,
    booking_id INTEGER NOT NULL DEFAULT 0,
    created    TEXT NOT NULL,
    PRIMARY KEY (user_id, name, club, date, start)
);
`

// Open opens (or creates) the database at `path` and applies the schema/migration.
func Open(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite: serialize access (1 connection) to avoid "database is locked"; the
	// traffic volume is low.
	d.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := d.Exec(pragma); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	if err := migrate(d); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return d, nil
}

// migrate brings a single-user database to the multi-user schema idempotently. On
// fresh installs the tables are created in their final form and the migration
// steps do nothing.
func migrate(d *sql.DB) error {
	// 1) Tables that need a PK change and can't be altered in place. Resolved
	//    BEFORE creating the new schema.
	if err := rebuildAutomations(d); err != nil {
		return err
	}
	if err := dropLegacyBookings(d); err != nil {
		return err
	}
	// The DB calendar cache is no longer used (the calendar is served live from
	// vapi); drop it if it existed from older versions.
	if _, err := d.Exec(`DROP TABLE IF EXISTS day_cache`); err != nil {
		return err
	}

	// 2) Create/ensure all tables in their final form.
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// 3) Columns that can be added in place to legacy tables.
	if err := ensureColumn(d, "sessions", "user_id", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	// vapi migration: ids needed to book/cancel via the mobile API.
	for _, col := range []string{"club_id", "class_id", "session_id"} {
		if err := ensureColumn(d, "bookings", col, "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if err := ensureColumn(d, "bookings", "instructor", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// i18n: user's preferred language (empty = not detected yet).
	if err := ensureColumn(d, "users", "lang", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// 4) Migrate the single legacy user (credentials.id=1) into the users table.
	if err := migrateLegacyUser(d); err != nil {
		return err
	}
	return nil
}

// rebuildAutomations rebuilds the automations table with PK (user_id, id) if it
// still has the old shape (no user_id), keeping existing rules as user 1's.
func rebuildAutomations(d *sql.DB) error {
	exists, err := tableExists(d, "automations")
	if err != nil {
		return err
	}
	if !exists {
		return nil // fresh install: the schema will create it in its final form
	}
	has, err := hasColumn(d, "automations", "user_id")
	if err != nil {
		return err
	}
	if has {
		return nil // already in the new shape
	}
	stmts := []string{
		`CREATE TABLE automations_new (
			user_id INTEGER NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL,
			club TEXT NOT NULL, weekday INTEGER NOT NULL, start TEXT NOT NULL,
			opens_days_before INTEGER NOT NULL, enabled INTEGER NOT NULL,
			created TEXT NOT NULL, PRIMARY KEY (user_id, id)
		)`,
		`INSERT INTO automations_new
			SELECT 1, id, name, club, weekday, start, opens_days_before, enabled, created
			FROM automations`,
		`DROP TABLE automations`,
		`ALTER TABLE automations_new RENAME TO automations`,
	}
	return execAll(d, stmts)
}

// dropLegacyBookings drops the bookings table if it has the old shape (a `center`
// column, without the vapi ids). It's a cache: repopulated from the mobile API.
func dropLegacyBookings(d *sql.DB) error {
	exists, err := tableExists(d, "bookings")
	if err != nil || !exists {
		return err
	}
	has, err := hasColumn(d, "bookings", "center")
	if err != nil {
		return err
	}
	if !has {
		return nil // already in the new shape
	}
	_, err = d.Exec(`DROP TABLE bookings`)
	return err
}

// migrateLegacyUser copies the single row of the legacy credentials table (id=1)
// into users, if it exists and users is empty.
func migrateLegacyUser(d *sql.DB) error {
	exists, err := tableExists(d, "credentials")
	if err != nil || !exists {
		return err
	}
	var users int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if users > 0 {
		return nil
	}
	var email string
	var pass []byte
	err = d.QueryRow(`SELECT email, pass FROM credentials WHERE id = 1`).Scan(&email, &pass)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = d.Exec(
		`INSERT INTO users (id, email, pass, created) VALUES (1, ?, ?, datetime('now'))`,
		email, pass)
	return err
}

// ---- introspection helpers ----

func tableExists(d *sql.DB, name string) (bool, error) {
	var n int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

func hasColumn(d *sql.DB, table, column string) (bool, error) {
	rows, err := d.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dfltVal sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func ensureColumn(d *sql.DB, table, column, decl string) error {
	has, err := hasColumn(d, table, column)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = d.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

func execAll(d *sql.DB, stmts []string) error {
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}
