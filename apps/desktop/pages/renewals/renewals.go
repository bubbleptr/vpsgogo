// Package renewals is the renewal calendar: the month's expiry days, every
// plan by days left, and what falls due this month and this year
// (docs/SPEC.md §4.3).
package renewals

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/plan"
	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// Options are what the page needs.
type Options struct {
	Backend services.Backend
	Runner  async.Runner
	Store   *store.Store
	Today   func() renewal.Date
}

// Page is the renewals page's state.
type Page struct {
	o Options
	// month is the first day of the month the calendar shows; zero shows
	// today's.
	month renewal.Date
	msg   string
	err   string
	busy  bool
	// warn says a renewal was stored but did not reach syncID's agent.
	warn   string
	syncID string
}

// New returns the page.
func New(o Options) *Page { return &Page{o: o} }

// View builds the page.
func (p *Page) View(c *ui.Context) {
	pal := theme.For(c)
	today := p.o.Today()
	if p.month.IsZero() {
		p.month = renewal.Date{Year: today.Year, Month: today.Month, Day: 1}
	}
	plans := slices.Clone(p.o.Store.Plans)
	slices.SortStableFunc(plans, func(a, b renewal.Subscription) int { return a.DaysLeft(today) - b.DaysLeft(today) })
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Padding(32).Gap(16).Children(func() {
			ui.Text(c, "Upcoming renewals").FontSize(22).FontWeight(800).TextColor(pal.Ink)
			p.spend(c, pal, plans, today)
			p.calendar(c, pal, plans, today)
			p.list(c, pal, plans, today)
		})
	})
}

func (p *Page) spend(c *ui.Context, pal theme.Palette, plans []renewal.Subscription, today renewal.Date) {
	monthStart := renewal.Date{Year: today.Year, Month: today.Month, Day: 1}
	monthEnd := renewal.DateOf(time.Date(today.Year, today.Month+1, 0, 0, 0, 0, 0, time.UTC))
	yearStart := renewal.Date{Year: today.Year, Month: time.January, Day: 1}
	yearEnd := renewal.Date{Year: today.Year, Month: time.December, Day: 31}
	ui.Text(c, "This month: "+amounts(renewal.Spend(plans, monthStart, monthEnd))).FontWeight(700).TextColor(pal.Ink)
	ui.Text(c, "This year: "+amounts(renewal.Spend(plans, yearStart, yearEnd))).FontWeight(700).TextColor(pal.Ink)
}

func amounts(as []renewal.Amount) string {
	if len(as) == 0 {
		return "nothing due"
	}
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = strings.TrimSpace(a.Total + " " + a.Currency)
	}
	return strings.Join(parts, " · ")
}

func (p *Page) calendar(c *ui.Context, pal theme.Palette, plans []renewal.Subscription, today renewal.Date) {
	first := time.Date(p.month.Year, p.month.Month, 1, 0, 0, 0, 0, time.UTC)
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "Previous month").Clicked() {
			p.month = renewal.DateOf(first.AddDate(0, -1, 0))
		}
		ui.Text(c, first.Format("January 2006")).FontSize(18).FontWeight(700).TextColor(pal.Ink).Width(180)
		if ui.Button(c, "Next month").Clicked() {
			p.month = renewal.DateOf(first.AddDate(0, 1, 0))
		}
	})
	expiring := map[renewal.Date][]renewal.Subscription{}
	for _, s := range plans {
		expiring[s.ExpiresAt] = append(expiring[s.ExpiresAt], s)
	}
	const cell = 48
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(4).Children(func() {
			for _, wd := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
				ui.Text(c, wd).Width(cell).TextColor(pal.Ink).Opacity(.6)
			}
		})
		start := first.AddDate(0, 0, -int(first.Weekday()))
		for week := range 6 {
			ui.Row(c.Key(week)).Gap(4).Children(func() {
				for wd := range 7 {
					day := start.AddDate(0, 0, week*7+wd)
					p.day(c.Key(wd), pal, day, expiring[renewal.DateOf(day)], today)
				}
			})
		}
	})
}

