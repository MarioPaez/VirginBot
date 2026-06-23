#!/usr/bin/env bash
# Levanta VirginBot en local con un solo comando: ./run.sh
# Carga el .env automáticamente (lo hace la propia app) y abre el navegador.
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -f .env ]; then
  echo "⚠️  Falta .env (necesita APP_SECRET, SMTP_*, VA_ALLOWED_EMAILS). Aborto."
  exit 1
fi

ADDR="${ADDR:-:8080}"
URL="http://localhost${ADDR}"

echo "▶  Arrancando VirginBot en ${URL}"

# Abre el navegador cuando el servidor esté escuchando (en 2º plano).
(
  for _ in $(seq 1 40); do
    if curl -fsS -o /dev/null "${URL}/healthz" 2>/dev/null; then
      command -v open >/dev/null && open "${URL}"      # macOS
      break
    fi
    sleep 0.5
  done
) &

ADDR="${ADDR}" go run .
