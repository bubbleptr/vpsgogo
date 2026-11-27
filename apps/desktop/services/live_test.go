package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/agentconfig"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/provisioning"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
	"github.com/bubbleptr/vpsgogo/packages/core/vault"
	"golang.org/x/crypto/ssh"
)

type dialFunc = func(context.Context, fleet.Server, string, ssh.AuthMethod) (provisioning.Session, error)

func openLive(t *testing.T, dir string, v vault.Vault, dial dialFunc) *Live {
	t.Helper()
	l, err := OpenLive(context.Background(), LiveOptions{
		DataDir:     dir,
		Vault:       v,
		AgentBinary: func(string) ([]byte, error) { return []byte("agent"), nil },
		Dial:        dial,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// noDial fails the test if anything connects to a server.
func noDial(t *testing.T) dialFunc {
	return func(_ context.Context, srv fleet.Server, user string, _ ssh.AuthMethod) (provisioning.Session, error) {
		t.Errorf("connected to %s as %s", srv.Host, user)
		return nil, errors.New("no network in this test")
	}
}

func addServer(t *testing.T, l *Live, name string) fleet.Server {
	t.Helper()
	srv, err := l.AddServer(context.Background(), fleet.NewServer{Name: name, Host: "203.0.113.7"}, "SHA256:test")
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func monthlyPlan(id string) renewal.Subscription {
	return renewal.Subscription{ServerID: id, ExpiresAt: renewal.Date{Year: 2027, Month: 1, Day: 31}, Cycle: renewal.Monthly, Price: "5.00", Currency: "USD"}
}

func TestSettingsRoundTripKeepsSecretsInTheVault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	v := vault.NewMemory()
	l := openLive(t, dir, v, noDial(t))

	got, err := l.Settings(ctx)
	if err != nil || got.RemindHour != 9 || got.Timezone != "" || len(got.Channels) != 0 || got.AnthropicKey != "" {
		t.Fatalf("defaults: %+v %v", got, err)
	}

	want := Settings{
		Timezone:     "Asia/Tokyo",
		RemindHour:   0,
		Channels:     []notify.ChannelConfig{{Type: "telegram", Token: "TG-SECRET-123", ChatID: "42"}},
		AnthropicKey: "sk-ant-SECRET-456",
	}
	if err := l.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	// A new Live on the same data reads them back.
	l.Close()
	l = openLive(t, dir, v, noDial(t))
	got, err = l.Settings(ctx)
	if err != nil || got.Timezone != "Asia/Tokyo" || got.RemindHour != 0 || got.AnthropicKey != want.AnthropicKey ||
		len(got.Channels) != 1 || !reflect.DeepEqual(got.Channels, want.Channels) {
		t.Fatalf("read back %+v %v", got, err)
	}

	for _, secret := range []string{"TG-SECRET-123", "sk-ant-SECRET-456"} {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(secret)) {
					t.Errorf("%s is on disk in %s", secret, d.Name())
				}
			}
			return nil
		})
	}
	if _, err := v.Get(channelsKey); err != nil {
		t.Errorf("channels are not in the vault: %v", err)
	}

	// Clearing them clears the vault.
	if err := l.SaveSettings(ctx, Settings{RemindHour: 9}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{channelsKey, anthropicKeyKey} {
		if _, err := v.Get(k); !errors.Is(err, vault.ErrNotFound) {
			t.Errorf("%s after clearing: %v", k, err)
		}
	}
}