func (p *Page) day(c *ui.Context, pal theme.Palette, day time.Time, subs []renewal.Subscription, today renewal.Date) {
	box := ui.Column(c).Width(48).Height(40).Radius(8).Padding(4)
	d := renewal.DateOf(day)
	inMonth := day.Month() == p.month.Month
	if len(subs) > 0 && inMonth {
		color := theme.Teal
		for _, s := range subs {
			switch s.Urgency(today) {
			case renewal.Overdue:
				color = theme.Coral
			case renewal.Soon:
				if color != theme.Coral {
					color = theme.Amber
				}
			}
		}
		names := make([]string, len(subs))
		for i, s := range subs {
			names[i] = p.o.Store.Name(s.ServerID)
		}
		box.Background(color).Label(fmt.Sprintf("%s: %s expires", day.Format("January 2, 2006"), strings.Join(names, ", ")))
	}
	if d == today {
		box.Border(2, pal.Ink)
	}
	box.Children(func() {
		t := ui.Text(c, fmt.Sprint(day.Day())).TextColor(pal.Ink)
		if !inMonth {
			t.Opacity(.35)
		}
	})
}

func (p *Page) list(c *ui.Context, pal theme.Palette, plans []renewal.Subscription, today renewal.Date) {
	ui.Text(c, "Plans").FontSize(18).FontWeight(700).TextColor(pal.Ink)
	if len(plans) == 0 {
		ui.Text(c, "No plans yet").TextColor(pal.Ink).Opacity(.7)
	}
	if p.msg != "" {
		ui.Text(c, p.msg).TextColor(theme.Green)
	}
	if p.err != "" {
		ui.Text(c, p.err).TextColor(theme.Coral)
	}
	if p.warn != "" {
		ui.Row(c).Gap(12).Children(func() {
			ui.Text(c, p.warn).TextColor(theme.Amber)
			if ui.Button(c, "Retry sync").Disabled(p.busy).Clicked() {
				p.retrySync()
			}
		})
	}
	for _, s := range plans {
		name := p.o.Store.Name(s.ServerID)
		ui.Row(c.Key(s.ServerID)).Gap(16).Padding(12).Radius(12).Background(pal.Paper).Children(func() {
			ui.Text(c, name).FontWeight(700).TextColor(pal.Ink).Width(160)
			ui.Text(c, plan.Summary(s)).TextColor(pal.Ink).Grow(1)
			plan.Badge(c, s, today)
			if ui.Button(c, "Mark renewed").Label("Mark " + name + " renewed").Disabled(p.busy).Clicked() {
				p.renew(s.ServerID, name)
			}
		})
	}
}

func (p *Page) renew(id, name string) {
	p.busy, p.msg, p.err, p.warn = true, "", "", ""
	p.o.Runner.Go(func(post func(func())) {
		s, err := p.o.Backend.Renew(context.Background(), id)
		post(func() {
			p.busy = false
			// A renewal that did not sync was stored all the same: show it,
			// or renewing again would add another cycle.
			var unsynced *services.SyncError
			if err != nil && !errors.As(err, &unsynced) {
				p.err = err.Error()
				return
			}
			p.o.Store.PutPlan(s)
			p.msg = fmt.Sprintf("%s renewed until %s", name, s.ExpiresAt)
			if unsynced != nil {
				p.warn, p.syncID = "Saved, but the agent wasn't updated: "+unsynced.Err.Error(), id
			}
		})
	})
}

// retrySync saves the stored plan again, which syncs it without renewing.
func (p *Page) retrySync() {
	s, ok := p.o.Store.Plan(p.syncID)
	if !ok {
		p.warn = ""
		return
	}
	p.busy, p.err = true, ""
	p.o.Runner.Go(func(post func(func())) {
		err := p.o.Backend.SavePlan(context.Background(), s)
		post(func() {
			p.busy = false
			var unsynced *services.SyncError
			switch {
			case errors.As(err, &unsynced):
				p.warn = "Saved, but the agent wasn't updated: " + unsynced.Err.Error()
			case err != nil:
				p.err = err.Error()
			default:
				p.warn = ""
			}
		})
	})
}
