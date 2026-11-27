// Package server is one server's page: live charts of its samples, its
// renewal plan, and removing it (docs/SPEC.md §4.2, §4.3).
package server

import (
	"context"
	"fmt"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/plan"
	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/features/planform"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/format"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// Ranges are the spans of history the charts show.
var Ranges = []struct {
	Label string
	Span  time.Duration
}{{"1h", time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}}

// Options are what the page needs.
type Options struct {
	Backend services.Backend
	Runner  async.Runner
	Store   *store.Store
	Now     func() time.Time
	Today   func() renewal.Date
	// OnRemoved is called once the server is removed.
	OnRemoved func()
}

// Page is the server page's state.
type Page struct {
	o  Options
	id string
	// gen counts Opens and Closes: a result shows only on the page that
	// asked for it, though the store always takes it.
	gen int

	rng     int
	samples []monitoring.Sample
	// histGen and streamGen tell the results of the current loads from
	// those of loads started before the page changed.
	histGen, streamGen int
	cancel             context.CancelFunc
	streamErr          string
	historyErr         string

	form       *planform.Form // the plan being added or edited
	planMsg    string
	planErr    string
	planBusy   bool
	confirming bool
	removeErr  string
}

// New returns the page, showing no server until Open.
func New(o Options) *Page { return &Page{o: o} }

// Open shows server id and starts loading its history and its stream.
func (p *Page) Open(id string) {
	p.Close()
	*p = Page{o: p.o, id: id, gen: p.gen, histGen: p.histGen + 1, streamGen: p.streamGen + 1}
	p.loadHistory()
	p.stream()
}

// Close stops the stream; the shell calls it when the user leaves.
func (p *Page) Close() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.gen++
	p.streamGen++
}

func (p *Page) loadHistory() {
	p.histGen++
	gen, id, span := p.histGen, p.id, Ranges[p.rng].Span
	since := p.o.Now().Add(-span)
	p.o.Runner.Go(func(post func(func())) {
		samples, err := p.o.Backend.History(context.Background(), id, since)
		post(func() {
			if gen != p.histGen {
				return
			}
			if err != nil {
				p.historyErr = err.Error()
				samples = nil // the range still applies to what was streamed
			} else {
				p.historyErr = ""
			}
			p.samples = mergeLive(samples, p.samples, since)
		})
	})
}

// mergeLive adds to history the current samples it does not hold: those
// taken from since on, after the history's last.
func mergeLive(history, current []monitoring.Sample, since time.Time) []monitoring.Sample {
	out := history
	for _, s := range current {
		if s.Time.Before(since) {
			continue
		}
		if len(history) == 0 || s.Time.After(history[len(history)-1].Time) {
			out = append(out, s)
		}
	}
	return out
}

func (p *Page) stream() {
	p.streamGen++
	gen, id := p.streamGen, p.id
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.streamErr = cancel, ""
	p.o.Runner.Go(func(post func(func())) {
		err := p.o.Backend.Stream(ctx, id, func(s monitoring.Sample) {
			post(func() {
				if gen == p.streamGen {
					p.add(s)
				}
			})
		})
		post(func() {
			if gen == p.streamGen && err != nil && ctx.Err() == nil {
				p.streamErr = err.Error()
			}
		})
	})
}

// add appends a streamed sample and drops those older than the range.
func (p *Page) add(s monitoring.Sample) {
	from := s.Time.Add(-Ranges[p.rng].Span)
	i := 0
	for i < len(p.samples) && p.samples[i].Time.Before(from) {
		i++
	}
	p.samples = append(p.samples[i:], s)
}

