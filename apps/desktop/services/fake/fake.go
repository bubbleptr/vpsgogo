// Package fake is a services.Backend in memory that tests script: probes
// that fail, provisioning that fails once at a step, samples pushed to a
// stream, and a record of every call.
package fake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/provisioning"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// Steps are the provisioning steps the fake reports, in order.
var Steps = []string{"detect", "packages", "user", "ssh", "firewall", "updates", "agent"}

// Call is one method call the fake received.
type Call struct {
	Method string
	Args   []any
}

// Backend is the fake. Its zero value is not usable: call New. Set its
// exported fields before the view runs, or between Flushes.
type Backend struct {
	mu sync.Mutex

	// Fingerprint is what Probe returns.
	Fingerprint string
	// ProbeErr fails every Probe while set.
	ProbeErr error
	// ProvisionFailAt makes the first Provision fail at this step, counted
	// from 1; later calls succeed. 0 never fails.
	ProvisionFailAt int
	// TestChannelErr fails every TestChannel while set.
	TestChannelErr error

	servers    []fleet.Server
	plans      map[string]renewal.Subscription
	settings   services.Settings
	history    map[string][]monitoring.Sample
	historyErr map[string]error
	stream     map[string][]monitoring.Sample
	streamErr  map[string]error
	provisions int
	calls      []Call
}

var _ services.Backend = (*Backend)(nil)

// New returns an empty fake.
func New() *Backend {
	return &Backend{
		Fingerprint: "SHA256:fake0fingerprint0for0tests0000000000000",
		plans:       map[string]renewal.Subscription{},
		history:     map[string][]monitoring.Sample{},
		historyErr:  map[string]error{},
		stream:      map[string][]monitoring.Sample{},
		streamErr:   map[string]error{},
	}
}

// Seed adds a server as if it were added and provisioned earlier.
func (b *Backend) Seed(s fleet.Server) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.servers = append(b.servers, s)
}

// SeedPlan stores a plan without recording a call.
func (b *Backend) SeedPlan(s renewal.Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.plans[s.ServerID] = s
}

// SetHistory sets the samples History returns for server id.
func (b *Backend) SetHistory(id string, samples ...monitoring.Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.history[id] = samples
}

// FailHistory fails History for server id with err, or stops failing it
// when err is nil.
func (b *Backend) FailHistory(id string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.historyErr[id] = err
}

// Push queues samples the next Stream of server id delivers.
func (b *Backend) Push(id string, samples ...monitoring.Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stream[id] = append(b.stream[id], samples...)
}

// FailStream makes the next Stream of server id return err after
// delivering what was pushed.
func (b *Backend) FailStream(id string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.streamErr[id] = err
}

// Calls returns the calls received, in order.
func (b *Backend) Calls() []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

