package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services/fake"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// now is the walk's wall clock: the calendar's today is 2026-12-01.
var now = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

const rootPassword = "Tr0ub4dor&3-root"

type walk struct {
	t  *testing.T
	b  *fake.Backend
	r  *async.Manual
	tt *ui.Tester
}

func newWalk(t *testing.T, dark bool) *walk {
	w := &walk{t: t, b: fake.New(), r: &async.Manual{}}
	a := New(Options{
		Backend: w.b, Runner: w.r,
		Info: services.AppInfo{Version: "0.1.0-test", OS: "darwin", Arch: "arm64"},
		Now:  func() time.Time { return now },
	})
	w.tt = ui.NewTesterWith(a.View, 1280, 1000, ui.TesterOptions{Dark: dark})
	w.flush()
	return w
}

func (w *walk) flush() {
	w.t.Helper()
	w.r.Flush()
	w.tt.Frame()
	w.noPassword()
}

func (w *walk) click(s string) {
	w.t.Helper()
	if err := w.tt.Click(s); err != nil {
		w.t.Fatalf("click %q: %v; texts %q", s, err, w.tt.Texts())
	}
	w.noPassword()
}

func (w *walk) typeInto(label, text string) {
	w.t.Helper()
	w.click(label)
	w.tt.Type(text)
	w.noPassword()
}

func (w *walk) want(texts ...string) {
	w.t.Helper()
	for _, s := range texts {
		if !w.tt.HasText(s) {
			w.t.Fatalf("no %q among %q", s, w.tt.Texts())
		}
	}
}

// noPassword fails the walk if the root password is ever shown: as a
// text, or as the value of a control, which is how a plain text input
// shows what was typed.
func (w *walk) noPassword() {
	w.t.Helper()
	for _, s := range w.tt.Texts() {
		if strings.Contains(s, rootPassword) {
			w.t.Fatalf("the root password shows in %q", s)
		}
	}
	for _, a := range w.tt.AccessAll() {
		if strings.Contains(a.Value, rootPassword) || strings.Contains(a.Name, rootPassword) {
			w.t.Fatalf("the root password shows in %+v", a)
		}
	}
}

func TestWalk(t *testing.T) {
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		t.Run(name, func(t *testing.T) { walkThrough(t, dark) })
	}
}

func walkThrough(t *testing.T, dark bool) {
	w := newWalk(t, dark)
	w.want("No servers yet", "v0.1.0-test · darwin/arm64")

	// The server step: a probe that fails, then one that works.
	w.click("Add server")
	w.typeInto("Name", "web-1")
	w.typeInto("Host", "203.0.113.7")
	w.typeInto("Provider", "Example Cloud")
	w.typeInto("Root password", rootPassword)
	w.b.ProbeErr = errors.New("dial tcp 203.0.113.7:22: i/o timeout")
	w.click("Connect")
	w.flush()
	w.want("i/o timeout")
	w.b.ProbeErr = nil
	w.click("Connect")
	w.flush()

	// The fingerprint step.
	w.want(w.b.Fingerprint)
	w.click("Trust this server")

	// The plan step: expires on December 15, monthly, 5.99 USD.
	w.click("12/31/2026")
	w.click("December 15, 2026")
	w.typeInto("Price", "5.99")
	w.click("Continue")

	// Setup fails at the third step, then the retry finishes it.
	w.b.ProvisionFailAt = 3
	w.flush()
	w.want("user: failed", "Retry")
	w.click("Retry")
	w.flush()
	w.want("Server ready")
	if n := w.b.Count("AddServer"); n != 1 {
		t.Fatalf("AddServer ran %d times", n)
	}

	// The server page: the readouts show a streamed sample; range tabs.
	w.b.SetHistory("srv-1", monitoring.Sample{Time: now.Add(-30 * time.Second), CPU: 12, MemUsed: 1 << 30, MemTotal: 4 << 30})
	w.b.Push("srv-1", monitoring.Sample{Time: now, CPU: 42, MemUsed: 3 << 30, MemTotal: 4 << 30, NetRx: 4096, NetTx: 1024,
		Disks: []monitoring.Disk{{Mount: "/", Used: 5 << 30, Total: 20 << 30}}})
	w.click("Open server")
	w.flush()
	w.want("web-1", "CPU 42%", "Memory 75% · 3.0 GB / 4.0 GB", "Network in 4.0 KB/s · out 1.0 KB/s",
		"Expires 2026-12-15 · monthly · 5.99 USD", "14 days left")
	w.click("7d")
	w.flush()
	w.want("CPU 42%")

	// The renewals page: the plan and this month's spend, then renewing.
	w.click("Renewals")
	w.want("web-1", "14 days left", "This month: 5.99 USD")
	w.click("Mark web-1 renewed")
	w.flush()
	w.want("web-1 renewed until 2027-01-15", "45 days left")
	if p, _ := w.b.Plan("srv-1"); p.ExpiresAt != (renewal.Date{Year: 2027, Month: 1, Day: 15}) {
		t.Fatalf("backend plan %+v", p)
	}

	// Settings: a webhook, tested, saved.
	w.click("Settings")
	w.click("Add channel")
	w.typeInto("Webhook URL", "https://hooks.example.com/vpsgogo")
	w.click("Send test message")
	w.flush()
	w.want("Test message sent")
	w.click("Save")
	w.flush()
	w.want("Settings saved")
	if s := w.b.SavedSettings(); len(s.Channels) != 1 || s.Channels[0].Type != "webhook" {
		t.Fatalf("saved %+v", s)
	}

	// Back to the dashboard: the card shows days left and the latest figures.
	w.click("Dashboard")
	w.flush()
	w.want("web-1", "online", "45 days left", "CPU 12%")
}

