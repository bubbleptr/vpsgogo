package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
)

// harness runs an agent with fake probes of the machine in a temp dir.
type harness struct {
	t      *testing.T
	dir    string
	opts   Options
	mu     sync.Mutex
	active map[string]bool
	sent   []notify.Message
	cancel context.CancelFunc
	done   chan error
}

func newHarness(t *testing.T) *harness {
	dir, err := os.MkdirTemp("", "agent") // short: unix socket paths are limited to ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{t: t, dir: dir, active: map[string]bool{}}
	n := 0
	h.opts = Options{
		Name:     "test-vps",
		Rules:    monitoring.DefaultRules(),
		Interval: 10 * time.Millisecond,
		Source: func(now time.Time) (monitoring.Sample, bool, error) {
			n++
			return monitoring.Sample{Time: now, CPU: float64(n % 50), MemTotal: 100, MemUsed: 10}, n > 1, nil
		},
		UnitActive: func(_ context.Context, unit string) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.active[unit]
		},
		Probe: func(context.Context, string) error { return nil },
		Notify: func(_ context.Context, m notify.Message) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.sent = append(h.sent, m)
			return nil
		},
		SocketPath: filepath.Join(dir, "agent.sock"),
		StatePath:  filepath.Join(dir, "samples.json"),
		SaveEvery:  time.Hour,
	}
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	go func() { h.done <- Run(ctx, h.opts) }()
	h.t.Cleanup(h.stop)
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	if err := <-h.done; err != nil {
		h.t.Errorf("Run: %v", err)
	}
	h.cancel = nil
}

func (h *harness) messages() []notify.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]notify.Message(nil), h.sent...)
}

// eventually polls cond for a second.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestQueryReturnsCollectedSamples(t *testing.T) {
	h := newHarness(t)
	h.start()
	start := time.Now()
	var got []monitoring.Sample
	eventually(t, "two samples", func() bool {
		var err error
		got, err = Query(context.Background(), h.opts.SocketPath, start.Add(-time.Second))
		return err == nil && len(got) >= 2
	})
	for i := 1; i < len(got); i++ {
		if !got[i].Time.After(got[i-1].Time) {
			t.Fatalf("samples out of order: %v then %v", got[i-1].Time, got[i].Time)
		}
	}
	// The baseline reading (ok=false) is not a sample.
	if got[0].CPU == 1 {
		t.Errorf("the baseline reading was stored: %+v", got[0])
	}
	later, err := Query(context.Background(), h.opts.SocketPath, got[len(got)-1].Time)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range later {
		if !s.Time.After(got[len(got)-1].Time) {
			t.Errorf("since is not exclusive: got %v", s.Time)
		}
	}
}

func TestStreamDeliversNewSamples(t *testing.T) {
	h := newHarness(t)
	h.start()
	eventually(t, "socket", func() bool { _, err := os.Stat(h.opts.SocketPath); return err == nil })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got []monitoring.Sample
	err := Stream(ctx, h.opts.SocketPath, func(s monitoring.Sample) error {
		got = append(got, s)
		if len(got) == 3 {
			return errStop
		}
		return nil
	})
	if err != nil || len(got) != 3 {
		t.Fatalf("Stream: %v after %d samples", err, len(got))
	}
}

func TestSocketIsForOwnerAndGroupOnly(t *testing.T) {
	h := newHarness(t)
	h.start()
	eventually(t, "socket", func() bool { _, err := os.Stat(h.opts.SocketPath); return err == nil })
	fi, err := os.Stat(h.opts.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket mode %o, want 660", perm)
	}
}

func TestWatchedUnitAlertsAndRecovers(t *testing.T) {
	h := newHarness(t)
	h.opts.Rules.Units = []string{"caddy.service", "ssh.service"}
	h.active["ssh.service"] = true
	h.start()
	eventually(t, "an alert", func() bool { return len(h.messages()) > 0 })
	m := h.messages()[0]
	if m.Server != "test-vps" || !strings.Contains(m.Title, "caddy.service") || !strings.HasPrefix(m.Title, "Alert") {
		t.Errorf("first message %+v", m)
	}
	for _, m := range h.messages() {
		if strings.Contains(m.Title+m.Text, "ssh.service") {
			t.Errorf("an active unit alerted: %+v", m)
		}
	}
	h.mu.Lock()
	h.active["caddy.service"] = true
	h.mu.Unlock()
	eventually(t, "recovery", func() bool {
		ms := h.messages()
		return strings.HasPrefix(ms[len(ms)-1].Title, "Recovered")
	})
	if n := len(h.messages()); n != 2 {
		t.Errorf("%d messages, want alert then recovery: %+v", n, h.messages())
	}
}

func TestSamplesSurviveRestart(t *testing.T) {
	h := newHarness(t)
	h.start()
	var before []monitoring.Sample
	eventually(t, "samples", func() bool {
		before, _ = Query(context.Background(), h.opts.SocketPath, time.Time{})
		return len(before) >= 3
	})
	h.stop()
	if _, err := os.Stat(h.opts.StatePath); err != nil {
		t.Fatalf("no state saved on stop: %v", err)
	}
	h.start()
	var after []monitoring.Sample
	eventually(t, "samples after restart", func() bool {
		after, _ = Query(context.Background(), h.opts.SocketPath, time.Time{})
		return len(after) > 0
	})
	if !after[0].Time.Equal(before[0].Time) {
		t.Errorf("first sample after restart %v, want %v", after[0].Time, before[0].Time)
	}
}

func TestSlowProbeDoesNotStallSampling(t *testing.T) {
	h := newHarness(t)
	h.opts.Rules.Probes = []string{"https://slow.example"}
	release := make(chan struct{})
	h.opts.Probe = func(ctx context.Context, _ string) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	h.start()
	defer close(release)
	eventually(t, "samples while a probe hangs", func() bool {
		got, _ := Query(context.Background(), h.opts.SocketPath, time.Time{})
		return len(got) >= 5
	})
}

func TestConfigRejectsPlainHTTP(t *testing.T) {
	dir := t.TempDir()
	for name, cfg := range map[string]string{
		"probe":     `{"rules":{"probes":["http://example.com/health"]}}`,
		"heartbeat": `{"heartbeat":"http://hc-ping.com/abc"}`,
	} {
		p := filepath.Join(dir, name+".json")
		os.WriteFile(p, []byte(cfg), 0o600)
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("%s: an http:// URL was accepted", name)
		}
	}
	p := filepath.Join(dir, "ok.json")
	os.WriteFile(p, []byte(`{"name":"vps","heartbeat":"https://hc-ping.com/abc"}`), 0o600)
	if c, err := LoadConfig(p); err != nil || c.Rules.CPUPercent != 90 {
		t.Errorf("valid config: %+v %v (missing rules should take the defaults)", c, err)
	}
}
