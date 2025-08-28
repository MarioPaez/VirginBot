package session

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/chromedp/chromedp"
)

const (
	URL_LOGIN   = "https://shop.virginactive.it/account/login"
	USER        = "VA_EMAIL"
	PASS        = "VA_PASS"
	CHROME_PATH = "/mnt/c/Program Files/Google/Chrome/Application/chrome.exe"
)

func DoLogin() {
	user := os.Getenv(USER)
	pass := os.Getenv(PASS)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(CHROME_PATH),
		chromedp.Headless,
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Run(ctx,
		chromedp.Navigate(URL_LOGIN),
		chromedp.WaitVisible(`input[name="username"]`),
		chromedp.SendKeys(`input[name="username"]`, user),
		chromedp.SendKeys(`input[name="password"]`, pass),
		chromedp.Click(`button[type="submit"]`),
		chromedp.WaitVisible(`div.card-title`),
	); err != nil {
		log.Fatal("error trying during the sign in\n", err)
	}
	fmt.Println("signed in")
}

// Email
// <input type="text" name="username" ng-required="true" ng-model="email" pattern="^[\w-\.]+@([\w-]+\.)+[\w-]{2,4}$" value="">
// pass
// <input type="password" name="password" ng:model="password" required="required">
//"C:\Program Files\Google\Chrome\Application\chrome.exe"
//Wait after sign in
/*
<div class="card-title">
                        Gestione di abbonamenti
                    </div>
*/
