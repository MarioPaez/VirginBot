package dpcookies

import (
	"context"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func GetCookies(cookies *[]*network.Cookie) chromedp.ActionFunc {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		*cookies, err = network.GetCookies().Do(ctx)
		return err
	})
}
