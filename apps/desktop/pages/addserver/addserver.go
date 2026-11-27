// Package addserver is the add-server wizard: where the server is, its
// host key, an optional renewal plan, then setup (docs/SPEC.md §1.1, §4.1).
// It logs in with the root password only; private-key root login is not
// offered yet.
package addserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/features/planform"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/provisioning"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// Options are what the wizard needs.
type Options struct {
	Backend services.Backend
	Runner  async.Runner
	Store   *store.Store
	Today   func() renewal.Date
	// OnOpen opens the server set up.
	OnOpen func(id string)
}

type step int

const (
	stepServer step = iota
	stepFingerprint
	stepPlan
	stepSetup
)

// Page is the wizard's state.
type Page struct {
	o    Options
	step step
	// gen counts Resets: a task's result applies only to the wizard that
	// started it.
	gen int

	name, host, provider string
	port                 float64
	// password is the root password: it is never shown, and is dropped
	// once setup succeeds or the wizard is left.
	password string

	busy        bool
	err         string
	fingerprint string

	plan     *planform.Form
	withPlan bool

	server    *fleet.Server // stored by AddServer
	planSaved bool
	progress  []provisioning.StepResult
	done      bool
}

// New returns the wizard at its first step.
func New(o Options) *Page {
	p := &Page{o: o}
	p.Reset()
	return p
}

// Reset starts the wizard over and forgets the password.
func (p *Page) Reset() {
	*p = Page{o: p.o, gen: p.gen + 1, port: 22}
}

// View builds the current step.
func (p *Page) View(c *ui.Context) {
	pal := theme.For(c)
	ui.Column(c).Fill().Padding(32).Gap(16).Children(func() {
		ui.Text(c, "Add a server").FontSize(22).FontWeight(800).TextColor(pal.Ink)
		switch p.step {
		case stepServer:
			p.serverStep(c)
		case stepFingerprint:
			p.fingerprintStep(c, pal)
		case stepPlan:
			p.planStep(c, pal)
		case stepSetup:
			p.setupStep(c, pal)
		}
		if p.err != "" {
			ui.Text(c, p.err).TextColor(theme.Coral)
		}
	})
}

func (p *Page) serverStep(c *ui.Context) {
	ui.Form(c, func() {
		ui.Field(c, "Name", func() { ui.TextInput(c, &p.name).Placeholder("Optional") })
		ui.Field(c, "Host", func() { ui.TextInput(c, &p.host).Placeholder("IP address or host name") })
		ui.Field(c, "Port", func() { ui.NumberInput(c, &p.port, 0, 999999, 1) })
		ui.Field(c, "Provider", func() { ui.TextInput(c, &p.provider).Placeholder("Optional") })
		ui.Field(c, "Root password", func() { ui.TextInput(c, &p.password).Password() })
	})
	if p.busy {
		ui.Text(c, "Connecting…")
	}
	if ui.PrimaryButton(c, "Connect").Disabled(p.busy).Clicked() {
		p.connect()
	}
}

func (p *Page) connect() {
	host := strings.TrimSpace(p.host)
	switch {
	case host == "":
		p.err = "Enter the server's IP address or host name."
		return
	case p.port < 1 || p.port > 65535 || p.port != float64(int(p.port)):
		p.err = "The port must be between 1 and 65535."
		return
	}
	p.err, p.busy = "", true
	port, gen := int(p.port), p.gen
	p.o.Runner.Go(func(post func(func())) {
		fp, err := p.o.Backend.Probe(context.Background(), host, port)
		post(func() {
			if gen != p.gen {
				return
			}
			p.busy = false
			if err != nil {
				p.err = err.Error()
				return
			}
			p.fingerprint, p.step = fp, stepFingerprint
		})
	})
}

func (p *Page) fingerprintStep(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "Check the server's host key fingerprint").FontWeight(700).TextColor(pal.Ink)
	ui.Text(c, "Your provider shows it in the server's console. Trust it only if it matches.").TextColor(pal.Ink).Opacity(.7)
	ui.Text(c, p.fingerprint).FontWeight(700).TextColor(pal.Ink)
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "Back").Clicked() {
			p.step = stepServer
		}
		if ui.PrimaryButton(c, "Trust this server").Clicked() {
			if p.plan == nil {
				p.plan = planform.New(p.o.Today())
			}
			p.step = stepPlan
		}
	})
}

