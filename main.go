package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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
	fmt.Print("Email: ", email, "\n")
	fmt.Print("Password: ", password, "\n")
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

	var currentURL string

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
		chromedp.Sleep(80*time.Second),

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

	fmt.Println("URL actual:", currentURL)
	fmt.Println("Error:", err)
}
