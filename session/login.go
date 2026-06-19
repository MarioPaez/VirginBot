package session

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

const (
	urlSubscriptions = "https://shop.virginactive.it/account/subscriptions"
	urlWWWStatus     = "https://www.virginactive.it/rest-api/login-status"
)

// HTTPLogin autentica contra el storefront de Shopware sin navegador: descarga
// el formulario, extrae su token CSRF y envía las credenciales. Devuelve un
// *http.Client con la cookie de sesión ya en su cookiejar, listo para reservar.
func HTTPLogin(user, pass string) (*http.Client, error) {
	if user == "" || pass == "" {
		return nil, fmt.Errorf("faltan las variables de entorno %s / %s", envUser, envPass)
	}

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("crear cookie jar: %w", err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	token, err := fetchLoginCSRFToken(client)
	if err != nil {
		return nil, err
	}

	if err := submitLogin(client, user, pass, token); err != nil {
		return nil, err
	}
	return client, nil
}

// LoginWWW devuelve un cliente autenticado contra www.virginactive.it (donde se
// reservan las clases). Encadena: login en el shop → canje del JWT de SSO en
// /loginbytokenglobal → cookie .AspNet.Cookies de Sitefinity. Todo por HTTP.
func LoginWWW(user, pass string) (*http.Client, error) {
	client, err := HTTPLogin(user, pass)
	if err != nil {
		return nil, err
	}
	if err := exchangeToWWW(client); err != nil {
		return nil, err
	}
	return client, nil
}

// exchangeToWWW obtiene del shop el enlace SSO (loginbytokenglobal) y lo sigue
// para autenticar www en el mismo cookiejar.
func exchangeToWWW(client *http.Client) error {
	ssoURL, err := fetchSSOLink(client)
	if err != nil {
		return err
	}

	req, _ := http.NewRequest(http.MethodGet, ssoURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("canjear token SSO: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	return verifyWWWAuth(client)
}

// fetchSSOLink descarga la página de suscripciones del shop y extrae el href
// del enlace "Calendario corsi" (loginbytokenglobal con el JWT).
func fetchSSOLink(client *http.Client) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, urlSubscriptions, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET subscriptions: %w", err)
	}
	defer resp.Body.Close()

	href, err := findSSOHref(resp.Body)
	if err != nil {
		return "", err
	}
	if href == "" {
		return "", fmt.Errorf("no se encontró el enlace SSO (loginbytokenglobal); ¿login del shop correcto?")
	}
	return html.UnescapeString(href), nil
}

// verifyWWWAuth confirma que www reconoce la sesión.
func verifyWWWAuth(client *http.Client) error {
	req, _ := http.NewRequest(http.MethodGet, urlWWWStatus, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET login-status: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"IsLoggedIn":true`) {
		return fmt.Errorf("www no autenticado tras el canje SSO")
	}
	return nil
}

// findSSOHref localiza el <a class="subscription-go-to-courses"> y devuelve su href.
func findSSOHref(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", fmt.Errorf("parsear subscriptions: %w", err)
	}
	var href string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if href != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			class, link := "", ""
			for _, a := range n.Attr {
				if a.Key == "class" {
					class = a.Val
				}
				if a.Key == "href" {
					link = a.Val
				}
			}
			if strings.Contains(class, "subscription-go-to-courses") {
				href = link
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return href, nil
}

// fetchLoginCSRFToken hace GET a la página de login (poblando el jar con la
// cookie de sesión inicial) y devuelve el _csrf_token del formulario de login.
func fetchLoginCSRFToken(client *http.Client) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, URL_LOGIN, nil)
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET login: %w", err)
	}
	defer resp.Body.Close()

	token, err := extractLoginToken(resp.Body)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("no se encontró _csrf_token en el formulario de login")
	}
	return token, nil
}

// submitLogin envía las credenciales y verifica que el login fue correcto
// mirando a dónde redirige el storefront tras el POST.
func submitLogin(client *http.Client, user, pass, token string) error {
	form := url.Values{
		"username":    {user},
		"password":    {pass},
		"_csrf_token": {token},
	}
	req, _ := http.NewRequest(http.MethodPost, URL_LOGIN, strings.NewReader(form.Encode()))
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", URL_LOGIN)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST login: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// La única señal fiable de éxito es poder entrar a un área que solo ven los
	// usuarios autenticados: un invitado (o un login rechazado) es redirigido a
	// /account/login al pedir /account/profile.
	return verifyAuthenticated(client)
}

// verifyAuthenticated comprueba que la sesión está autenticada pidiendo el
// perfil: si el storefront nos rebota a /account/login, no hay sesión.
func verifyAuthenticated(client *http.Client) error {
	req, _ := http.NewRequest(http.MethodGet, URL_PROFILE, nil)
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET profile: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if strings.Contains(resp.Request.URL.Path, "/account/login") {
		return fmt.Errorf("login rechazado: la sesión no está autenticada (credenciales o CSRF)")
	}
	return nil
}

// extractLoginToken localiza el <form action="/account/login"> y devuelve el
// value de su <input name="_csrf_token">.
func extractLoginToken(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", fmt.Errorf("parsear HTML de login: %w", err)
	}

	var token string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if token != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "form" && attr(n, "action") == "/account/login" {
			token = findCSRFInput(n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return token, nil
}

// findCSRFInput busca recursivamente el input _csrf_token dentro de un nodo.
func findCSRFInput(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "input" && attr(n, "name") == "_csrf_token" {
		return attr(n, "value")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v := findCSRFInput(c); v != "" {
			return v
		}
	}
	return ""
}

// attr devuelve el valor de un atributo de un nodo, o "" si no existe.
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
