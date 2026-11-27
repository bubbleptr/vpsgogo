package dashboard

import (
	"errors"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services/fake"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

var now = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

func TestShowsAppInfo(t *testing.T) {
	p := New(Options{Info: services.AppInfo{Version: "0.1.0-test", OS: "darwin", Arch: "arm64"}})
	for _, dark := range []bool{false, true} {
		tt := ui.NewTesterWith(p.View, 1200, 780, ui.TesterOptions{Dark: dark})
		for _, want := range []string{"VPSGoGo", "v0.1.0-test · darwin/arm64"} {
			if !tt.HasText(want) {
				t.Errorf("dark=%v: no %q among %q", dark, want, tt.Texts())
			}
		}
	}
}

type rig struct {
	t      *testing.T
	b      *fake.Backend
	r      *async.Manual
	s      *store.Store
	tt     *ui.Tester
	added  int
	opened string
}

func newRig(t *testing.T, servers ...fleet.Server) *rig {
	g := &rig{t: t, b: fake.New(), r: &async.Manual{}, s: &store.Store{Loaded: true, Servers: servers}}
	p := New(Options{
		Backend: g.b, Runner: g.r, Store: g.s,
		Now:    func() time.Time { return now },
		Today:  func() renewal.Date { return renewal.DateOf(now) },
		OnAdd:  func() { g.added++ },
		OnOpen: func(id string) { g.opened = id },
	})
	g.tt = ui.NewTester(p.View, 1200, 800) // its first frame starts the first poll
	return g
}

func (g *rig) flush() {
	g.r.Flush()
	g.tt.Frame()
}

func (g *rig) want(texts ...string) {
	g.t.Helper()
	for _, s := range texts {
		if !g.tt.HasText(s) {
			g.t.Fatalf("no %q among %q", s, g.tt.Texts())
		}
	}
}

var web = fleet.Server{ID: "srv-1", Name: "web-1", Host: "203.0.113.7", Port: 22}

func TestEmptyFleetOffersToAddAServer(t *testing.T) {
	g := newRig(t)
	g.want("No servers yet")
	if err := g.tt.Click("Add server"); err != nil {
		t.Fatal(err)
	}
	if g.added != 1 {
		t.Fatal("Add server did nothing")
	}
}

func TestCardShowsLatestSampleAndDaysLeft(t *testing.T) {
	g := newRig(t, web)
	g.s.PutPlan(renewal.Subscription{ServerID: "srv-1", ExpiresAt: renewal.Date{Year: 2026, Month: 12, Day: 13}, Cycle: renewal.Monthly})
	g.b.SetHistory("srv-1",
		monitoring.Sample{Time: now.Add(-20 * time.Second), CPU: 10},
		monitoring.Sample{Time: now.Add(-10 * time.Second), CPU: 42, MemUsed: 1 << 30, MemTotal: 2 << 30,
			Disks: []monitoring.Disk{{Mount: "/", Used: 10 << 30, Total: 40 << 30}}},
	)
	g.flush()
	g.want("web-1", "203.0.113.7:22", "online", "CPU 42%", "Memory 1.0 GB / 2.0 GB", "Disk 10.0 GB / 40.0 GB", "12 days left")
	if err := g.tt.Click("Open web-1"); err != nil || g.opened != "srv-1" {
		t.Fatalf("open: %v, opened %q", err, g.opened)
	}
	var since time.Time
	for _, c := range g.b.Calls() {
		if c.Method == "History" {
			since = c.Args[1].(time.Time)
		}
	}
	if want := now.Add(-time.Minute); !since.Equal(want) {
		t.Fatalf("History since %v, want %v", since, want)
	}
}

func TestNoPlanNoSamples(t *testing.T) {
	g := newRig(t, web)
	g.flush()
	g.want("No plan", "No samples yet", "online")
}

func TestExpiredPlanIsFlagged(t *testing.T) {
	g := newRig(t, web)
	g.s.PutPlan(renewal.Subscription{ServerID: "srv-1", ExpiresAt: renewal.Date{Year: 2026, Month: 11, Day: 28}, Cycle: renewal.Monthly})
	g.tt.Frame()
	g.want("Expired 3 days ago")
}

func TestOfflineAfterThreeFailedPolls(t *testing.T) {
	g := newRig(t, web)
	g.b.FailHistory("srv-1", errors.New("ssh: connect: timeout"))
	g.flush() // first poll
	g.want("unknown")
	for range 2 {
		g.tt.Advance(30 * time.Second)
		g.flush()
	}
	if n := g.b.Count("History"); n != 3 {
		t.Fatalf("%d polls in 60 s", n)
	}
	g.want("offline")

	g.b.FailHistory("srv-1", nil)
	g.tt.Advance(30 * time.Second)
	g.flush()
	g.want("online")
}

func TestPollsEveryThirtySeconds(t *testing.T) {
	g := newRig(t, web)
	g.flush()
	g.tt.Advance(29 * time.Second)
	g.flush()
	if n := g.b.Count("History"); n != 1 {
		t.Fatalf("%d polls before 30 s", n)
	}
	g.tt.Advance(time.Second)
	g.flush()
	if n := g.b.Count("History"); n != 2 {
		t.Fatalf("%d polls at 30 s", n)
	}
}

func TestWarnsFromSevenDaysLeft(t *testing.T) {
	for _, c := range []struct {
		day  int
		want string
		warn bool
	}{
		{9, "8 days left", false},
		{8, "7 days left · renew soon", true},
		{1, "Expires today · renew soon", true},
	} {
		g := newRig(t, web)
		g.s.PutPlan(renewal.Subscription{ServerID: "srv-1", ExpiresAt: renewal.Date{Year: 2026, Month: 12, Day: c.day}, Cycle: renewal.Monthly})
		g.tt.Frame()
		g.want(c.want)
		if got := g.tt.HasText("renew soon"); got != c.warn {
			t.Errorf("expires Dec %d: warns %v", c.day, got)
		}
	}
}

func TestPollHasItsOwnDeadline(t *testing.T) {
	g := newRig(t, web)
	start := time.Now()
	g.flush()
	for _, c := range g.b.Calls() {
		if c.Method != "History" {
			continue
		}
		deadline := c.Args[2].(time.Time)
		if deadline.IsZero() || deadline.Sub(start) > PollTimeout+time.Second {
			t.Fatalf("poll deadline %v, %v after it started", deadline, deadline.Sub(start))
		}
		return
	}
	t.Fatal("no poll")
}
