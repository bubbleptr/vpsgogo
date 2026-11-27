// Package dashboard is the app's first page: a card per server with its
// status, its latest figures and its plan's days left (docs/SPEC.md §4.2).
package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/plan"
	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/format"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

const (
	// PollEvery is how often the page asks each server for its samples.
	PollEvery = 30 * time.Second
	// PollTimeout bounds one poll, so a silent server counts as a failure
	// whatever the backend's own limits.
	PollTimeout = 15 * time.Second
	// OfflineAfter is how many polls in a row must fail before a server
	// shows as offline (docs/SPEC.md §4.2).
	OfflineAfter = 3
)

// Options are what the page needs. Without a Backend the page shows only
// the app info, as the standalone page of main.go does.
type Options struct {
	Info    services.AppInfo
	Backend services.Backend
	Runner  async.Runner
	Store   *store.Store
	Now     func() time.Time
	Today   func() renewal.Date
	OnAdd   func()
	OnOpen  func(id string)
}

// status is what the polls found out about one server.
type status struct {
	fails    int
	ok       bool // the last poll succeeded
	latest   *monitoring.Sample
	inFlight bool
	next     time.Time // on the ui clock; zero polls at once
}

// Page is the dashboard's state.
type Page struct {
	o      Options
	status map[string]*status
}

// New returns the page over o's backend.
func New(o Options) *Page { return &Page{o: o, status: map[string]*status{}} }

// Opened makes the page poll every server at its next frame, as when the
// user comes back to it.
func (p *Page) Opened() {
	for _, st := range p.status {
		st.next = time.Time{}
	}
}

// View builds the page.
func (p *Page) View(c *ui.Context) {
	pal := theme.For(c)
	theme.Canvas(ui.Column(c).Fill(), pal).Padding(32).Gap(16).Children(func() {
		ui.Row(c).Gap(12).Children(func() {
			ui.Text(c, "VPSGoGo").FontSize(26).FontWeight(800).TextColor(pal.Ink).Grow(1)
			if p.o.OnAdd != nil && len(p.servers()) > 0 {
				if ui.PrimaryButton(c, "Add server").Clicked() {
					p.o.OnAdd()
				}
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			p.fleet(c, pal)
		})
		ui.Text(c, fmt.Sprintf("v%s · %s/%s", p.o.Info.Version, p.o.Info.OS, p.o.Info.Arch)).TextColor(pal.Ink).Opacity(.6)
	})
}

func (p *Page) servers() []fleet.Server {
	if p.o.Store == nil {
		return nil
	}
	return p.o.Store.Servers
}

func (p *Page) fleet(c *ui.Context, pal theme.Palette) {
	if p.o.Store == nil {
		return
	}
	if !p.o.Store.Loaded {
		ui.Text(c, "Loading…").TextColor(pal.Ink)
		return
	}
	servers := p.servers()
	if len(servers) == 0 {
		ui.Column(c).Gap(12).Padding(24).Children(func() {
			ui.Text(c, "No servers yet").FontSize(18).FontWeight(700).TextColor(pal.Ink)
			ui.Text(c, "Paste the IP address and root password of a VPS you bought, and VPSGoGo does the rest.").TextColor(pal.Ink).Opacity(.7)
			if p.o.OnAdd != nil && ui.PrimaryButton(c, "Add server").Clicked() {
				p.o.OnAdd()
			}
		})
		return
	}
	p.poll(c, servers)
	ui.Column(c).Gap(12).Children(func() {
		for _, s := range servers {
			p.card(c.Key(s.ID), pal, s)
		}
	})
}

// poll starts the polls that are due and asks for a frame when the next
// one is.
func (p *Page) poll(c *ui.Context, servers []fleet.Server) {
	if p.o.Backend == nil {
		return
	}
	now := c.Now()
	var soonest time.Time
	for _, s := range servers {
		st := p.status[s.ID]
		if st == nil {
			st = &status{}
			p.status[s.ID] = st
		}
		if !st.inFlight && !now.Before(st.next) {
			p.start(s.ID, st)
			st.next = now.Add(PollEvery)
		}
		if soonest.IsZero() || st.next.Before(soonest) {
			soonest = st.next
		}
	}
	if !soonest.IsZero() {
		c.After(soonest.Sub(now))
	}
}

func (p *Page) start(id string, st *status) {
	st.inFlight = true
	since := p.o.Now().Add(-time.Minute)
	b := p.o.Backend
	p.o.Runner.Go(func(post func(func())) {
		ctx, cancel := context.WithTimeout(context.Background(), PollTimeout)
		defer cancel()
		samples, err := b.History(ctx, id, since)
		post(func() {
			st.inFlight = false
			if err != nil {
				st.fails++
				return
			}
			st.fails, st.ok = 0, true
			if len(samples) > 0 {
				last := samples[len(samples)-1]
				st.latest = &last
			}
		})
	})
}

func (p *Page) card(c *ui.Context, pal theme.Palette, s fleet.Server) {
	st := p.status[s.ID]
	if st == nil {
		st = &status{}
	}
	ui.Column(c).Padding(16).Gap(6).Radius(16).Background(pal.Paper).Border(2, pal.Ink).Children(func() {
		ui.Row(c).Gap(12).Children(func() {
			ui.Text(c, s.Name).FontSize(18).FontWeight(800).TextColor(pal.Ink)
			label, color := "unknown", pal.Ink
			switch {
			case st.fails >= OfflineAfter:
				label, color = "offline", theme.Coral
			case st.ok:
				label, color = "online", theme.Green
			}
			ui.Text(c, label).FontWeight(700).TextColor(color)
			ui.Spacer(c)
			if p.o.OnOpen != nil && ui.Button(c, "Open "+s.Name).Clicked() {
				p.o.OnOpen(s.ID)
			}
		})
		ui.Text(c, fmt.Sprintf("%s:%d", s.Host, s.Port)).TextColor(pal.Ink).Opacity(.7)
		if st.latest == nil {
			ui.Text(c, "No samples yet").TextColor(pal.Ink).Opacity(.7)
		} else {
			figures(c, pal, *st.latest)
		}
		if sub, ok := p.o.Store.Plan(s.ID); ok {
			plan.Badge(c, sub, p.o.Today())
		} else {
			ui.Text(c, "No plan").TextColor(pal.Ink).Opacity(.7)
		}
	})
}

func figures(c *ui.Context, pal theme.Palette, s monitoring.Sample) {
	ui.Row(c).Gap(16).Children(func() {
		ui.Text(c, fmt.Sprintf("CPU %.0f%%", s.CPU)).TextColor(pal.Ink)
		if s.MemTotal > 0 {
			ui.Text(c, fmt.Sprintf("Memory %s / %s", format.Bytes(s.MemUsed), format.Bytes(s.MemTotal))).TextColor(pal.Ink)
		}
		if d, ok := rootDisk(s.Disks); ok {
			ui.Text(c, fmt.Sprintf("Disk %s / %s", format.Bytes(d.Used), format.Bytes(d.Total))).TextColor(pal.Ink)
		}
	})
}

// rootDisk returns the disk mounted at /, or the first.
func rootDisk(disks []monitoring.Disk) (monitoring.Disk, bool) {
	for _, d := range disks {
		if d.Mount == "/" {
			return d, true
		}
	}
	if len(disks) > 0 {
		return disks[0], true
	}
	return monitoring.Disk{}, false
}
