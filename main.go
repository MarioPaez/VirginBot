package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	_ "time/tzdata" // embebe la base de zonas horarias (Europe/Rome) en el binario

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/db"
	"github.com/MarioPaez/VirginBot/notification"
	"github.com/MarioPaez/VirginBot/server"
)

const defaultDBFile = "virginbot.db"

func main() {
	loadDotEnv(".env") // carga el .env si existe (no pisa variables ya exportadas)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	dbPath := os.Getenv("VIRGINBOT_DB") // ruta de la BD (volumen persistente en el hosting)
	if dbPath == "" {
		dbPath = defaultDBFile
	}
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("abrir base de datos: %v", err)
	}
	defer database.Close()

	secret := os.Getenv("APP_SECRET")
	if secret == "" {
		log.Fatal("APP_SECRET no definido: es obligatorio (cifra las contraseñas de los usuarios). " +
			"Genera uno con `openssl rand -hex 32` y ponlo en el entorno (.env en local, secret en el hosting).")
	}
	acc, err := account.NewStore(database, secret)
	if err != nil {
		log.Fatalf("almacén de cuentas: %v", err)
	}
	// Compatibilidad: si no hay usuarios pero sí credenciales en el entorno, las
	// sembramos como primer usuario (así arranca sin pasar por el login del FE).
	if len(acc.ListUserIDs()) == 0 {
		if e, p := os.Getenv("VA_EMAIL"), os.Getenv("VA_PASS"); e != "" && p != "" {
			if _, err := acc.Upsert(e, p); err != nil {
				log.Printf("aviso: no se pudo sembrar el usuario inicial: %v", err)
			}
		}
	}
	apiKey := os.Getenv("VA_API_KEY")
	if apiKey == "" {
		log.Fatal("VA_API_KEY no definido: es la clave estática de la app móvil de Virgin (header x-api-key). " +
			"Ponla en el entorno (.env en local, secret en el hosting).")
	}
	auth := server.NewAuth(acc, apiKey)

	if al := strings.TrimSpace(os.Getenv("VA_ALLOWED_EMAILS")); al != "" {
		log.Printf("acceso restringido: %d email(s) en la allowlist", len(strings.Split(al, ",")))
	} else {
		log.Println("acceso ABIERTO: cualquier cuenta Virgin válida puede registrarse (define VA_ALLOWED_EMAILS para restringir)")
	}

	clubs := []int{calendar.ClubCorsoComo, calendar.ClubCavour}
	// Clases que mostramos: calistenia, pilates (reformer), full body, TRX y cycle
	// en ambos clubes; solarium solo en Corso Como.
	keep := func(c calendar.Class) bool {
		name := strings.ToLower(c.Name)
		switch {
		case strings.Contains(name, "calisthenics"),
			strings.Contains(name, "reformer"),
			strings.Contains(name, "pilates"),
			strings.Contains(name, "full body"),
			strings.Contains(name, "trx"),
			strings.Contains(name, "cycle"):
			return true
		case strings.Contains(name, "solarium"):
			return strings.Contains(c.Club, "Corso Como")
		default:
			return false
		}
	}

	store := automation.NewStore(database)

	sender := notification.FromEnv()
	srv := server.New(database, auth, acc, clubs, keep, store, sender)

	// notifyUser dirige cada aviso al email del usuario correspondiente.
	notifyUser := func(userID int64, subject, body string) {
		if err := sender.Send(auth.Email(userID), subject, body); err != nil {
			log.Printf("aviso email fallido (u%d): %v", userID, err)
		}
	}

	// Motor de automatización: sondea con timing preciso (fetch fresco por día y
	// usuario), reserva con el cliente autenticado de cada usuario y avisa por email.
	engine := automation.NewEngine(store, srv.FreshDayFor, func(userID int64, clubID, classID, sessionID int, date string) error {
		client, err := auth.ClientFor(userID)
		if err != nil {
			return err
		}
		if err := client.Book(clubID, classID, sessionID, date); err != nil {
			return err
		}
		srv.InvalidateUser(userID) // refresca reservas (reconcilia la tabla bookings) y calendario
		return nil
	}, notifyUser)
	go engine.Run(make(chan struct{}))

	log.Printf("API escuchando en http://localhost%s", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
