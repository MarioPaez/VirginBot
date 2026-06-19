package calendar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	baseURL   = "https://www.virginactive.it/calendario-corsi/JFilter"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	pageSize = 5 // clases por página que devuelve JFilter (scroll infinito)
)

// Clubes conocidos (ver notes/info.md).
const (
	ClubCorsoComo    = "4e933bca-ca21-4bec-9c68-9e5b537212e7"
	ClubPiazzaCavour = "2d9dfbe6-0ae0-4d21-8eb1-eca09fc3bc8b"
)

// Clases conocidas (extraídas del desplegable del calendario).
const (
	ClassCalisthenics      = "59149c6f-a8d2-4bfd-ab47-3c8621b1f254"
	ClassCalisthenicsPerf  = "874c6bff-4365-4d6e-93f9-0c6ab5fbba20"
	ClassCalisthenicsFloor = "9aed4c32-e451-4861-86e8-bccc55dfdbf4"
	ClassSolarium          = "65244836-c142-4b2a-98f5-c1523a6d0f1e"
)

// Class es una clase programada en el calendario.
type Class struct {
	Date      string `json:"date"`      // YYYY-MM-DD
	Start     string `json:"start"`     // HH:MM
	End       string `json:"end"`       // HH:MM
	Duration  string `json:"duration"`  // p. ej. "60 min."
	Name      string `json:"name"`      // p. ej. "Calisthenics Performance"
	Club      string `json:"club"`      // p. ej. "Milano Corso Como"
	Studio    string `json:"studio"`    // p. ej. "Studio Cycle"
	BookingID int    `json:"bookingId"` // p. ej. 374638 (para BookClass)
	Center    int    `json:"center"`    // p. ej. 209 (bookingCenter)
	Booked    bool   `json:"booked"`    // true si la sesión ya está reservada (auth)
}

// jfilterResponse es la respuesta JSON del endpoint JFilter.
type jfilterResponse struct {
	ClassCalendar string `json:"class_calendar"`
}

// maxConcurrency limita cuántos días se piden a la vez. Más de ~6 satura el
// backend remoto y sus respuestas empiezan a superar el timeout del cliente.
const maxConcurrency = 6

// maxRetries reintenta cada página ante errores transitorios (timeouts del
// backend lento bajo carga).
const maxRetries = 3

// FetchRange devuelve las clases de los clubes indicados para `days` días
// consecutivos a partir de `start` (incluido). Los días se piden en paralelo
// (con concurrencia acotada) y el resultado se devuelve en orden cronológico.
func FetchRange(client *http.Client, clubIDs, classIDs []string, start time.Time, days int) ([]Class, error) {
	type result struct {
		classes []Class
		err     error
	}
	results := make([]result, days)

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < days; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			day := start.AddDate(0, 0, i)
			classes, err := FetchDay(client, clubIDs, classIDs, day)
			if err != nil {
				results[i] = result{err: fmt.Errorf("día %s: %w", day.Format("2006-01-02"), err)}
				return
			}
			results[i] = result{classes: classes}
		}(i)
	}
	wg.Wait()

	var all []Class
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		all = append(all, r.classes...)
	}
	return all, nil
}

// FetchDay devuelve todas las clases de un único día, recorriendo las páginas
// del scroll infinito hasta agotarlas.
func FetchDay(client *http.Client, clubIDs, classIDs []string, day time.Time) ([]Class, error) {
	date := day.Format("2006-01-02")
	var classes []Class
	for page := 1; ; page++ {
		raw, err := fetchPage(client, clubIDs, classIDs, date, page)
		if err != nil {
			return nil, err
		}
		parsed := parseClasses(raw, date)
		classes = append(classes, parsed...)
		// Una página con menos de pageSize clases es la última.
		if len(parsed) < pageSize {
			break
		}
	}
	return classes, nil
}

// fetchPage pide una página del calendario, con reintentos ante fallos
// transitorios del backend lento.
func fetchPage(client *http.Client, clubIDs, classIDs []string, date string, page int) (string, error) {
	q := url.Values{}
	q.Set("club_ids", strings.Join(clubIDs, ","))
	if len(classIDs) > 0 {
		q.Set("class_ids", strings.Join(classIDs, ","))
	}
	q.Set("day_selected", date)
	q.Set("page", fmt.Sprint(page))
	u := baseURL + "?" + q.Encode()

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		html, err := doFetchPage(client, u)
		if err == nil {
			return html, nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * time.Second) // backoff lineal
	}
	return "", fmt.Errorf("tras %d intentos: %w", maxRetries, lastErr)
}

func doFetchPage(client *http.Client, u string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET JFilter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("JFilter devolvió %s", resp.Status)
	}

	var jr jfilterResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return "", fmt.Errorf("decodificar JSON: %w", err)
	}
	return jr.ClassCalendar, nil
}

var reTime = regexp.MustCompile(`\d{2}:\d{2}`)
var reDuration = regexp.MustCompile(`\d+\s*min\.?`)
var reBtnID = regexp.MustCompile(`(\d+)c(\d+)`)

// parseClasses extrae las clases de un fragmento HTML de class_calendar.
func parseClasses(htmlFragment, date string) []Class {
	doc, err := html.Parse(strings.NewReader(htmlFragment))
	if err != nil {
		return nil
	}

	var classes []Class
	for _, line := range findAll(doc, func(n *html.Node) bool { return hasClass(n, "classLine") }) {
		classes = append(classes, parseClassLine(line, date))
	}
	return classes
}

// parseClassLine extrae los campos de una única .classLine.
func parseClassLine(line *html.Node, date string) Class {
	c := Class{Date: date}

	if orario := firstWithClass(line, "calendarLessonOrario"); orario != nil {
		txt := textContent(orario)
		if times := reTime.FindAllString(txt, -1); len(times) > 0 {
			c.Start = times[0]
			if len(times) > 1 {
				c.End = times[1]
			}
		}
		c.Duration = strings.TrimSpace(reDuration.FindString(txt))
	}

	if name := firstWithClass(line, "calendaClassName"); name != nil {
		if strong := firstTag(name, "strong"); strong != nil {
			c.Name = strings.TrimSpace(textContent(strong))
		}
	}

	if club := firstWithClass(line, "calendarLessonClub"); club != nil {
		if studio := firstWithClass(club, "fw300"); studio != nil {
			c.Studio = strings.TrimSpace(textContent(studio))
		}
		full := strings.TrimSpace(textContent(club))
		c.Club = strings.TrimSpace(strings.TrimSuffix(full, c.Studio))
	}

	if btn := firstWithClass(line, "calendarButton"); btn != nil {
		// El botón (o enlace) lleva id "<bookingId>c<center>", presente con o
		// sin sesión. Con sesión, la clase "btn-unbook" indica ya reservada.
		el := firstTag(btn, "button")
		if el == nil {
			el = firstTag(btn, "a")
		}
		if el != nil {
			if m := reBtnID.FindStringSubmatch(attrVal(el, "id")); m != nil {
				c.BookingID, _ = strconv.Atoi(m[1])
				c.Center, _ = strconv.Atoi(m[2])
			}
			c.Booked = strings.Contains(attrVal(el, "class"), "btn-unbook")
		}
	}

	return c
}