func (p *Page) planStep(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "Renewal plan").FontWeight(700).TextColor(pal.Ink)
	ui.Text(c, "VPSGoGo reminds you before it expires. You can add it later.").TextColor(pal.Ink).Opacity(.7)
	p.plan.View(c)
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "Back").Clicked() {
			p.step = stepFingerprint
		}
		if ui.Button(c, "Skip").Clicked() {
			p.withPlan = false
			p.startSetup()
		}
		// The server has no ID until setup stores it, so the plan is
		// checked under a stand-in.
		if ui.PrimaryButton(c, "Continue").Clicked() {
			if _, err := p.plan.Plan("new"); err == nil {
				p.withPlan = true
				p.startSetup()
			}
		}
	})
}

// startSetup runs setup from its first unfinished stage: AddServer, then
// SavePlan, then Provision. A retry never adds the server again.
func (p *Page) startSetup() {
	p.step, p.busy, p.done, p.err, p.progress = stepSetup, true, false, "", nil
	srv, gen := p.server, p.gen
	var plan *renewal.Subscription
	if p.withPlan && !p.planSaved {
		s, err := p.plan.Plan("new")
		if err != nil {
			p.step, p.busy = stepPlan, false
			return
		}
		plan = &s
	}
	n := fleet.NewServer{Name: strings.TrimSpace(p.name), Host: strings.TrimSpace(p.host), Port: int(p.port), Provider: strings.TrimSpace(p.provider)}
	fp, password := p.fingerprint, p.password
	b := p.o.Backend
	p.o.Runner.Go(func(post func(func())) {
		ctx := context.Background()
		// The store learns of what was stored even if the user left; the
		// wizard only of its own setup.
		mine := func(f func()) func() {
			return func() {
				if gen == p.gen {
					f()
				}
			}
		}
		fail := func(err error) { post(mine(func() { p.busy, p.err = false, err.Error() })) }
		if srv == nil {
			s, err := b.AddServer(ctx, n, fp)
			if err != nil {
				fail(err)
				return
			}
			srv = &s
			post(func() { p.o.Store.PutServer(s); mine(func() { p.server = &s })() })
		}
		if plan != nil {
			plan.ServerID = srv.ID
			if err := b.SavePlan(ctx, *plan); err != nil {
				fail(err)
				return
			}
			saved := *plan
			post(func() { p.o.Store.PutPlan(saved); mine(func() { p.planSaved = true })() })
		}
		err := b.Provision(ctx, srv.ID, password, func(r provisioning.StepResult) {
			post(mine(func() { p.progress = append(p.progress, r) }))
		})
		if err != nil {
			fail(err)
			return
		}
		post(mine(func() { p.busy, p.done, p.password = false, true, "" }))
	})
}

func (p *Page) setupStep(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "Setting up").FontWeight(700).TextColor(pal.Ink)
	if p.busy {
		ui.Progress(c, -1).Label("Setup in progress")
	}
	ui.Column(c).Gap(4).Children(func() {
		for i, r := range p.progress {
			line, color := r.Step+": done", theme.Green
			if r.Error != "" {
				line, color = r.Step+": failed", theme.Coral
			}
			ui.Text(c.Key(i), line).TextColor(color)
		}
	})
	switch {
	case p.done:
		name := ""
		if p.server != nil {
			name = p.server.Name
		}
		ui.Text(c, "Server ready").FontWeight(700).TextColor(theme.Green)
		ui.Text(c, fmt.Sprintf("%s is hardened and reports to VPSGoGo.", name)).TextColor(pal.Ink)
		if ui.PrimaryButton(c, "Open server").Clicked() && p.server != nil && p.o.OnOpen != nil {
			p.o.OnOpen(p.server.ID)
		}
	case !p.busy && p.err != "":
		if ui.PrimaryButton(c, "Retry").Clicked() {
			p.startSetup()
		}
	}
}
