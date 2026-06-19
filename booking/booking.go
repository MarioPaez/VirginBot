package booking

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

const (
	base      = "https://www.virginactive.it/VirginIntegrations/IntegrationPlatform"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// outcome es el resultado común de BookClass/UnbookClass.
type outcome struct {
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

// Book reserva una clase. Requiere un cliente autenticado en www (session.LoginWWW).
func Book(client *http.Client, bookingID, center int) error {
	return do(client, "BookClass", "BookClassResult", bookingID, center)
}

// Unbook cancela una reserva.
func Unbook(client *http.Client, bookingID, center int) error {
	return do(client, "UnbookClass", "UnbookClassResult", bookingID, center)
}

// do ejecuta Book/Unbook y traduce el resultado a error si no es 200.
func do(client *http.Client, action, resultKey string, bookingID, center int) error {
	q := url.Values{}
	q.Set("bookingId", fmt.Sprint(bookingID))
	q.Set("bookingCenter", fmt.Sprint(center))
	u := fmt.Sprintf("%s/%s?%s", base, action, q.Encode())

	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var wrapper map[string]outcome
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return fmt.Errorf("%s: respuesta no reconocida: %s", action, truncate(body, 200))
	}
	res := wrapper[resultKey]
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s rechazado (%d): %s", action, res.StatusCode, res.StatusMessage)
	}
	return nil
}

// MyBookings devuelve las clases ya reservadas en la ventana de `days` días,
// consultando el calendario con la sesión autenticada.
func MyBookings(client *http.Client, clubIDs, classIDs []string, days int) ([]calendar.Class, error) {
	all, err := calendar.FetchRange(client, clubIDs, classIDs, time.Now(), days)
	if err != nil {
		return nil, err
	}
	var booked []calendar.Class
	for _, c := range all {
		if c.Booked {
			booked = append(booked, c)
		}
	}
	return booked, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
