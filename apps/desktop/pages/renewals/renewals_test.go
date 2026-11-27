package renewals

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services/fake"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

var today = renewal.Date{Year: 2026, Month: 12, Day: 1}

type rig struct {
	t  *testing.T
	b  *fake.Backend
	r  *async.Manual
	s  *store.Store
	tt *ui.Tester
}

func newRig(t *testing.T, plans ...renewal.Subscription) *rig {
	g := &rig{t: t, b: fake.New(), r: &async.Manual{}, s: &store.Store{Loaded: true, Servers: []fleet.Server{
		{ID: "srv-1", Name: "web-1"}, {ID: "srv-2", Name: "db-1"}, {ID: "srv-3", Name: "vpn-1"},
	}}}
	for _, p := range plans {
		g.b.SeedPlan(p)
		g.s.PutPlan(p)
	}
	page := New(Options{Backend: g.b, Runner: g.r, Store: g.s, Today: func() renewal.Date { return today }})
	g.tt = ui.NewTester(page.View, 1200, 1100)
	return g
}

func (g *rig) click(s string) {
	g.t.Helper()
	if err := g.tt.Click(s); err != nil {
		g.t.Fatalf("click %q: %v; texts %q", s, err, g.tt.Texts())
	}
}

func (g *rig) want(texts ...string) {
	g.t.Helper()
	for _, s := range texts {
		if !g.tt.HasText(s) {
			g.t.Fatalf("no %q among %q", s, g.tt.Texts())
		}
	}
}

func sub(id string, y int, m, d int, cycle renewal.Cycle, price, cur string) renewal.Subscription {
	return renewal.Subscription{ServerID: id, ExpiresAt: renewal.Date{Year: y, Month: time.Month(m), Day: d}, Cycle: cycle, Price: price, Currency: cur}
}

func TestNoPlans(t *testing.T) {
	g := newRig(t)
	g.want("No plans yet", "December 2026", "This month: nothing due")
}

func TestListsPlansByDaysLeftWithSpend(t *testing.T) {
	g := newRig(t,
		sub("srv-1", 2026, 12, 20, renewal.Monthly, "5.99", "USD"),
		sub("srv-2", 2026, 12, 3, renewal.Yearly, "50", "EUR"),
		sub("srv-3", 2027, 3, 1, renewal.Monthly, "2", "USD"),
	)
	g.want("db-1", "2 days left", "web-1", "19 days left", "vpn-1",
		"This month: 50 EUR · 5.99 USD", "This year: 50 EUR · 5.99 USD")
	texts := g.tt.Texts()
	db, web, vpn := slices.Index(texts, "db-1"), slices.Index(texts, "web-1"), slices.Index(texts, "vpn-1")
	if !(db < web && web < vpn) {
		t.Fatalf("not sorted by days left: %q", texts)
	}
	if _, ok := g.tt.Find("December 20, 2026: web-1 expires"); !ok {
		t.Fatalf("the calendar does not mark web-1's expiry")
	}
	g.click("Next month")
	g.want("January 2027")
	if _, ok := g.tt.Find("January 20, 2027: web-1 expires"); ok {
		t.Fatal("marks a date the plan does not expire on")
	}
}

func TestMarkRenewedFromTheList(t *testing.T) {
	g := newRig(t, sub("srv-1", 2026, 12, 20, renewal.Monthly, "5.99", "USD"))
	g.click("Mark web-1 renewed")
	g.r.Flush()
	g.tt.Frame()
	g.want("Expires 2027-01-20", "50 days left", "web-1 renewed until 2027-01-20")
}

func TestOneTimePlanRenewRefused(t *testing.T) {
	g := newRig(t, sub("srv-1", 2026, 12, 20, renewal.Once, "", ""))
	g.click("Mark web-1 renewed")
	g.r.Flush()
	g.tt.Frame()
	g.want(renewal.ErrOneTime.Error())
}

func TestExpiredPlan(t *testing.T) {
	g := newRig(t, sub("srv-1", 2026, 11, 30, renewal.Monthly, "", ""))
	g.want("Expired yesterday")
}

func TestRenewThatDidNotSyncKeepsTheNewDate(t *testing.T) {
	g := newRig(t, sub("srv-1", 2026, 12, 20, renewal.Monthly, "5.99", "USD"))
	g.b.SyncErr = errors.New("web-1: connection refused")
	g.click("Mark web-1 renewed")
	g.r.Flush()
	g.tt.Frame()
	g.want("web-1 renewed until 2027-01-20", "Expires 2027-01-20",
		"Saved, but the agent wasn't updated: web-1: connection refused")

	g.b.SyncErr = nil
	g.click("Retry sync")
	g.r.Flush()
	g.tt.Frame()
	if g.tt.HasText("wasn't updated") {
		t.Fatal("the warning stays after a sync that worked")
	}
	if r, s := g.b.Count("Renew"), g.b.Count("SavePlan"); r != 1 || s != 1 {
		t.Fatalf("Renew ran %d times, SavePlan %d", r, s)
	}
	if p, _ := g.b.Plan("srv-1"); p.ExpiresAt != (renewal.Date{Year: 2027, Month: 1, Day: 20}) {
		t.Fatalf("stored expiry %v", p.ExpiresAt)
	}
}
