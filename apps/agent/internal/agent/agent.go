// Package agent is gogo-agent's daemon and the client of its socket: it takes
// a sample every interval, keeps them, checks the alert rules, sends notices,
// and answers query and stream requests on a unix socket. It listens on no
// network port. See docs/SPEC.md §4.2.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// Options configures Run. Every function is a probe of the machine, so tests
// can stand in for /proc, systemd and the network.
type Options struct {
	Name     string // the server's display name, in notices
	Rules    monitoring.Rules
	Interval time.Duration

	Source     func(now time.Time) (monitoring.Sample, bool, error) // monitoring.Collector.Collect
	UnitActive func(ctx context.Context, unit string) bool
	Probe      func(ctx context.Context, url string) error
	Notify     func(ctx context.Context, m notify.Message) error

	// Heartbeat, when set, is called every HeartbeatEvery (default 1m) so an
	// outside service can tell the server went silent.
	Heartbeat      func(ctx context.Context) error
	HeartbeatEvery time.Duration

	SocketPath string
	StatePath  string        // where samples are kept across restarts
	SaveEvery  time.Duration // default 5m; samples are also saved on stop

	// Renewal, when set, is the server's plan: a reminder goes out on each
	// remind day once it is RemindHour o'clock in Location (default UTC).
	// RenewalStatePath keeps what was sent across restarts.
	Renewal          *renewal.Subscription
	Location         *time.Location
	RemindHour       int
	RenewalEvery     time.Duration // how often to check; default 1h
	RenewalStatePath string

	Now func() time.Time // default time.Now
	Log *log.Logger      // default: stderr, which systemd sends to the journal
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = log.New(os.Stderr, "", 0)
	}
	if o.SaveEvery == 0 {
		o.SaveEvery = 5 * time.Minute
	}
	if o.HeartbeatEvery == 0 {
		o.HeartbeatEvery = time.Minute
	}
	if o.RenewalEvery == 0 {
		o.RenewalEvery = time.Hour
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Run serves until ctx ends, then saves the samples and returns.
func Run(ctx context.Context, o Options) error {
	o.defaults()
	store := monitoring.NewStore()
	if f, err := os.Open(o.StatePath); err == nil {
		if err := store.Load(f); err != nil {
			o.Log.Printf("ignoring saved samples: %v", err)
		}
		f.Close()
	}

	ln, err := listen(o.SocketPath)
	if err != nil {
		return err
	}
	hub := &hub{subs: map[chan monitoring.Sample]struct{}{}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); serve(ctx, ln, store, hub, o.Log) }()
	if o.Heartbeat != nil {
		wg.Add(1)
		go func() { defer wg.Done(); heartbeat(ctx, o) }()
	}
	if o.Renewal != nil {
		wg.Add(1)
		go func() { defer wg.Done(); newRenewals(o).run(ctx) }()
	}

	// Checks and notices run apart from sampling: a hanging probe or a slow
	// channel delays alerts, never samples. While the checker is busy, newer
	// samples replace the one waiting for it.
	pending := make(chan monitoring.Sample, 1)
	wg.Add(1)
	go func() { defer wg.Done(); check(ctx, o, pending) }()

	tick := time.NewTicker(o.Interval)
	defer tick.Stop()
	lastSave := time.Now()
	for {
		select {
		case <-ctx.Done():
			ln.Close()
			wg.Wait()
			os.Remove(o.SocketPath)
			return save(store, o.StatePath)
		case now := <-tick.C:
			s, ok, err := o.Source(now)
			if err != nil {
				o.Log.Printf("collect: %v", err)
				continue
			}
			if !ok {
				continue
			}
			store.Add(s)
			hub.publish(s)
			select {
			case <-pending: // drop the sample the checker did not get to
			default:
			}
			pending <- s
			if now.Sub(lastSave) >= o.SaveEvery {
				if err := save(store, o.StatePath); err != nil {
					o.Log.Printf("save: %v", err)
				}
				lastSave = now
			}
		}
	}
}

// check evaluates the alert rules on each sample it receives until ctx ends.
func check(ctx context.Context, o Options, samples <-chan monitoring.Sample) {
	engine := monitoring.NewEngine(o.Rules)
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-samples:
			for _, ev := range engine.Observe(observe(ctx, o, s)) {
				send(ctx, o, ev)
			}
		}
	}
}