// View builds the page.
func (p *Page) View(c *ui.Context) {
	pal := theme.For(c)
	srv, ok := p.o.Store.Server(p.id)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Padding(32).Gap(16).Children(func() {
			if !ok {
				ui.Text(c, "This server is gone.").TextColor(pal.Ink)
				return
			}
			ui.Text(c, srv.Name).FontSize(22).FontWeight(800).TextColor(pal.Ink)
			ui.Text(c, fmt.Sprintf("%s:%d · %s", srv.Host, srv.Port, srv.HostKeyFP)).TextColor(pal.Ink).Opacity(.7)
			p.metrics(c, pal)
			p.plan(c, pal)
			p.remove(c, pal, srv.Name)
		})
	})
}

func (p *Page) metrics(c *ui.Context, pal theme.Palette) {
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, "Monitoring").FontSize(18).FontWeight(700).TextColor(pal.Ink).Grow(1)
		if ui.Tabs(c, &p.rng, Ranges[0].Label, Ranges[1].Label, Ranges[2].Label).Changed() {
			p.loadHistory()
		}
	})
	if p.streamErr != "" {
		ui.Row(c).Gap(12).Children(func() {
			ui.Text(c, "Live updates stopped: "+p.streamErr).TextColor(theme.Coral)
			if ui.Button(c, "Reconnect").Clicked() {
				p.stream()
			}
		})
	}
	if p.historyErr != "" {
		ui.Row(c).Gap(12).Children(func() {
			ui.Text(c, "History could not load: "+p.historyErr).TextColor(theme.Coral)
			if ui.Button(c, "Reload history").Clicked() {
				p.loadHistory()
			}
		})
	}
	if len(p.samples) == 0 {
		ui.Text(c, "No samples yet").TextColor(pal.Ink).Opacity(.7)
		return
	}
	last := p.samples[len(p.samples)-1]
	memPct := format.Percent(last.MemUsed, last.MemTotal)
	maxNet := 1.0
	for _, s := range p.samples {
		maxNet = max(maxNet, s.NetRx, s.NetTx)
	}
	chartRow(c, pal, fmt.Sprintf("CPU %.0f%%", last.CPU), theme.Teal, p.series(func(s monitoring.Sample) float64 { return s.CPU / 100 }))
	chartRow(c, pal, fmt.Sprintf("Memory %.0f%% · %s / %s", memPct, format.Bytes(last.MemUsed), format.Bytes(last.MemTotal)), theme.Green,
		p.series(func(s monitoring.Sample) float64 { return format.Percent(s.MemUsed, s.MemTotal) / 100 }))
	chartRow(c, pal, fmt.Sprintf("Network in %s · out %s", format.Rate(last.NetRx), format.Rate(last.NetTx)), theme.Amber,
		p.series(func(s monitoring.Sample) float64 { return s.NetRx / maxNet }),
		p.series(func(s monitoring.Sample) float64 { return s.NetTx / maxNet }))
}

// series returns f of every sample, each a fraction from 0 to 1.
func (p *Page) series(f func(monitoring.Sample) float64) []float64 {
	out := make([]float64, len(p.samples))
	for i, s := range p.samples {
		out[i] = min(max(f(s), 0), 1)
	}
	return out
}

// chartRow draws lines of fractions beside their readout, which is the
// text tests and screen readers get.
func chartRow(c *ui.Context, pal theme.Palette, readout string, color ui.Color, lines ...[]float64) {
	ui.Row(c).Gap(16).Children(func() {
		ui.Text(c, readout).FontWeight(700).TextColor(pal.Ink).Width(280)
		ui.Column(c).Grow(1).Height(72).Radius(8).Background(pal.Cream).Draw(func(pt *ui.Painter, r ui.Rect) {
			for _, line := range lines {
				if len(line) < 2 {
					continue
				}
				pts := make([][2]float32, len(line))
				for i, v := range line {
					x := r.X + r.W*float32(i)/float32(len(line)-1)
					y := r.Y + r.H - 4 - (r.H-8)*float32(v)
					pts[i] = [2]float32{x, y}
				}
				pt.StrokePolyline(pts, 2, color)
			}
		})
	})
}

