package main

import (
	"log"

	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/egoist/mygo"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	mygo.Bind(services.System{Version: version})

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "VPSGoGo",
			Width:           1200,
			Height:          780,
			MinWidth:        720,
			MinHeight:       480,
			BackgroundColor: "light-dark(#F7F5F0, #1E2440)", // --canvas in src/shared/ui/tokens.css
			StateKey:        "main",
			URL:             "/",
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