// Count returns how many times method was called.
func (b *Backend) Count(method string) int {
	n := 0
	for _, c := range b.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Plan returns the stored plan of server id.
func (b *Backend) Plan(id string) (renewal.Subscription, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.plans[id]
	return s, ok
}

// SavedSettings returns the stored settings.
func (b *Backend) SavedSettings() services.Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// record notes a call; b.mu must be held.
func (b *Backend) record(method string, args ...any) {
	b.calls = append(b.calls, Call{method, args})
}

func (b *Backend) Servers(ctx context.Context) ([]fleet.Server, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("Servers")
	return slices.Clone(b.servers), nil
}

func (b *Backend) Probe(ctx context.Context, host string, port int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("Probe", host, port)
	if b.ProbeErr != nil {
		return "", b.ProbeErr
	}
	return b.Fingerprint, nil
}

func (b *Backend) AddServer(ctx context.Context, n fleet.NewServer, fingerprint string) (fleet.Server, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("AddServer", n, fingerprint)
	if n.Port == 0 {
		n.Port = 22
	}
	if n.Name == "" {
		n.Name = n.Host
	}
	s := fleet.Server{
		ID:   fmt.Sprintf("srv-%d", len(b.servers)+1),
		Name: n.Name, Host: n.Host, Port: n.Port, Provider: n.Provider,
		User: "root", HostKeyFP: fingerprint, CreatedAt: time.Unix(0, 0).UTC(),
	}
	b.servers = append(b.servers, s)
	return s, nil
}

// Provision reports Steps. The first call fails at ProvisionFailAt, if set.
func (b *Backend) Provision(ctx context.Context, id, rootPassword string, progress func(provisioning.StepResult)) error {
	b.mu.Lock()
	b.record("Provision", id) // never the password
	b.provisions++
	failAt := 0
	if b.provisions == 1 {
		failAt = b.ProvisionFailAt
	}
	b.mu.Unlock()
	for i, step := range Steps {
		if i+1 == failAt {
			err := fmt.Errorf("provisioning: %s: fake failure", step)
			progress(provisioning.StepResult{Step: step, Error: err.Error()})
			return err
		}
		progress(provisioning.StepResult{Step: step, Changed: true})
	}
	return nil
}

func (b *Backend) RemoveServer(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("RemoveServer", id)
	b.servers = slices.DeleteFunc(b.servers, func(s fleet.Server) bool { return s.ID == id })
	delete(b.plans, id)
	return nil
}

// History returns the samples set for id taken after since.
func (b *Backend) History(ctx context.Context, id string, since time.Time) ([]monitoring.Sample, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("History", id, since)
	if err := b.historyErr[id]; err != nil {
		return nil, err
	}
	var out []monitoring.Sample
	for _, s := range b.history[id] {
		if s.Time.After(since) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Stream delivers the samples pushed for id, then returns the error set
// with FailStream, once, or nil as if ctx ended.
func (b *Backend) Stream(ctx context.Context, id string, f func(monitoring.Sample)) error {
	b.mu.Lock()
	b.record("Stream", id)
	queued := b.stream[id]
	delete(b.stream, id)
	err := b.streamErr[id]
	delete(b.streamErr, id)
	b.mu.Unlock()
	for _, s := range queued {
		if ctx.Err() != nil {
			return nil
		}
		f(s)
	}
	return err
}

func (b *Backend) Plans(ctx context.Context) ([]renewal.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("Plans")
	out := make([]renewal.Subscription, 0, len(b.plans))
	for _, s := range b.plans {
		out = append(out, s)
	}
	slices.SortFunc(out, func(x, y renewal.Subscription) int {
		if x.ServerID < y.ServerID {
			return -1
		}
		if x.ServerID > y.ServerID {
			return 1
		}
		return 0
	})
	return out, nil
}

func (b *Backend) SavePlan(ctx context.Context, s renewal.Subscription) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("SavePlan", s)
	if err := s.Validate(); err != nil {
		return err
	}
	b.plans[s.ServerID] = s
	return nil
}

func (b *Backend) DeletePlan(ctx context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("DeletePlan", id)
	delete(b.plans, id)
	return nil
}

// Renew moves the plan on one cycle with core's Renewed.
func (b *Backend) Renew(ctx context.Context, id string) (renewal.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("Renew", id)
	s, ok := b.plans[id]
	if !ok {
		return renewal.Subscription{}, errors.New("renewal: no plan")
	}
	s, err := s.Renewed()
	if err != nil {
		return renewal.Subscription{}, err
	}
	b.plans[id] = s
	return s, nil
}

func (b *Backend) Settings(ctx context.Context) (services.Settings, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("Settings")
	s := b.settings
	s.Channels = slices.Clone(s.Channels)
	return s, nil
}

func (b *Backend) SaveSettings(ctx context.Context, s services.Settings) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("SaveSettings", s)
	s.Channels = slices.Clone(s.Channels)
	b.settings = s
	return nil
}

func (b *Backend) TestChannel(ctx context.Context, ch notify.ChannelConfig) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record("TestChannel", ch.Type)
	return b.TestChannelErr
}
