// Package app is the desktop app's shell: it loads the fleet, plans and
// settings, and moves between the pages.
package app

import (
	"context"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/pages/addserver"
	"github.com/bubbleptr/vpsgogo/apps/desktop/pages/dashboard"
	"github.com/bubbleptr/vpsgogo/apps/desktop/pages/renewals"
	"github.com/bubbleptr/vpsgogo/apps/desktop/pages/server"
	"github.com/bubbleptr/vpsgogo/apps/desktop/pages/settings"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// Options are what the app runs on.
type Options struct {
	Backend services.Backend
	Runner  async.Runner
	Info    services.AppInfo
	Now     func() time.Time // calendar "today"; default time.Now
}

type route int

const (
	routeDashboard route = iota
	routeRenewals
	routeSettings
	routeServer
	routeAdd
)

// App is the shell's state.
type App struct {
	o       Options
	store   store.Store
	route   route
	loading bool
	loadErr string

	dashboard *dashboard.Page
	add       *addserver.Page
	server    *server.Page
	renewals  *renewals.Page
	settings  *settings.Page
}

// New returns the app and starts loading its data.
func New(o Options) *App {
	if o.Now == nil {
		o.Now = time.Now
	}
	a := &App{o: o}
	a.dashboard = dashboard.New(dashboard.Options{
		Info: o.Info, Backend: o.Backend, Runner: o.Runner, Store: &a.store,
		Now: o.Now, Today: a.today,
		OnAdd:  func() { a.navigate(routeAdd) },
		OnOpen: a.openServer,
	})
	a.add = addserver.New(addserver.Options{
		Backend: o.Backend, Runner: o.Runner, Store: &a.store, Today: a.today,
		OnOpen: a.openServer,
	})
	a.server = server.New(server.Options{
		Backend: o.Backend, Runner: o.Runner, Store: &a.store, Now: o.Now, Today: a.today,
		OnRemoved: func() { a.navigate(routeDashboard) },
	})
	a.renewals = renewals.New(renewals.Options{Backend: o.Backend, Runner: o.Runner, Store: &a.store, Today: a.today})
	a.settings = settings.New(settings.Options{Backend: o.Backend, Runner: o.Runner, Store: &a.store, Info: o.Info})
	a.load()
	return a
}

// today is the date in the user's timezone, or the system's.
func (a *App) today() renewal.Date {
	now := a.o.Now()
	if tz := a.store.Settings.Timezone; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			now = now.In(loc)
		}
	}
	return renewal.DateOf(now)
}

func (a *App) load() {
	a.loading, a.loadErr = true, ""
	b := a.o.Backend
	a.o.Runner.Go(func(post func(func())) {
		ctx := context.Background()
		servers, err := b.Servers(ctx)
		if err != nil {
			post(func() { a.loading, a.loadErr = false, err.Error() })
			return
		}
		plans, err := b.Plans(ctx)
		if err != nil {
			post(func() { a.loading, a.loadErr = false, err.Error() })
			return
		}
		s, err := b.Settings(ctx)
		if err != nil {
			post(func() { a.loading, a.loadErr = false, err.Error() })
			return
		}
		post(func() {
			a.loading = false
			a.store = store.Store{Loaded: true, Servers: servers, Plans: plans, Settings: s}
			if a.route == routeSettings {
				a.settings.Open() // its draft was taken from nothing
			}
		})
	})
}

// navigate leaves the current page and shows r.
func (a *App) navigate(r route) {
	switch a.route {
	case routeServer:
		if r != routeServer {
			a.server.Close()
		}
	case routeAdd:
		if r != routeAdd {
			a.add.Reset() // forgets the root password
		}
	}
	switch r {
	case routeDashboard:
		a.dashboard.Opened()
	case routeSettings:
		if a.route != routeSettings {
			a.settings.Open()
		}
	}
	a.route = r
}

func (a *App) openServer(id string) {
	a.navigate(routeServer)
	a.server.Open(id)
}

// View builds the window's content.
func (a *App) View(c *ui.Context) {
	pal := theme.For(c)
	theme.Canvas(ui.Row(c).Fill().AlignItems(ui.Stretch), pal).Children(func() {
		ui.Column(c).Width(180).Padding(24, 16).Gap(8).Background(pal.Cream).Children(func() {
			a.navButton(c, "Dashboard", routeDashboard)
			a.navButton(c, "Renewals", routeRenewals)
			a.navButton(c, "Settings", routeSettings)
		})
		ui.Column(c).Grow(1).Children(func() {
			switch {
			case a.loadErr != "":
				ui.Column(c).Padding(32).Gap(12).Children(func() {
					ui.Text(c, "VPSGoGo could not load your servers").FontWeight(700).TextColor(pal.Ink)
					ui.Text(c, a.loadErr).TextColor(theme.Coral)
					if ui.Button(c, "Try again").Clicked() {
						a.load()
					}
				})
			case !a.store.Loaded:
				ui.Text(c, "Loading…").TextColor(pal.Ink).Padding(32)
			default:
				a.page(c)
			}
		})
	})
}

func (a *App) navButton(c *ui.Context, label string, r route) {
	current := a.route == r || r == routeDashboard && (a.route == routeServer || a.route == routeAdd)
	var b ui.Element
	if current {
		b = ui.PrimaryButton(c, label)
	} else {
		b = ui.Button(c, label)
	}
	if b.Clicked() {
		a.navigate(r)
	}
}

func (a *App) page(c *ui.Context) {
	switch a.route {
	case routeDashboard:
		a.dashboard.View(c)
	case routeRenewals:
		a.renewals.View(c)
	case routeSettings:
		a.settings.View(c)
	case routeServer:
		a.server.View(c)
	case routeAdd:
		a.add.View(c)
	}
}
