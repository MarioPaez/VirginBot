package opts

import "github.com/chromedp/chromedp"

func DefineOpts() []chromedp.ExecAllocatorOption {
	return append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath("/usr/bin/google-chrome"),
		chromedp.Flag("headless", false),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("block-new-web-contents", true),
		chromedp.Flag("disable-features", "Translate"),
	)
}
