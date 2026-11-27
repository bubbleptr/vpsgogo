package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/provisioning"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal/store"
	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
	"github.com/bubbleptr/vpsgogo/packages/core/vault"
	"golang.org/x/crypto/ssh"
)

// DBName is the SQLite file in the data directory that holds the fleet and
// the renewal plans.
const DBName = "vpsgogo.db"

// LiveOptions configure OpenLive.
type LiveOptions struct {
	// DataDir holds DBName and settings.json. No secrets go there.
	DataDir string
	// Vault keeps SSH keys, channels and the Anthropic key: Keychain in the
	// app, vault.NewMemory in tests.
	Vault vault.Vault
	// AgentBinary returns gogo-agent for a Linux arch; the app passes
	// embed.Binary.
	AgentBinary func(arch string) ([]byte, error)
	// Dial replaces how provisioning and sync connect; only tests set it.
	Dial func(ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod) (provisioning.Session, error)
}

// Live is the Backend of the app: packages/core over SQLite, the vault and
// SSH.
type Live struct {
	repo        *fleet.SQLite
	fleet       *fleet.Service
	plans       *store.SQLite
	vault       vault.Vault
	dir         string
	agentBinary func(arch string) ([]byte, error)
	dial        func(ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod) (provisioning.Session, error)
	// systemZone names the computer's time zone; tests replace it.
	systemZone func() string

	mu sync.Mutex // settings.json
}

var _ Backend = (*Live)(nil)

// OpenLive opens (and creates if needed) the app's data in o.DataDir.
func OpenLive(ctx context.Context, o LiveOptions) (*Live, error) {
	if o.DataDir == "" || o.Vault == nil || o.AgentBinary == nil {
		return nil, errors.New("services: OpenLive needs a data directory, a vault and the agent binaries")
	}
	path := filepath.Join(o.DataDir, DBName)
	repo, err := fleet.OpenSQLite(ctx, path)
	if err != nil {
		return nil, err
	}
	plans, err := store.OpenSQLite(ctx, path)
	if err != nil {
		repo.Close()
		return nil, err
	}
	return &Live{
		repo:        repo,
		fleet:       fleet.NewService(repo, o.Vault),
		plans:       plans,
		vault:       o.Vault,
		dir:         o.DataDir,
		agentBinary: o.AgentBinary,
		dial:        o.Dial,
		systemZone:  systemZone,
	}, nil
}

// Close closes the database.
func (l *Live) Close() error { return errors.Join(l.plans.Close(), l.repo.Close()) }

// SyncError means a change was stored but did not reach some servers'
// agents. The UI shows it; saving again retries the sync.
type SyncError struct{ Err error }

func (e *SyncError) Error() string { return "saved, but not synced to the server: " + e.Err.Error() }
func (e *SyncError) Unwrap() error { return e.Err }

func (l *Live) Servers(ctx context.Context) ([]fleet.Server, error) { return l.fleet.List(ctx) }

func (l *Live) Probe(ctx context.Context, host string, port int) (string, error) {
	return sshx.Probe(ctx, host, port)
}

func (l *Live) AddServer(ctx context.Context, n fleet.NewServer, fingerprint string) (fleet.Server, error) {
	return l.fleet.Add(ctx, n, fingerprint)
}

func (l *Live) Provision(ctx context.Context, id, rootPassword string, progress func(provisioning.StepResult)) error {
	srv, err := l.fleet.Get(ctx, id)
	if err != nil {
		return err
	}
	config, err := l.agentConfig(ctx, srv)
	if err != nil {
		return err
	}
	notifyJSON, err := l.notifyConfig()
	if err != nil {
		return err
	}
	p := l.provisioner()
	p.Progress = progress
	p.Agent = &provisioning.AgentInstall{Binary: l.agentBinary, Config: config, Notify: notifyJSON}
	return p.Run(ctx, id, rootPassword)
}

func (l *Live) RemoveServer(ctx context.Context, id string) error {
	if err := l.fleet.Delete(ctx, id); err != nil {
		return err
	}
	return l.plans.Delete(ctx, id)
}

func (l *Live) provisioner() *provisioning.Provisioner {
	return &provisioning.Provisioner{Fleet: l.fleet, Dial: l.dial}
}

// History runs `gogo-agent query` on the server as gogo.
func (l *Live) History(ctx context.Context, id string, since time.Time) ([]monitoring.Sample, error) {
	c, err := l.connect(ctx, id)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	arg := "0"
	if !since.IsZero() {
		arg = since.UTC().Format(time.RFC3339Nano) // the agent returns samples strictly after it
	}
	res, err := c.Exec(ctx, "gogo-agent query --since "+arg+" --json")
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("services: gogo-agent query exited %d: %s", res.Code, strings.TrimSpace(res.Stderr))
	}
	var samples []monitoring.Sample
	if err := json.Unmarshal([]byte(res.Stdout), &samples); err != nil {
		return nil, fmt.Errorf("services: gogo-agent query: %w", err)
	}
	return samples, nil
}