func (p *Page) plan(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "Renewal plan").FontSize(18).FontWeight(700).TextColor(pal.Ink)
	sub, has := p.o.Store.Plan(p.id)
	switch {
	case p.form != nil:
		p.form.View(c)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				p.form = nil
			}
			if ui.PrimaryButton(c, "Save plan").Disabled(p.planBusy).Clicked() {
				p.savePlan()
			}
		})
	case !has:
		ui.Text(c, "No plan").TextColor(pal.Ink).Opacity(.7)
		if ui.Button(c, "Add plan").Clicked() {
			p.form, p.planMsg, p.planErr = planform.New(p.o.Today()), "", ""
		}
	default:
		ui.Text(c, plan.Summary(sub)).TextColor(pal.Ink)
		plan.Badge(c, sub, p.o.Today())
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Edit plan").Clicked() {
				p.form, p.planMsg, p.planErr = planform.Edit(sub), "", ""
			}
			if ui.Button(c, "Mark renewed").Disabled(p.planBusy).Clicked() {
				p.renew()
			}
			if ui.Button(c, "Delete plan").Disabled(p.planBusy).Clicked() {
				p.deletePlan()
			}
		})
	}
	if p.planMsg != "" {
		ui.Text(c, p.planMsg).TextColor(theme.Green)
	}
	if p.planErr != "" {
		ui.Text(c, p.planErr).TextColor(theme.Coral)
	}
}

// do runs one plan call. On success its store update applies, and, if the
// page still shows this server, msg.
func (p *Page) do(call func(ctx context.Context) (update func(), msg string, err error)) {
	p.planBusy, p.planMsg, p.planErr = true, "", ""
	gen := p.gen
	p.o.Runner.Go(func(post func(func())) {
		update, msg, err := call(context.Background())
		post(func() {
			if err == nil {
				update()
			}
			if gen != p.gen {
				return
			}
			p.planBusy = false
			if err != nil {
				p.planErr = err.Error()
				return
			}
			p.form, p.planMsg = nil, msg
		})
	})
}

func (p *Page) savePlan() {
	s, err := p.form.Plan(p.id)
	if err != nil {
		return // the form shows it
	}
	p.do(func(ctx context.Context) (func(), string, error) {
		return func() { p.o.Store.PutPlan(s) }, "", p.o.Backend.SavePlan(ctx, s)
	})
}

func (p *Page) renew() {
	id := p.id
	p.do(func(ctx context.Context) (func(), string, error) {
		s, err := p.o.Backend.Renew(ctx, id)
		return func() { p.o.Store.PutPlan(s) }, "Renewed until " + s.ExpiresAt.String(), err
	})
}

func (p *Page) deletePlan() {
	id := p.id
	p.do(func(ctx context.Context) (func(), string, error) {
		return func() { p.o.Store.RemovePlan(id) }, "", p.o.Backend.DeletePlan(ctx, id)
	})
}

func (p *Page) remove(c *ui.Context, pal theme.Palette, name string) {
	ui.Divider(c)
	if ui.Button(c, "Remove server").Clicked() {
		p.confirming = true
	}
	if p.removeErr != "" {
		ui.Text(c, p.removeErr).TextColor(theme.Coral)
	}
	msg := "VPSGoGo forgets this server, its plan and its SSH key. Once set up, the server allows neither root nor password logins, so without this key nobody can log in over SSH. The server itself keeps running."
	if ui.AlertDialog(c, &p.confirming, "Remove "+name+"?", msg, "Cancel", "Remove") == 1 {
		id, gen := p.id, p.gen
		p.o.Runner.Go(func(post func(func())) {
			err := p.o.Backend.RemoveServer(context.Background(), id)
			post(func() {
				if err == nil {
					p.o.Store.RemoveServer(id)
				}
				if gen != p.gen {
					return
				}
				if err != nil {
					p.removeErr = err.Error()
					return
				}
				p.Close()
				if p.o.OnRemoved != nil {
					p.o.OnRemoved()
				}
			})
		})
	}
}