func TestLeavingTheServerPageCancelsItsStream(t *testing.T) {
	w := &walk{t: t, b: fake.New(), r: &async.Manual{}}
	w.b.Seed(fleet.Server{ID: "srv-1", Name: "web-1", Host: "203.0.113.7", Port: 22})
	a := New(Options{Backend: w.b, Runner: w.r, Now: func() time.Time { return now }})
	w.tt = ui.NewTester(a.View, 1280, 1000)
	w.flush()
	w.click("Open web-1")
	w.click("Dashboard") // before the stream's task runs
	w.flush()
	calls := w.b.Calls()
	i := slices.IndexFunc(calls, func(c fake.Call) bool { return c.Method == "Stream" })
	if i < 0 || calls[i].Args[1] != true {
		t.Fatalf("Stream ran with a live context after the page was left: %+v", calls)
	}
}

// failServers fails Servers while err is set.
type failServers struct {
	services.Backend
	err error
}

func (f *failServers) Servers(ctx context.Context) ([]fleet.Server, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.Backend.Servers(ctx)
}

func TestLoadErrorOffersRetry(t *testing.T) {
	w := &walk{t: t, b: fake.New(), r: &async.Manual{}}
	failing := &failServers{Backend: w.b, err: errors.New("sqlite: database is locked")}
	a := New(Options{Backend: failing, Runner: w.r, Now: func() time.Time { return now }})
	w.tt = ui.NewTester(a.View, 1280, 1000)
	w.want("Loading…")
	w.flush()
	w.want("database is locked")
	failing.err = nil
	w.click("Try again")
	w.flush()
	w.want("No servers yet")
}

func TestLeavingTheWizardDropsItsPendingResults(t *testing.T) {
	w := newWalk(t, false)
	// A probe still running as the user leaves.
	w.click("Add server")
	w.typeInto("Host", "203.0.113.7")
	w.click("Connect")
	w.click("Dashboard")
	w.flush()
	w.click("Add server")
	w.want("Connect")
	if w.tt.HasText(w.b.Fingerprint) {
		t.Fatal("the wizard opened on the last probe's fingerprint")
	}

	// A setup still running as the user leaves: the next server is added
	// on its own, not as the last one.
	w.typeInto("Host", "203.0.113.7")
	w.click("Connect")
	w.flush()
	w.click("Trust this server")
	w.click("Skip")
	w.click("Dashboard")
	w.flush()
	w.click("Add server")
	w.want("Connect")
	w.typeInto("Host", "203.0.113.8")
	w.click("Connect")
	w.flush()
	w.click("Trust this server")
	w.click("Skip")
	w.flush()
	w.want("Server ready")
	if n := w.b.Count("AddServer"); n != 2 {
		t.Fatalf("AddServer ran %d times for two servers", n)
	}
}

func TestSettingsOpenedWhileLoadingShowLoadedValues(t *testing.T) {
	w := &walk{t: t, b: fake.New(), r: &async.Manual{}}
	w.b.SaveSettings(context.Background(), services.Settings{Timezone: "Asia/Tokyo", AnthropicKey: "sk-ant-x"})
	a := New(Options{Backend: w.b, Runner: w.r, Now: func() time.Time { return now }})
	w.tt = ui.NewTester(a.View, 1280, 1000)
	w.click("Settings")
	w.flush()
	w.want("Anthropic API key: set")
	w.click("Save")
	w.flush()
	if s := w.b.SavedSettings(); s.Timezone != "Asia/Tokyo" || s.AnthropicKey != "sk-ant-x" {
		t.Fatalf("Save wrote %+v", s)
	}
}
