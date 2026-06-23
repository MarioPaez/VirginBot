package db

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateRealDB valida la migración mono→multi-usuario sobre una COPIA de la
// base de datos real del proyecto (../virginbot.db), sin tocar la original. Si no
// existe, se omite (p. ej. en CI con BD limpia).
func TestMigrateRealDB(t *testing.T) {
	src := filepath.Join("..", "virginbot.db")
	if _, err := os.Stat(src); err != nil {
		t.Skip("no hay virginbot.db; se omite el test de migración real")
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	// Copiamos los tres ficheros (db + WAL + SHM) para preservar datos no
	// volcados aún al fichero principal.
	for _, suf := range []string{"", "-wal", "-shm"} {
		if err := copyFile(src+suf, dst+suf); err != nil && suf == "" {
			t.Fatalf("copiar %s: %v", src+suf, err)
		}
	}

	d, err := Open(dst)
	if err != nil {
		t.Fatalf("Open (migración): %v", err)
	}
	defer d.Close()

	// Tablas nuevas presentes.
	for _, tbl := range []string{"users", "bookings", "automations", "sessions", "day_cache"} {
		if ok, _ := tableExists(d, tbl); !ok {
			t.Errorf("falta la tabla %q tras migrar", tbl)
		}
	}
	// Columnas user_id añadidas.
	for _, tc := range []struct{ table, col string }{
		{"automations", "user_id"}, {"sessions", "user_id"}, {"day_cache", "user_id"},
	} {
		if ok, _ := hasColumn(d, tc.table, tc.col); !ok {
			t.Errorf("%s no tiene la columna %s", tc.table, tc.col)
		}
	}
	// El usuario legado debe haberse migrado (id=1) si había credenciales.
	if ok, _ := tableExists(d, "credentials"); ok {
		var credCount int
		d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id = 1`).Scan(&credCount)
		if credCount > 0 {
			var email string
			if err := d.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email); err != nil {
				t.Errorf("usuario legado no migrado a users(id=1): %v", err)
			} else if email == "" {
				t.Errorf("usuario migrado sin email")
			}
		}
	}
	// Re-abrir debe ser idempotente (sin error).
	d.Close()
	d2, err := Open(dst)
	if err != nil {
		t.Fatalf("segunda apertura (idempotencia): %v", err)
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
