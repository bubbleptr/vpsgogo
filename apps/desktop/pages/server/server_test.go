package server

import (
	"errors"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services/fake"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

var now = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

var web = fleet.Server{ID: "srv-1", Name: "web-1", Host: "203.0.113.7", Port: 22}

type rig struct {
	t       *testing.T
	b       *fake.Backend
	r       *async.Manual
	s       *store.Store
	p       *Page
	tt      *ui.Tester
	removed int
}

func newRig(t *testing.T, plans ...renewal.Subscription) *rig {
	g := &rig{t: t, b: fake.New(), r: &async.Manual{}, s: &store.Store{Loaded: true, Servers: []fleet.Server{web}}}
	g.b.Seed(web)
	for _, s := range plans {
		g.b.SeedPlan(s)
		g.s.PutPlan(s)
	}
	g.p = New(Options{
		Backend: g.b, Runner: g.r, Store: g.s,
		Now:       func() time.Time { return now },
		Today:     func() renewal.Date { return renewal.DateOf(now) },
		OnRemoved: func() { g.removed++ },
	})
	g.tt = ui.NewTester(g.p.View, 1200, 1000)
	return g
}

func (g *rig) open() {
	g.p.Open("srv-1")
	g.flush()
}

func (g *rig) flush() {
	g.r.Flush()
	g.tt.Frame()
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

func (g *rig) lastSince() time.Time {
	var since time.Time
	for _, c := range g.b.Calls() {
		if c.Method == "History" {
			since = c.Args[1].(time.Time)
		}
	}
	return since
}

var monthly = renewal.Subscription{ServerID: "srv-1", ExpiresAt: renewal.Date{Year: 2026, Month: 12, Day: 15}, Cycle: renewal.Monthly, Price: "5.99", Currency: "USD"}

func TestNoSamples(t *testing.T) {
	g := newRig(t)
	g.open()
	g.want("web-1", "No samples yet")
}

func TestStreamedSampleUpdatesReadouts(t *testing.T) {
	g := newRig(t)
	g.b.SetHistory("srv-1", monitoring.Sample{Time: now.Add(-time.Minute), CPU: 5, MemUsed: 1 << 30, MemTotal: 4 << 30})
	g.b.Push("srv-1", monitoring.Sample{Time: now, CPU: 42, MemUsed: 2 << 30, MemTotal: 4 << 30, NetRx: 2048, NetTx: 1024})
	g.open()
	g.want("CPU 42%", "Memory 50% · 2.0 GB / 4.0 GB", "Network in 2.0 KB/s · out 1.0 KB/s")
	if want := now.Add(-time.Hour); !g.lastSince().Equal(want) {
		t.Fatalf("since %v, want %v", g.lastSince(), want)
	}
}

func TestRangeTabsReloadHistory(t *testing.T) {
	g := newRig(t)
	g.open()
	g.click("24h")
	g.flush()
	if want := now.Add(-24 * time.Hour); !g.lastSince().Equal(want) {
		t.Fatalf("24h: since %v", g.lastSince())
	}
	g.click("7d")
	g.flush()
	if want := now.Add(-7 * 24 * time.Hour); !g.lastSince().Equal(want) {
		t.Fatalf("7d: since %v", g.lastSince())
	}
}

func TestStreamErrorOffersReconnect(t *testing.T) {
	g := newRig(t)
	g.b.FailStream("srv-1", errors.New("ssh: connection lost"))
	g.open()
	g.want("Live updates stopped: ssh: connection lost")
	g.b.Push("srv-1", monitoring.Sample{Time: now, CPU: 77})
	g.click("Reconnect")
	g.flush()
	g.want("CPU 77%")
	if g.tt.HasText("Live updates stopped") {
		t.Fatal("the error stays after reconnecting")
	}
	if n := g.b.Count("Stream"); n != 2 {
		t.Fatalf("Stream ran %d times", n)
	}
}

func TestCloseCancelsTheStream(t *testing.T) {
	g := newRig(t)
	g.b.Push("srv-1", monitoring.Sample{Time: now, CPU: 99})
	g.p.Open("srv-1")
	g.p.Close()
	g.flush()
	if len(g.p.samples) != 0 {
		t.Fatalf("a closed page took samples: %+v", g.p.samples)
	}
}

func TestAddPlanThenEdit(t *testing.T) {
	g := newRig(t)
	g.open()
	g.want("No plan")
	g.click("Add plan")
	g.click("Price")
	g.tt.Type("abc")
	g.click("Save plan")
	g.want(`renewal: price "abc" is not a number such as 5.99`)
	if g.b.Count("SavePlan") != 0 {
		t.Fatal("saved an invalid plan")
	}
	g.click("Price")
	g.tt.Key(ui.Super, ui.KeyA)
	g.tt.Type("4.50")
	g.click("Save plan")
	g.flush()
	g.want("Expires 2026-12-31 · monthly · 4.50 USD", "30 days left")
	if p, ok := g.b.Plan("srv-1"); !ok || p.Price != "4.50" {
		t.Fatalf("stored %+v", p)
	}
}

func TestMarkRenewed(t *testing.T) {
	g := newRig(t, monthly)
	g.open()
	g.want("Expires 2026-12-15 · monthly · 5.99 USD", "14 days left")
	g.click("Mark renewed")
	g.flush()
	g.want("Renewed until 2027-01-15", "Expires 2027-01-15")
	if p, _ := g.s.Plan("srv-1"); p.ExpiresAt != (renewal.Date{Year: 2027, Month: 1, Day: 15}) {
		t.Fatalf("store has %v", p.ExpiresAt)
	}
}

func TestOneTimePlanRenewRefused(t *testing.T) {
	once := monthly
	once.Cycle = renewal.Once
	g := newRig(t, once)
	g.open()
	g.click("Mark renewed")
	g.flush()
	g.want(renewal.ErrOneTime.Error())
}

func TestDeletePlan(t *testing.T) {
	g := newRig(t, monthly)
	g.open()
	g.click("Delete plan")
	g.flush()
	g.want("No plan")
	if _, ok := g.b.Plan("srv-1"); ok {
		t.Fatal("the backend kept the plan")
	}
}

func TestRemoveServerAsksFirst(t *testing.T) {
	g := newRig(t)
	g.open()
	g.click("Remove server")
	g.want("Remove web-1?")
	g.click("Cancel")
	g.flush()
	if g.b.Count("RemoveServer") != 0 {
		t.Fatal("removed after Cancel")
	}
	g.click("Remove server")
	g.click("Remove")
	g.flush()
	if g.b.Count("RemoveServer") != 1 || g.removed != 1 || len(g.s.Servers) != 0 {
		t.Fatalf("removed %d, callback %d, store %+v", g.b.Count("RemoveServer"), g.removed, g.s.Servers)
	}
}

func TestResultsAfterLeavingUpdateOnlyTheStore(t *testing.T) {
	g := newRig(t, monthly)
	g.open()
	g.click("Mark renewed")
	g.click("Remove server")
	g.click("Remove")
	g.p.Close() // the user left before either finished
	g.flush()
	if g.removed != 0 {
		t.Fatal("a removal that finished after leaving moved the user")
	}
	if len(g.s.Servers) != 0 {
		t.Fatal("the store kept the removed server")
	}
	if g.p.planMsg != "" {
		t.Fatalf("a renewal after leaving set %q", g.p.planMsg)
	}
}

func TestShrinkingTheRangeDropsOlderSamples(t *testing.T) {
	g := newRig(t)
	g.b.SetHistory("srv-1", monitoring.Sample{Time: now.Add(-3 * 24 * time.Hour), CPU: 66})
	g.open()
	g.click("7d")
	g.flush()
	g.want("CPU 66%")
	g.click("1h")
	g.flush()
	g.want("No samples yet")
	if g.tt.HasText("CPU 66%") {
		t.Fatal("1h shows a sample three days old")
	}
}

func TestHistoryErrorOffersReload(t *testing.T) {
	g := newRig(t)
	g.b.FailHistory("srv-1", errors.New("ssh: handshake failed"))
	g.open()
	g.want("History could not load: ssh: handshake failed")
	if g.tt.HasText("Live updates stopped") {
		t.Fatal("a history error shows as a stream error")
	}
	g.b.FailHistory("srv-1", nil)
	g.b.SetHistory("srv-1", monitoring.Sample{Time: now.Add(-time.Minute), CPU: 31})
	g.click("Reload history")
	g.flush()
	g.want("CPU 31%")
	if g.tt.HasText("History could not load") {
		t.Fatal("the error stays after reloading")
	}
}

func TestRenewThatDidNotSyncKeepsTheNewDate(t *testing.T) {
	g := newRig(t, monthly)
	g.open()
	g.b.SyncErr = errors.New("web-1: connection refused")
	g.click("Mark renewed")
	g.flush()
	g.want("Renewed until 2027-01-15", "Expires 2027-01-15",
		"Saved, but the agent wasn't updated: web-1: connection refused")

	g.b.SyncErr = nil
	g.click("Retry sync")
	g.flush()
	if g.tt.HasText("wasn't updated") {
		t.Fatal("the warning stays after a sync that worked")
	}
	if r, s := g.b.Count("Renew"), g.b.Count("SavePlan"); r != 1 || s != 1 {
		t.Fatalf("Renew ran %d times, SavePlan %d", r, s)
	}
	if p, _ := g.b.Plan("srv-1"); p.ExpiresAt != (renewal.Date{Year: 2027, Month: 1, Day: 15}) {
		t.Fatalf("stored expiry %v", p.ExpiresAt)
	}
}

func TestDeleteThatDidNotSyncRetriesTheDelete(t *testing.T) {
	g := newRig(t, monthly)
	g.open()
	g.b.SyncErr = errors.New("web-1: connection refused")
	g.click("Delete plan")
	g.flush()
	g.want("No plan", "Saved, but the agent wasn't updated")
	g.b.SyncErr = nil
	g.click("Retry sync")
	g.flush()
	if g.b.Count("DeletePlan") != 2 || g.b.Count("SavePlan") != 0 {
		t.Fatalf("calls %+v", g.b.Calls())
	}
	if g.tt.HasText("wasn't updated") {
		t.Fatal("the warning stays")
	}
}