// observe runs the unit and probe checks the rules ask for.
func observe(ctx context.Context, o Options, s monitoring.Sample) monitoring.Observation {
	obs := monitoring.Observation{Sample: s, Units: map[string]bool{}, Probes: map[string]error{}}
	for _, u := range o.Rules.Units {
		obs.Units[u] = o.UnitActive(ctx, u)
	}
	for _, url := range o.Rules.Probes {
		obs.Probes[url] = o.Probe(ctx, url)
	}
	return obs
}

func send(ctx context.Context, o Options, ev monitoring.Event) {
	title := "Alert: " + ev.Text
	if !ev.Firing {
		title = "Recovered: " + ev.Text
	}
	m := notify.Message{Server: o.Name, Title: title, Text: ev.Text, Time: ev.Time}
	o.Log.Printf("%s (%s)", title, ev.Key)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := o.Notify(ctx, m); err != nil {
		o.Log.Printf("notify: %v", err)
	}
}

func heartbeat(ctx context.Context, o Options) {
	t := time.NewTicker(o.HeartbeatEvery)
	defer t.Stop()
	for {
		if err := o.Heartbeat(ctx); err != nil && ctx.Err() == nil {
			o.Log.Printf("heartbeat: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// save writes the samples atomically.
func save(store *monitoring.Store, path string) error {
	return writeAtomic(path, store.Save)
}

// writeAtomic writes a file through a temporary one: a crash never leaves a
// torn file.
func writeAtomic(path string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// listen opens the socket for the agent's user and group only: the gogo user
// reads samples as a member of the gogo-agent group.
func listen(path string) (net.Listener, error) {
	os.Remove(path) // a socket left by a crash
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// request is one line a client sends.
type request struct {
	Op    string    `json:"op"` // "query" or "stream"
	Since time.Time `json:"since"`
}

// reply answers a query in one line; a stream answers with one sample a line.
type reply struct {
	Samples []monitoring.Sample `json:"samples,omitempty"`
	Error   string              `json:"error,omitempty"`
}

func serve(ctx context.Context, ln net.Listener, store *monitoring.Store, h *hub, lg *log.Logger) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			if err := handle(ctx, conn, store, h); err != nil && ctx.Err() == nil {
				lg.Printf("socket: %v", err)
			}
		}()
	}
}

func handle(ctx context.Context, conn net.Conn, store *monitoring.Store, h *hub) error {
	line, err := bufio.NewReader(io.LimitReader(conn, 4096)).ReadBytes('\n')
	if err != nil {
		return err
	}
	var req request
	enc := json.NewEncoder(conn)
	if err := json.Unmarshal(line, &req); err != nil {
		return enc.Encode(reply{Error: "bad request"})
	}
	switch req.Op {
	case "query":
		return enc.Encode(reply{Samples: store.Since(req.Since)})
	case "stream":
		ch := h.subscribe()
		defer h.unsubscribe(ch)
		for {
			select {
			case <-ctx.Done(): // the agent is stopping: no more samples come
				return nil
			case s := <-ch:
				if err := enc.Encode(s); err != nil {
					return nil // the client went away
				}
			}
		}
	default:
		return enc.Encode(reply{Error: "unknown op " + req.Op})
	}
}

// hub fans new samples out to streaming clients. A client too slow to keep
// up misses samples rather than holding up the agent.
type hub struct {
	mu   sync.Mutex
	subs map[chan monitoring.Sample]struct{}
}

func (h *hub) subscribe() chan monitoring.Sample {
	ch := make(chan monitoring.Sample, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan monitoring.Sample) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *hub) publish(s monitoring.Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- s:
		default:
		}
	}
}

// Query asks the agent at socket for the samples after since.
func Query(ctx context.Context, socket string, since time.Time) ([]monitoring.Sample, error) {
	conn, err := dial(ctx, socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Op: "query", Since: since}); err != nil {
		return nil, err
	}
	var r reply
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return nil, fmt.Errorf("agent: read reply: %w", err)
	}
	if r.Error != "" {
		return nil, errors.New("agent: " + r.Error)
	}
	return r.Samples, nil
}

// errStop ends a Stream without an error when f returns it.
var errStop = errors.New("stop")

// Stream calls f with each new sample until ctx ends or f returns an error.
func Stream(ctx context.Context, socket string, f func(monitoring.Sample) error) error {
	conn, err := dial(ctx, socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(request{Op: "stream"}); err != nil {
		return err
	}
	dec := json.NewDecoder(conn)
	for {
		var s monitoring.Sample
		if err := dec.Decode(&s); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("agent: stream: %w", err)
		}
		if err := f(s); err != nil {
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
	}
}

func dial(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("agent: is gogo-agent running? %w", err)
	}
	return conn, nil
}
