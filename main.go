package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/booking"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/notification"
	"github.com/MarioPaez/VirginBot/server"
)

const (
	automationsFile = "automations.json"
	credentialsFile = "credentials.enc"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	secret := os.Getenv("APP_SECRET")
	if secret == "" {
		secret = "virginbot-default-secret"
		log.Println("aviso: APP_SECRET no definido; usando clave por defecto (menos seguro)")
	}
	acc, err := account.NewStore(credentialsFile, secret)
	if err != nil {
		log.Fatalf("almacén de credenciales: %v", err)
	}
	// Compatibilidad: si no hay credenciales guardadas pero sí en el entorno,
	// las sembramos (así sigue funcionando sin pasar por el login del FE).
	if _, _, ok := acc.Get(); !ok {
		if e, p := os.Getenv("VA_EMAIL"), os.Getenv("VA_PASS"); e != "" && p != "" {
			acc.Set(e, p)
		}
	}
	auth := server.NewAuth(acc)

	clubs := []string{calendar.ClubCorsoComo, calendar.ClubPiazzaCavour}
	classIDs := []string{
		calendar.ClassCalisthenics,
		calendar.ClassCalisthenicsPerf,
		calendar.ClassCalisthenicsFloor,
		calendar.ClassSolarium,
	}
	// Calistenia (ambos clubes) + Solarium solo en Corso Como.
	keep := func(c calendar.Class) bool {
		name := strings.ToLower(c.Name)
		if strings.Contains(name, "calisthenics") {
			return true
		}
		return strings.Contains(name, "solarium") && strings.Contains(c.Club, "Corso Como")
	}

	store, err := automation.NewStore(automationsFile)
	if err != nil {
		log.Fatalf("no se pudieron cargar las automatizaciones: %v", err)
	}

	srv := server.New(auth, clubs, classIDs, keep, store)

	// Motor de automatización: sondea con timing preciso (fetch fresco por día),
	// reserva con el cliente autenticado y avisa por email del resultado.
	engine := automation.NewEngine(store, srv.FreshDay, func(bookingID, center int) error {
		client, err := auth.Client()
		if err != nil {
			return err
		}
		if err := booking.Book(client, bookingID, center); err != nil {
			return err
		}
		srv.Invalidate() // refresca el estado de reserva en el FE
		return nil
	}, notification.FromEnv())
	go engine.Run(make(chan struct{}))

	log.Printf("API escuchando en http://localhost%s", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
