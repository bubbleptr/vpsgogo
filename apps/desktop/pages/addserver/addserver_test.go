package addserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services/fake"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

const password = "hunter2-root-secret"

type rig struct {
	t      *testing.T
	b      *fake.Backend
	r      *async.Manual
	s      *store.Store
	p      *Page
	tt     *ui.Tester
	opened string
}

func newRig(t *testing.T) *rig {
	g := &rig{t: t, b: fake.New(), r: &async.Manual{}, s: &store.Store{Loaded: true}}
	g.p = New(Options{
		Backend: g.b, Runner: g.r, Store: g.s,
		Today:  func() renewal.Date { return renewal.Date{Year: 2026, Month: 12, Day: 1} },
		OnOpen: func(id string) { g.opened = id },
	})
	g.tt = ui.NewTester(g.p.View, 1000, 900)
	return g
}

func (g *rig) click(s string) {
	g.t.Helper()
	if err := g.tt.Click(s); err != nil {
		g.t.Fatalf("click %q: %v; texts %q", s, err, g.tt.Texts())
	}
	g.noPassword()
}

func (g *rig) typeInto(label, text string) {
	g.t.Helper()
	g.click(label)
	g.tt.Type(text)
	g.noPassword()
}

func (g *rig) flush() {
	g.r.Flush()
	g.tt.Frame()
	g.noPassword()
}

func (g *rig) want(texts ...string) {
	g.t.Helper()
	for _, s := range texts {
		if !g.tt.HasText(s) {
			g.t.Fatalf("no %q among %q", s, g.tt.Texts())
		}
	}
}

func (g *rig) noPassword() {
	g.t.Helper()
	for _, s := range g.tt.Texts() {
		if strings.Contains(s, password) {
			g.t.Fatalf("the root password shows in %q", s)
		}
	}
	for _, a := range g.tt.AccessAll() {
		if strings.Contains(a.Value, password) {
			g.t.Fatalf("the root password reaches assistive technology in %+v", a)
		}
	}
}

// serverStep fills the first step and connects.
func (g *rig) serverStep() {
	g.typeInto("Name", "web-1")
	g.typeInto("Host", "203.0.113.7")
	g.typeInto("Root password", password)
	g.click("Connect")
	g.flush()
}

func TestRequiresHostAndPortInRange(t *testing.T) {
	g := newRig(t)
	g.click("Connect")
	g.want("Enter the server's IP address or host name.")
	g.typeInto("Host", "203.0.113.7")
	g.click("Port")
	g.tt.Key(ui.Super, ui.KeyA)
	g.tt.Type("70000")
	g.click("Connect")
	g.want("The port must be between 1 and 65535.")
	if n := g.r.Pending(); n != 0 {
		t.Fatalf("%d tasks started for invalid input", n)
	}
}

func TestProbeErrorShowsAndRetries(t *testing.T) {
	g := newRig(t)
	g.b.ProbeErr = errors.New("dial tcp 203.0.113.7:22: connection refused")
	g.serverStep()
	g.want("connection refused")
	g.b.ProbeErr = nil
	g.click("Connect")
	g.flush()
	g.want(g.b.Fingerprint, "Trust this server")
}

func TestBackFromFingerprintKeepsEntries(t *testing.T) {
	g := newRig(t)
	g.serverStep()
	g.click("Back")
	g.want("Connect")
	if g.p.host != "203.0.113.7" {
		t.Fatalf("host %q after Back", g.p.host)
	}
}

func TestInvalidPlanShowsCoreError(t *testing.T) {
	g := newRig(t)
	g.serverStep()
	g.click("Trust this server")
	g.typeInto("Price", "five")
	g.click("Continue")
	g.want(`renewal: price "five" is not a number such as 5.99`)
	if g.r.Pending() != 0 || g.b.Count("AddServer") != 0 {
		t.Fatal("an invalid plan went on to setup")
	}
}

func TestSkipPlanProvisionsWithoutOne(t *testing.T) {
	g := newRig(t)
	g.serverStep()
	g.click("Trust this server")
	g.click("Skip")
	g.flush()
	g.want("Server ready")
	if g.b.Count("SavePlan") != 0 {
		t.Fatal("saved a plan the user skipped")
	}
	if len(g.s.Servers) != 1 || len(g.s.Plans) != 0 {
		t.Fatalf("store %+v", g.s)
	}
	if g.p.password != "" {
		t.Fatal("the password stays in state after setup")
	}
	g.click("Open server")
	if g.opened != "srv-1" {
		t.Fatalf("opened %q", g.opened)
	}
}

func TestRetryProvisionsTheSameServer(t *testing.T) {
	g := newRig(t)
	g.b.ProvisionFailAt = 3
	g.serverStep()
	g.click("Trust this server")
	g.typeInto("Price", "5.99")
	g.click("Continue")
	g.flush()
	g.want("detect: done", "packages: done", "user: failed", "fake failure", "Retry")
	if g.tt.HasText("Server ready") {
		t.Fatal("ready after a failure")
	}
	g.click("Retry")
	g.flush()
	g.want("agent: done", "Server ready")
	if g.tt.HasText("user: failed") {
		t.Fatal("the failed run's progress stays after a retry")
	}
	if n := g.b.Count("AddServer"); n != 1 {
		t.Fatalf("AddServer ran %d times", n)
	}
	if n := g.b.Count("SavePlan"); n != 1 {
		t.Fatalf("SavePlan ran %d times", n)
	}
	var ids []any
	for _, c := range g.b.Calls() {
		if c.Method == "Provision" {
			ids = append(ids, c.Args[0])
		}
	}
	if len(ids) != 2 || ids[0] != "srv-1" || ids[1] != "srv-1" {
		t.Fatalf("Provision ids %v", ids)
	}
	if p, ok := g.b.Plan("srv-1"); !ok || p.Price != "5.99" || p.Currency != "USD" {
		t.Fatalf("plan %+v %v", p, ok)
	}
	if _, ok := g.s.Plan("srv-1"); !ok {
		t.Fatal("the store has no plan")
	}
}

func TestTargetIsLockedWhileProbing(t *testing.T) {
	g := newRig(t)
	g.typeInto("Host", "203.0.113.7")
	g.click("Connect")
	for _, label := range []string{"Host", "Port"} {
		if a, ok := g.tt.AccessByRole(ui.RoleTextField, label); !ok || !a.Disabled {
			t.Fatalf("%s is editable while probing: %+v", label, a)
		}
	}
}

func TestProbeOfAnOldTargetIsDiscarded(t *testing.T) {
	g := newRig(t)
	g.typeInto("Host", "203.0.113.7")
	g.click("Connect")
	g.p.host = "198.51.100.9" // edited before the probe of .7 returned
	g.flush()
	if g.tt.HasText(g.b.Fingerprint) || g.p.step != stepServer {
		t.Fatal("took the fingerprint of another host")
	}
	g.want("Connect")
}

func TestSetupAddsTheProbedTarget(t *testing.T) {
	g := newRig(t)
	g.serverStep()
	g.click("Trust this server")
	g.p.host, g.p.port = "198.51.100.9", 2222 // changed after the probe
	g.click("Skip")
	g.flush()
	calls := g.b.Calls()
	for _, c := range calls {
		if c.Method == "AddServer" {
			n := c.Args[0].(fleet.NewServer)
			if n.Host != "203.0.113.7" || n.Port != 22 || c.Args[1] != g.b.Fingerprint {
				t.Fatalf("added %+v with %v", n, c.Args[1])
			}
			return
		}
	}
	t.Fatalf("no AddServer in %+v", calls)
}