// Stream runs `gogo-agent stream` on the server as gogo. f is called on the
// SSH session's goroutine, never after Stream returns.
func (l *Live) Stream(ctx context.Context, id string, f func(monitoring.Sample)) error {
	c, err := l.connect(ctx, id)
	if err != nil {
		return err
	}
	defer c.Close()
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	// Closing the connection, not only the session, returns at once even
	// when the server has silently gone away.
	defer context.AfterFunc(sctx, func() { c.Close() })()
	w := &sampleWriter{f: f, stop: stop}
	err = c.ExecStream(sctx, "gogo-agent stream --json", w)
	switch {
	case ctx.Err() != nil:
		return nil
	case w.err != nil: // ExecStream has returned, so w is done
		return w.err
	case err != nil:
		return err
	default:
		return errors.New("services: gogo-agent stream ended")
	}
}

// sampleWriter turns `gogo-agent stream --json` output, one sample a line,
// into calls of f. On a line that is not a sample it records the error and
// stops the stream.
type sampleWriter struct {
	f    func(monitoring.Sample)
	stop func()
	buf  []byte
	err  error
}

func (w *sampleWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for w.err == nil {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(w.buf[:i])
		w.buf = w.buf[i+1:]
		if len(line) == 0 {
			continue
		}
		var s monitoring.Sample
		if err := json.Unmarshal(line, &s); err != nil {
			w.err = fmt.Errorf("services: gogo-agent stream: %w", err)
			w.stop()
			break
		}
		w.f(s)
	}
	return len(p), nil
}

// connect logs in to a provisioned server as gogo with its key.
func (l *Live) connect(ctx context.Context, id string) (*sshx.Client, error) {
	srv, err := l.fleet.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if srv.User != provisioning.OpsUser {
		return nil, errors.New("services: the server is not provisioned yet")
	}
	pem, err := l.fleet.PrivateKey(id)
	if err != nil {
		return nil, err
	}
	auth, err := sshx.PrivateKey(pem)
	if err != nil {
		return nil, err
	}
	return sshx.Dial(ctx, sshx.Target{Host: srv.Host, Port: srv.Port, User: provisioning.OpsUser}, auth, srv.HostKeyFP)
}

func (l *Live) Plans(ctx context.Context) ([]renewal.Subscription, error) { return l.plans.List(ctx) }

func (l *Live) SavePlan(ctx context.Context, s renewal.Subscription) error {
	if err := l.plans.Put(ctx, s); err != nil {
		return err
	}
	return l.sync(ctx, []string{s.ServerID}, false)
}

func (l *Live) DeletePlan(ctx context.Context, id string) error {
	if err := l.plans.Delete(ctx, id); err != nil {
		return err
	}
	return l.sync(ctx, []string{id}, false)
}

func (l *Live) Renew(ctx context.Context, id string) (renewal.Subscription, error) {
	s, err := l.plans.Renew(ctx, id)
	if err != nil {
		return s, err
	}
	return s, l.sync(ctx, []string{id}, false)
}

func (l *Live) TestChannel(ctx context.Context, ch notify.ChannelConfig) error {
	chans, err := notify.Build(notify.Config{Channels: []notify.ChannelConfig{ch}})
	if err != nil {
		return err
	}
	return chans[0].Send(ctx, notify.Message{
		Server: "VPSGoGo",
		Title:  "Test message",
		Text:   "This channel works: alerts and renewal reminders from your servers will arrive here.",
		Time:   time.Now(),
	})
}

// sync writes agent.json, and with notify notify.json, to the agents of the
// given servers that are provisioned, skipping the others. It returns a
// *SyncError naming the servers it could not reach.
func (l *Live) sync(ctx context.Context, ids []string, withNotify bool) error {
	var errs []error
	for _, id := range ids {
		srv, err := l.fleet.Get(ctx, id)
		switch {
		case errors.Is(err, fleet.ErrNotFound):
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		case srv.User != provisioning.OpsUser:
			continue
		}
		if err := l.syncServer(ctx, srv, withNotify); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", srv.Name, err))
		}
	}
	if len(errs) > 0 {
		return &SyncError{Err: errors.Join(errs...)}
	}
	return nil
}

func (l *Live) syncServer(ctx context.Context, srv fleet.Server, withNotify bool) error {
	p := l.provisioner()
	if withNotify {
		n, err := l.notifyConfig()
		if err != nil {
			return err
		}
		if _, err := p.SyncAgentNotify(ctx, srv.ID, n); err != nil {
			return err
		}
	}
	config, err := l.agentConfig(ctx, srv)
	if err != nil {
		return err
	}
	_, err = p.SyncAgentConfig(ctx, srv.ID, config)
	return err
}
