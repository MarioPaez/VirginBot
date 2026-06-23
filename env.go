package main

import (
	"bufio"
	"os"
	"strings"
)

// loadDotEnv carga variables de un fichero .env al entorno del proceso, sin
// pisar las que ya estén definidas (el entorno real tiene prioridad). No falla
// si el fichero no existe: en producción las variables se inyectan de otra forma.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Quita comillas envolventes ('...' o "...").
		if len(val) >= 2 {
			if (val[0] == '\'' && val[len(val)-1] == '\'') ||
				(val[0] == '"' && val[len(val)-1] == '"') {
				val = val[1 : len(val)-1]
			}
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // el entorno real manda
		}
		os.Setenv(key, val)
	}
}
