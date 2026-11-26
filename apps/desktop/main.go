package main

import (
	"log"

	"github.com/bubbleptr/vpsgogo/apps/desktop/pages/dashboard"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	page := dashboard.New(services.System{Version: version}.Info())

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "VPSGoGo",
			Width:           1200,
			Height:          780,
			MinWidth:        720,
			MinHeight:       480,
			BackgroundColor: theme.WindowBackground,
			StateKey:        "main",
			Content:         ui.View(page.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
