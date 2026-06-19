package session

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	URL_LOGIN    = "https://shop.virginactive.it/account/login"
	URL_PROFILE  = "https://shop.virginactive.it/account/profile"
	URL_CALENDAR = "https://www.virginactive.it/calendario-corsi"

	envUser = "VA_EMAIL"
	envPass = "VA_PASS"
)

// IDs conocidos del catálogo (ver notes/info.md).
var (
	clubCorsoComo          = "4e933bca-ca21-4bec-9c68-9e5b537212e7"
	classCalisthenicsPerf  = "874c6bff-4365-4d6e-93f9-0c6ab5fbba20"
	classCalisthenicsBasic = "59149c6f-a8d2-4bfd-ab47-3c8621b1f254"
)

// DoLogin autentica vía HTTP (sin navegador) contra el storefront de Shopware
// y deja preparada la URL del calendario filtrada para la futura reserva.
func DoLogin() {
	client, err := HTTPLogin(os.Getenv(envUser), os.Getenv(envPass))
	if err != nil {
		log.Fatalf("login HTTP fallido: %v", err)
	}
	fmt.Println("sesión lista; cliente HTTP autenticado y verificado")

	calURL := calendarURL(
		[]string{clubCorsoComo},
		[]string{classCalisthenicsPerf, classCalisthenicsBasic},
		time.Now(),
	)
	log.Printf("calendario objetivo: %s", calURL)
	_ = client // se usará para listar clases y reservar
}

// calendarURL construye la URL del calendario ya filtrada por clubes, clases y
// día, evitando tener que interactuar con los desplegables de la web.
func calendarURL(clubIDs, classIDs []string, day time.Time) string {
	q := url.Values{}
	q.Set("club_ids", strings.Join(clubIDs, ","))
	q.Set("class_ids", strings.Join(classIDs, ","))
	q.Set("day_selected", day.Format("2006-01-02"))
	return URL_CALENDAR + "?" + q.Encode()
}
