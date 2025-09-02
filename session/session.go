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

func DoLogin() {
	user := os.Getenv(USER)
	pass := os.Getenv(PASS)

	opts := defineOpts()

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Run(ctx,
		chromedp.Navigate(URL_LOGIN),
		chromedp.Sleep(1*time.Second), //wait popup
		chromedp.Click(`button.iubenda-cs-reject-btn.iubenda-cs-btn-primary`, chromedp.NodeVisible), //refuse cookies
		chromedp.WaitVisible(`input[name="username"]`),
		chromedp.SendKeys(`input[name="username"]`, user),
		chromedp.SendKeys(`input[name="password"]`, pass),
		chromedp.Click(`button.vrgnBtn.vrgnBtnRight.vrgnBtnRight-flexend[name="login"]`, chromedp.NodeVisible), //sign in
		chromedp.Click(`subscription-go-to-courses btn btn-primary mt-4`, chromedp.NodeVisible),                //Go course page
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
	}
	fmt.Println("signed in")
	FindClasses(ctx)
}

func defineOpts() []chromedp.ExecAllocatorOption {
	return append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath("/usr/bin/google-chrome-stable"),
		chromedp.Flag("headless", false),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("block-new-web-contents", true),
		chromedp.Flag("disable-features", "Translate,TranslateUI"),
	)
}

func FindClasses(ctx context.Context) {
	if err := chromedp.Run(ctx,
		browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeGeolocation}),
		chromedp.Click(`iubenda-cs-accept-btn iubenda-cs-btn-primary`, chromedp.NodeVisible),
		chromedp.Sleep(120*time.Second),
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
	}
}
