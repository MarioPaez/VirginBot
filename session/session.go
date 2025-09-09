package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/MarioPaez/VirginBot/opts"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const (
	URL_LOGIN    = "https://shop.virginactive.it/account/login"
	URL_CALENDAR = "https://www.virginactive.it/calendario-corsi"
	USER         = "VA_EMAIL"
	PASS         = "VA_PASS"
	CHROME_PATH  = `/mnt/c/Program Files/Google/Chrome/Application/`
)

func DoLogin() {
	user := os.Getenv(USER)
	pass := os.Getenv(PASS)

	opts := opts.DefineOpts()

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithDebugf(log.Printf)) //, chromedp.WithDebugf(log.Printf)
	defer cancel()

	// var cookies []*network.Cookie
	if err := chromedp.Run(ctx,
		chromedp.Navigate(URL_LOGIN),
		chromedp.Sleep(1*time.Second), //wait popup
		chromedp.Click(`button.iubenda-cs-accept-btn.iubenda-cs-btn-primary`, chromedp.NodeVisible), //accept cookies
		chromedp.WaitVisible(`input[name="username"]`),
		chromedp.SendKeys(`input[name="username"]`, user),
		chromedp.SendKeys(`input[name="password"]`, pass),
		chromedp.Click(`button.vrgnBtn.vrgnBtnRight.vrgnBtnRight-flexend[name="login"]`, chromedp.NodeVisible), //sign in
		chromedp.WaitVisible("subscription-go-to-courses btn btn-primary mt-4"),                                //Wait to do the login

		//LUEGO BORRAR
		browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeGeolocation}).WithOrigin(URL_CALENDAR),
		chromedp.Navigate(URL_CALENDAR),
		chromedp.Click(`iubenda-cs-accept-btn iubenda-cs-btn-primary`, chromedp.NodeVisible),
		chromedp.Click(`textarea.select2-search__field[aria-describedby="select2-ClassesNames-container"]`, chromedp.NodeVisible),
		// Escribe el texto
		chromedp.SendKeys(`textarea.select2-search__field[aria-describedby="select2-ClassesNames-container"]`, "Calisthenics Performance"),

		// Confirma la selección (Enter)
		chromedp.SendKeys(`textarea.select2-search__field[aria-describedby="select2-ClassesNames-container"]`, kb.Enter),
		chromedp.Sleep(1*time.Second),
		chromedp.SendKeys(`textarea.select2-search__field[aria-describedby="select2-ClassesNames-container"]`, "Calisthenics"),
		chromedp.SendKeys(`textarea.select2-search__field[aria-describedby="select2-ClassesNames-container"]`, kb.Enter),
		//chromedp.Sleep(5*time.Second), //wait enter effect
		//! CLubes
		// chromedp.Click(`#select2-ClubsNames-container .select2-search__field`, chromedp.NodeVisible),
		// 2️⃣ Escribir el texto en el input de búsqueda
		chromedp.SendKeys(`textarea.select2-search__field[aria-describedby="select2-ClubsNames-container"]`, "Milano Corso Como"),

		// 3️⃣ Confirmar la selección con Enter
		chromedp.SendKeys(`textarea.select2-search__field[aria-describedby="select2-ClubsNames-container"]`, kb.Enter),
		chromedp.Sleep(120*time.Second),
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
		log.Printf("chromedp error: %+v", err)
	}
	fmt.Println("sign in successfully")
	//FindClasses(ctx)
}

func FindClasses(ctx context.Context) {
	if err := chromedp.Run(ctx,
		browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeGeolocation}).WithOrigin(URL_CALENDAR),
		chromedp.Navigate(URL_CALENDAR),
		chromedp.Click(`iubenda-cs-accept-btn iubenda-cs-btn-primary`, chromedp.NodeVisible),
		chromedp.Sleep(120*time.Second),
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
	}
}