func TestSaveSettingsRejectsWhatTheAgentWouldNot(t *testing.T) {
	ctx := context.Background()
	l := openLive(t, t.TempDir(), vault.NewMemory(), noDial(t))
	for name, s := range map[string]Settings{
		"hour":    {RemindHour: 24},
		"zone":    {Timezone: "Mars/Olympus", RemindHour: 9},
		"channel": {RemindHour: 9, Channels: []notify.ChannelConfig{{Type: "webhook", URL: "http://example.com/hook"}}},
	} {
		if err := l.SaveSettings(ctx, s); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
	if got, _ := l.Settings(ctx); got.RemindHour != 9 || got.Timezone != "" || len(got.Channels) != 0 {
		t.Errorf("a rejected save was stored: %+v", got)
	}
}

func TestAgentConfigCarriesPlanTimezoneAndHour(t *testing.T) {
	ctx := context.Background()
	l := openLive(t, t.TempDir(), vault.NewMemory(), noDial(t))
	l.systemZone = func() string { return "Europe/Berlin" }
	srv := addServer(t, l, "web-1")

	parse := func() agentconfig.Config {
		t.Helper()
		data, err := l.agentConfig(ctx, srv)
		if err != nil {
			t.Fatal(err)
		}
		c, err := agentconfig.Parse(data)
		if err != nil {
			t.Fatalf("the agent would reject %s: %v", data, err)
		}
		return c
	}

	c := parse()
	if c.Name != "web-1" || c.Renewal != nil || c.Timezone != "Europe/Berlin" || c.RemindHourOr9() != 9 || c.Rules.CPUPercent != monitoring.DefaultRules().CPUPercent {
		t.Errorf("no plan, default settings: %+v", c)
	}

	plan := monthlyPlan(srv.ID)
	if err := l.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := l.SaveSettings(ctx, Settings{Timezone: "Asia/Tokyo", RemindHour: 0}); err != nil {
		t.Fatal(err)
	}
	c = parse()
	if c.Renewal == nil || c.Renewal.ExpiresAt != plan.ExpiresAt || c.Renewal.Price != "5.00" || c.Timezone != "Asia/Tokyo" || c.RemindHourOr9() != 0 {
		t.Errorf("with a plan and settings: %+v", c)
	}

	if err := l.DeletePlan(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if c = parse(); c.Renewal != nil {
		t.Errorf("deleted plan still in agent.json: %+v", c.Renewal)
	}
}

func TestSystemZoneIsAnIANAName(t *testing.T) {
	z := systemZone()
	if z == "" || z == "Local" {
		t.Fatalf("systemZone() = %q", z)
	}
	if _, err := time.LoadLocation(z); err != nil {
		t.Fatalf("systemZone() = %q: %v", z, err)
	}
}

func TestSyncSkipsUnprovisionedServers(t *testing.T) {
	ctx := context.Background()
	l := openLive(t, t.TempDir(), vault.NewMemory(), noDial(t))
	srv := addServer(t, l, "fresh") // still logs in as root

	if err := l.SavePlan(ctx, monthlyPlan(srv.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Renew(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.SaveSettings(ctx, Settings{RemindHour: 8, Channels: []notify.ChannelConfig{{Type: "ntfy", URL: "https://ntfy.sh/x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := l.DeletePlan(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSyncFailureStillStoresThePlan(t *testing.T) {
	ctx := context.Background()
	dials := 0
	l := openLive(t, t.TempDir(), vault.NewMemory(), func(context.Context, fleet.Server, string, ssh.AuthMethod) (provisioning.Session, error) {
		dials++
		return nil, sshx.ErrHostKeyMismatch // final: no retries
	})
	srv := addServer(t, l, "provisioned")
	key, err := sshx.GenerateKey("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.fleet.SetPrivateKey(srv.ID, key.PrivatePEM); err != nil {
		t.Fatal(err)
	}
	if err := l.fleet.SetUser(ctx, srv.ID, provisioning.OpsUser); err != nil {
		t.Fatal(err)
	}

	err = l.SavePlan(ctx, monthlyPlan(srv.ID))
	var syncErr *SyncError
	if !errors.As(err, &syncErr) || !errors.Is(err, sshx.ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want a SyncError", err)
	}
	if dials == 0 {
		t.Error("never tried to sync")
	}
	plans, err := l.Plans(ctx)
	if err != nil || len(plans) != 1 || plans[0].ServerID != srv.ID {
		t.Fatalf("plans %+v %v", plans, err)
	}
}

func TestRenewOneTimePlan(t *testing.T) {
	ctx := context.Background()
	l := openLive(t, t.TempDir(), vault.NewMemory(), noDial(t))
	srv := addServer(t, l, "once")
	plan := monthlyPlan(srv.ID)
	plan.Cycle = renewal.Once
	if err := l.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Renew(ctx, srv.ID); !errors.Is(err, renewal.ErrOneTime) {
		t.Fatalf("err = %v, want ErrOneTime", err)
	}
}

func TestRemoveServerForgetsItsPlanAndKey(t *testing.T) {
	ctx := context.Background()
	v := vault.NewMemory()
	l := openLive(t, t.TempDir(), v, noDial(t))
	srv := addServer(t, l, "gone")
	l.fleet.SetPrivateKey(srv.ID, []byte("key"))
	if err := l.SavePlan(ctx, monthlyPlan(srv.ID)); err != nil {
		t.Fatal(err)
	}
	if err := l.RemoveServer(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	servers, _ := l.Servers(ctx)
	plans, _ := l.Plans(ctx)
	if _, err := l.fleet.PrivateKey(srv.ID); len(servers) != 0 || len(plans) != 0 || !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("servers %v plans %v key err %v", servers, plans, err)
	}
}

// Stream's output arrives in chunks that need not end at a line.
func TestSampleLinesSplitAcrossWrites(t *testing.T) {
	var got []monitoring.Sample
	w := &sampleWriter{f: func(s monitoring.Sample) { got = append(got, s) }, stop: func() {}}
	w.Write([]byte(`{"cpu":1}` + "\n" + `{"cp`))
	w.Write([]byte(`u":2}` + "\n\n"))
	if w.err != nil || len(got) != 2 || got[0].CPU != 1 || got[1].CPU != 2 {
		t.Fatalf("got %+v err %v", got, w.err)
	}
	stopped := false
	w = &sampleWriter{f: func(monitoring.Sample) {}, stop: func() { stopped = true }}
	w.Write([]byte("gogo-agent: not json\n"))
	if w.err == nil || !stopped {
		t.Fatalf("bad line: err %v stopped %v", w.err, stopped)
	}
}
