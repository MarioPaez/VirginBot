package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"io"
	"net/url"
	"regexp"
	"os"
	"strings"
)

func main() {
	// Crear un cliente HTTP con almacenamiento de cookies
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// URL de la página de login
	loginPageURL := "https://shop.virginactive.it/account/login"
	fmt.Println("URL de la página de login:", loginPageURL)
	// 1️⃣ Obtener el _csrf_token desde la página de login
	resp, err := client.Get(loginPageURL)
	if err != nil {
		fmt.Println("Error al obtener la página de login:", err)
		return
	}
	defer resp.Body.Close()
	
	// Leer el contenido de la respuesta
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error al leer la respuesta:", err)
		return
	}
	// Buscar el token CSRF en el HTML usando una expresión regular
	csrfRegex := regexp.MustCompile(`<input type="hidden" name="_csrf_token" value="(.*?)">`)
	matches := csrfRegex.FindStringSubmatch(string(body))
	if len(matches) < 2 {
		fmt.Println("No se encontró el token CSRF")
		return
	}
	csrfToken := matches[1]
	fmt.Println("CSRF Token encontrado:", csrfToken)

	// 2️⃣ Enviar la petición POST con el login
	loginURL := "https://shop.virginactive.it/account/login"
	// Acceder a las variables
	email := os.Getenv("VA_EMAIL")
	password := os.Getenv("VA_PASS")

	data := url.Values{
		"_csrf_token": {csrfToken},
		"username":    {email},
		"password":    {password},
		"login":       {"Accedo"}, // Esto puede ser opcional, pero algunas webs lo requieren
	}

	req, _ := http.NewRequest("POST", loginURL, strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", loginPageURL) // Algunas webs requieren este header

	resp, err = client.Do(req)
	if err != nil {
		fmt.Println("Error al enviar login:", err)
		return
	}
	defer resp.Body.Close()

	// 3️⃣ Verificar si se guardaron cookies
	fmt.Println("Cookies guardadas tras el login:")
	for _, cookie := range jar.Cookies(req.URL) {
		fmt.Println(cookie.Name, ":", cookie.Value)
	}
}
