package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

const (
	URL_LOGIN    = "https://shop.virginactive.it/account/login"
	URL_CALENDAR = "https://www.virginactive.it/calendario-corsi"
	USER         = "VA_EMAIL"
	PASS         = "VA_PASS"
	CHROME_PATH  = `/mnt/c/Program Files/Google/Chrome/Application/`
)

func DoLogin() context.Context {
	user := os.Getenv(USER)
	pass := os.Getenv(PASS)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath("/usr/bin/google-chrome-stable"), // o donde esté tu chrome
		chromedp.Flag("headless", false),                   // esencial
		//chromedp.Flag("disable-gpu", true),                                              // evita errores en WSL
		//chromedp.Flag("no-sandbox", true),                                               // necesario si WSL no permite sandbox
		//chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("ignore-certificate-errors", true),
		//chromedp.Flag("use-fake-ui-for-media-stream", true),
		//chromedp.Flag("block-new-web-contents", true), es para forzar no nuevos tabs
		//chromedp.Flag("disable-notifications", true),
		chromedp.Flag("disable-features", "Translate"),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Run(ctx,
		chromedp.Navigate(URL_LOGIN),
		chromedp.Sleep(1*time.Second), // esperar a que aparezca el popup
		// Hacer click en "Rifiuta" para rechazar cookies
		chromedp.Click(`button.iubenda-cs-reject-btn.iubenda-cs-btn-primary`, chromedp.NodeVisible),
		chromedp.WaitVisible(`input[name="username"]`),
		chromedp.SendKeys(`input[name="username"]`, user),
		chromedp.SendKeys(`input[name="password"]`, pass),
		chromedp.Click(`button.vrgnBtn.vrgnBtnRight.vrgnBtnRight-flexend[name="login"]`, chromedp.NodeVisible),
		chromedp.Click(`subscription-go-to-courses btn btn-primary mt-4`, chromedp.NodeVisible),
		// browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeGeolocation}),
		// chromedp.Click(`iubenda-cs-accept-btn iubenda-cs-btn-primary`, chromedp.NodeVisible),
		// chromedp.Sleep(120*time.Second),
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
	}
	fmt.Println("signed in")
	return ctx
}

func FindClasses(ctx context.Context) {
	if err := chromedp.Run(ctx,
		chromedp.Navigate(URL_CALENDAR),
		browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeGeolocation}),
		chromedp.Click(`iubenda-cs-accept-btn iubenda-cs-btn-primary`, chromedp.NodeVisible),
		chromedp.Sleep(120*time.Second),
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
	}
}
