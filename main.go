package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/booking"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/server"
)

const automationsFile = "automations.json"

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	auth := server.NewAuth(os.Getenv("VA_EMAIL"), os.Getenv("VA_PASS"))

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

	// Motor de automatización: usa el calendario autenticado del servidor y
	// reserva con el cliente autenticado.
	engine := automation.NewEngine(store, srv.Classes, func(bookingID, center int) error {
		client, err := auth.Client()
		if err != nil {
			return err
		}
		return booking.Book(client, bookingID, center)
	})
	go engine.Run(make(chan struct{}))

	log.Printf("API escuchando en http://localhost%s", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
