package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // the user's zone must load on any desktop OS

	"github.com/bubbleptr/vpsgogo/packages/core/agentconfig"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal/store"
	"github.com/bubbleptr/vpsgogo/packages/core/vault"
)

// Vault keys of the settings that hold secrets.
const (
	channelsKey     = "settings/channels"
	anthropicKeyKey = "settings/anthropic-key"
)

const settingsFile = "settings.json"

// storedSettings is settings.json: the settings that are not secret.
type storedSettings struct {
	Timezone   string `json:"timezone,omitempty"`
	RemindHour *int   `json:"remindHour,omitempty"` // nil: 9
}

func (s storedSettings) hour() int {
	if s.RemindHour == nil {
		return 9
	}
	return *s.RemindHour
}

func (l *Live) Settings(ctx context.Context) (Settings, error) {
	st, err := l.storedSettings()
	if err != nil {
		return Settings{}, err
	}
	chans, err := l.channels()
	if err != nil {
		return Settings{}, err
	}
	key, err := l.vault.Get(anthropicKeyKey)
	if err != nil && !errors.Is(err, vault.ErrNotFound) {
		return Settings{}, fmt.Errorf("services: read the Anthropic key: %w", err)
	}
	return Settings{Timezone: st.Timezone, RemindHour: st.hour(), Channels: chans, AnthropicKey: string(key)}, nil
}

// SaveSettings checks s, stores it, and syncs the channels and agent.json
// to every provisioned server.
func (l *Live) SaveSettings(ctx context.Context, s Settings) error {
	if s.RemindHour < 0 || s.RemindHour > 23 {
		return fmt.Errorf("services: remind hour %d is not an hour of the day", s.RemindHour)
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil {
			return fmt.Errorf("services: unknown time zone %q", s.Timezone)
		}
	}
	if err := (notify.Config{Channels: s.Channels}).Validate(); err != nil {
		return err
	}

	if err := l.saveStored(storedSettings{Timezone: s.Timezone, RemindHour: &s.RemindHour}); err != nil {
		return err
	}
	if err := l.setSecret(channelsKey, s.Channels, len(s.Channels) > 0); err != nil {
		return fmt.Errorf("services: store the channels: %w", err)
	}
	if err := l.setSecret(anthropicKeyKey, s.AnthropicKey, s.AnthropicKey != ""); err != nil {
		return fmt.Errorf("services: store the Anthropic key: %w", err)
	}

	servers, err := l.fleet.List(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, len(servers))
	for i, srv := range servers {
		ids[i] = srv.ID
	}
	return l.sync(ctx, ids, true)
}

// setSecret stores v under key (a string as is, anything else as JSON), or
// deletes key when keep is false.
func (l *Live) setSecret(key string, v any, keep bool) error {
	if !keep {
		return l.vault.Delete(key)
	}
	var b []byte
	if s, ok := v.(string); ok {
		b = []byte(s)
	} else {
		var err error
		if b, err = json.Marshal(v); err != nil {
			return err
		}
	}
	return l.vault.Set(key, b)
}

func (l *Live) channels() ([]notify.ChannelConfig, error) {
	b, err := l.vault.Get(channelsKey)
	if errors.Is(err, vault.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("services: read the channels: %w", err)
	}
	var chans []notify.ChannelConfig
	if err := json.Unmarshal(b, &chans); err != nil {
		return nil, errors.New("services: the stored channels are not valid JSON") // the error could quote a token
	}
	return chans, nil
}

// notifyConfig is notify.json for the agents, nil when there are no
// channels.
func (l *Live) notifyConfig() ([]byte, error) {
	chans, err := l.channels()
	if err != nil || len(chans) == 0 {
		return nil, err
	}
	return json.Marshal(notify.Config{Channels: chans})
}

func (l *Live) storedSettings() (storedSettings, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var st storedSettings
	b, err := os.ReadFile(filepath.Join(l.dir, settingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("services: %s: %w", settingsFile, err)
	}
	return st, nil
}

func (l *Live) saveStored(st storedSettings) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := filepath.Join(l.dir, settingsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// agentConfig is agent.json for srv from what is stored: its plan and the
// user's time zone and remind hour. Provisioning and every sync use it.
func (l *Live) agentConfig(ctx context.Context, srv fleet.Server) ([]byte, error) {
	var plan *renewal.Subscription
	switch s, err := l.plans.Get(ctx, srv.ID); {
	case err == nil:
		plan = &s
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	st, err := l.storedSettings()
	if err != nil {
		return nil, err
	}
	tz := st.Timezone
	if tz == "" {
		tz = l.systemZone()
	}
	hour := st.hour()
	c := agentconfig.Config{Name: srv.Name, Rules: monitoring.DefaultRules(), Renewal: plan, Timezone: tz, RemindHour: &hour}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("services: agent.json for %s: %w", srv.Name, err)
	}
	return json.Marshal(c)
}

// systemZone is the IANA name of the computer's time zone, which the agent
// on the server uses when the user has not chosen one. time.Local only says
// "Local", so it reads $TZ and the /etc/localtime symlink (macOS, Linux).
// Where neither names a zone, as on Windows, it falls back to UTC.
func systemZone() string {
	for _, name := range []string{os.Getenv("TZ"), localtimeLink()} {
		name = strings.TrimPrefix(name, ":")
		if _, after, ok := strings.Cut(name, "zoneinfo/"); ok {
			name = after
		}
		if name == "" || name == "Local" || strings.HasPrefix(name, "/") {
			continue
		}
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return "UTC"
}

func localtimeLink() string {
	p, _ := os.Readlink("/etc/localtime")
	return p
}
