package main

import (
	"context"
	"log"

	"github.com/bubbleptr/vpsgogo/apps/desktop/app"
	agentbin "github.com/bubbleptr/vpsgogo/apps/desktop/embed"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	mygo.App.WhenReady(func() {
		dir, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		backend, err := services.OpenLive(context.Background(), services.LiveOptions{
			DataDir:     dir,
			Vault:       services.Keychain{},
			AgentBinary: agentbin.Binary,
		})
		if err != nil {
			log.Fatal(err)
		}
		// Results of background work wait for the window, then apply on its
		// thread.
		runner := &async.Live{}
		shell := app.New(app.Options{
			Backend: backend,
			Runner:  runner,
			Info:    services.System{Version: version}.Info(),
		})
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "VPSGoGo",
			Width:           1200,
			Height:          780,
			MinWidth:        720,
			MinHeight:       480,
			BackgroundColor: theme.WindowBackground,
			StateKey:        "main",
			Content:         ui.View(shell.View),
		})
		runner.Bind(win.Update)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
