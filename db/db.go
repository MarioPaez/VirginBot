// Package db abre la base de datos SQLite y crea el esquema. No conoce tipos de
// dominio: cada almacén (account, automation, server) hace su propio SQL sobre
// el *sql.DB compartido.
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// schema es el esquema actual (multi-usuario). En instalaciones nuevas crea todo
// directamente; en bases existentes, las tablas legadas se ajustan en migrate().
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

// Open abre (o crea) la base de datos en `path` y aplica el esquema/migración.
func Open(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite: serializamos el acceso (1 conexión) para evitar "database is
	// locked"; el volumen de tráfico es bajo.
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
		return nil, fmt.Errorf("migrar esquema: %w", err)
	}
	return d, nil
}

// migrate lleva una base mono-usuario al esquema multi-usuario de forma
// idempotente. En instalaciones nuevas, las tablas se crean ya en su forma final
// y los pasos de migración no hacen nada.
func migrate(d *sql.DB) error {
	// 1) Tablas que necesitan cambio de PK y NO se pueden alterar in situ.
	//    Se resuelven ANTES de crear el esquema nuevo.
	if err := rebuildAutomations(d); err != nil {
		return err
	}
	if err := dropLegacyBookings(d); err != nil {
		return err
	}
	// La caché de calendario en BD ya no se usa (el calendario se sirve en vivo
	// desde vapi); se elimina si existía de versiones anteriores.
	if _, err := d.Exec(`DROP TABLE IF EXISTS day_cache`); err != nil {
		return err
	}

	// 2) Crear/asegurar todas las tablas en su forma final.
	if _, err := d.Exec(schema); err != nil {
		return fmt.Errorf("crear esquema: %w", err)
	}

	// 3) Columnas que sí se pueden añadir in situ a tablas legadas.
	if err := ensureColumn(d, "sessions", "user_id", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	// Migración a vapi: ids necesarios para reservar/cancelar vía la API móvil.
	for _, col := range []string{"club_id", "class_id", "session_id"} {
		if err := ensureColumn(d, "bookings", col, "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if err := ensureColumn(d, "bookings", "instructor", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// i18n: idioma preferido del usuario (vacío = aún sin detectar).
	if err := ensureColumn(d, "users", "lang", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// 4) Migrar el único usuario legado (credentials.id=1) a la tabla users.
	if err := migrateLegacyUser(d); err != nil {
		return err
	}
	return nil
}

// rebuildAutomations reconstruye la tabla automations con PK (user_id, id) si
// aún tiene la forma antigua (sin user_id), conservando las reglas existentes
// como del usuario 1.
func rebuildAutomations(d *sql.DB) error {
	exists, err := tableExists(d, "automations")
	if err != nil {
		return err
	}
	if !exists {
		return nil // instalación nueva: el esquema la creará con la forma final
	}
	has, err := hasColumn(d, "automations", "user_id")
	if err != nil {
		return err
	}
	if has {
		return nil // ya está en la forma nueva
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

// dropLegacyBookings borra la tabla bookings si tiene la forma antigua (columna
// `center`, sin los ids de vapi). Es caché: se repuebla desde la API móvil.
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
		return nil // ya está en la forma nueva
	}
	_, err = d.Exec(`DROP TABLE bookings`)
	return err
}

// migrateLegacyUser copia la fila única de la tabla legada credentials (id=1) a
// users, si existe y users está vacía.
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

// ---- helpers de introspección ----

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
