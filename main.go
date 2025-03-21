package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type BookClassResponse struct {
	BookClassResult struct {
		Data          string `json:"Data"`
		ErrorMessage  string `json:"ErrorMessage"`
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	} `json:"BookClassResult"`
}

func main() {

	// Obtener credenciales
	email := os.Getenv("VA_EMAIL")
	password := os.Getenv("VA_PASS")
	loginUrl := "https://shop.virginactive.it/account/login"
	// subscriptionsUrl := "https://shop.virginactive.it/account/subscriptions"

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),       // false = mostrar navegador
		chromedp.Flag("start-maximized", true), // opcional: maximizar ventana
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	err := chromedp.Run(ctx,
		// Navegar a la página de login
		chromedp.Navigate(loginUrl),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Navegando a la página de login")
			return nil
		}),
		// Esperar y aceptar el diálogo de cookies
		chromedp.WaitVisible(`button.iubenda-cs-accept-btn.iubenda-cs-btn-primary[role="button"]`),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Dialog de cookies encontrado")
			// Debug: verificar si el botón existe
			var exists bool
			if err := chromedp.Evaluate(`!!document.querySelector('button.iubenda-cs-accept-btn.iubenda-cs-btn-primary')`, &exists).Do(ctx); err != nil {
				return err
			}
			fmt.Printf("¿Existe el botón de aceptar? %v\n", exists)
			return nil
		}),
		chromedp.Click(`button.iubenda-cs-accept-btn.iubenda-cs-btn-primary`),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Cookies aceptadas")
			return nil
		}),

		// Verificar que el diálogo se ha cerrado
		chromedp.WaitNotPresent(`button.iubenda-cs-accept-btn.iubenda-cs-btn-primary`),

		// Esperar a que los campos estén disponibles
		chromedp.WaitVisible(`input[name="username"]`),
		chromedp.WaitVisible(`input[name="password"]`),
		// Rellenar el formulario
		chromedp.SendKeys(`input[name="username"]`, email),
		chromedp.SendKeys(`input[name="password"]`, password),
		// Hacer clic en el botón de login
		chromedp.Click(`button[name="login"]`),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Click en botón login realizado")
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Esperando al botón de calendario...")
			return nil
		}),
		// Esperar a que el botón del calendario esté visible
		chromedp.WaitVisible(`a.subscription-go-to-courses.btn.btn-primary`),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Botón de calendario encontrado")
			return nil
		}),
		// Intentar hacer click usando JavaScript
		chromedp.Evaluate(`
			const btn = document.querySelector('a.subscription-go-to-courses.btn.btn-primary');
			if (btn) {
				btn.click();
				console.log('Click ejecutado via JavaScript');
			} else {
				console.log('Botón no encontrado');
			}
		`, nil),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Println("✅ Intento de click realizado")
			return nil
		}),
		// Esperar un momento para ver si la navegación comienza
		chromedp.Sleep(10*time.Second),

		// Verificar si hay mensajes de error
		chromedp.ActionFunc(func(ctx context.Context) error {
			var errorVisible bool
			if err := chromedp.Evaluate(`!!document.querySelector('.error-message, .alert-danger')`, &errorVisible).Do(ctx); err != nil {
				return err
			}
			if errorVisible {
				var errorText string
				if err := chromedp.Text(`.error-message, .alert-danger`, &errorText).Do(ctx); err != nil {
					return err
				}
				fmt.Printf("❌ Error de login detectado: %s\n", errorText)
			}
			return nil
		}),

		// Esperar y monitorear la redirección
		chromedp.ActionFunc(func(ctx context.Context) error {
			for i := 0; i < 50; i++ { // 15 intentos, 1 segundo cada uno
				var url string
				if err := chromedp.Location(&url).Do(ctx); err != nil {
					return err
				}
				fmt.Printf("📍 URL actual (%d): %s\n", i+1, url)

				if strings.Contains(url, "/account/subscriptions") {
					fmt.Println("✅ Redirección exitosa a subscriptions")
					return nil
				}
				time.Sleep(1 * time.Second)
			}
			return fmt.Errorf("❌ Timeout esperando redirección")
		}),
	)

	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	var cookies []*network.Cookie
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			cookies, err = network.GetCookies().Do(ctx)
			if err != nil {
				return err
			}
			fmt.Println("🍪 Cookies obtenidas:")
			for _, cookie := range cookies {
				fmt.Printf("- %s: %s\n", cookie.Name, cookie.Value)
			}
			return nil
		}),
	)

	if err != nil {
		fmt.Println("Error al obtener cookies:", err)
		return
	}

	// Create HTTP client with cookie jar
	jar, err := cookiejar.New(nil)
	if err != nil {
		fmt.Println("Error creando cookie jar:", err)
		return
	}

	client := &http.Client{
		Jar: jar,
	}

	// Convert chromedp cookies to http.Cookie and add them to jar
	calendarURL, _ := url.Parse("https://www.virginactive.it/calendario-corsi")
	var httpCookies []*http.Cookie
	for _, cookie := range cookies {
		httpCookies = append(httpCookies, &http.Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Path:     cookie.Path,
			Domain:   cookie.Domain,
			Expires:  time.Unix(int64(cookie.Expires), 0),
			Secure:   cookie.Secure,
			HttpOnly: cookie.HTTPOnly,
		})
	}
	jar.SetCookies(calendarURL, httpCookies)
	// Now you can use the client to make requests with the cookies
	// Example request:
	req, err := http.NewRequest("GET", "https://www.virginactive.it/VirginIntegrations/IntegrationPlatform/BookClass?bookingId=252000&bookingCenter=202", nil)
	if err != nil {
		fmt.Println("Error creando request:", err)
		return
	}

	// Set headers
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Host", "www.virginactive.it")
	req.Header.Set("Referer", "https://www.virginactive.it/calendario-corsi?day_selected=2025-03-26")
	req.Header.Set("sec-ch-ua", `"Chromium";v="134", "Not:A-Brand";v="24", "Google Chrome";v="134"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", "Windows")
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	// Print all request headers before making the request
	fmt.Println("\n🔍 Request Headers:")
	for name, values := range req.Header {
		for _, value := range values {
			fmt.Printf("%s: %s\n", name, value)
		}
	}

	// Print cookies that will be sent
	fmt.Println("\n🍪 Cookies que se enviarán:")
	for _, cookie := range jar.Cookies(req.URL) {
		fmt.Printf("- %s: %s\n", cookie.Name, cookie.Value)
	}

	// Make the request
	fmt.Println("\n📡 Enviando request...")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error haciendo request:", err)
		return
	}
	defer resp.Body.Close()

	// Leer y decodificar la respuesta JSON
	var bookResponse BookClassResponse
	if err := json.NewDecoder(resp.Body).Decode(&bookResponse); err != nil {
		fmt.Println("Error decodificando respuesta:", err)
		return
	}

	// Imprimir la respuesta formateada
	jsonBytes, err := json.MarshalIndent(bookResponse, "", "    ")
	if err != nil {
		fmt.Println("Error formateando respuesta:", err)
		return
	}
	fmt.Printf("\n📄 Respuesta:\n%s\n", string(jsonBytes))

	fmt.Printf("📡 Response Status: %s\n", resp.Status)

}
