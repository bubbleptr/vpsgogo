package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// Config is /etc/gogo-agent/agent.json, which the desktop app writes. It holds
// no secrets; channel credentials are in notify.json.
type Config struct {
	Name      string           `json:"name"`
	Rules     monitoring.Rules `json:"rules"`
	Heartbeat string           `json:"heartbeat,omitempty"` // HTTPS URL pinged every minute, Healthchecks.io style

	// Renewal is the server's plan, for renewal reminders.
	Renewal *renewal.Subscription `json:"renewal,omitempty"`
	// Timezone is the user's, such as "Asia/Shanghai": reminders go by the
	// user's calendar, not the server's. Empty means UTC.
	Timezone string `json:"timezone,omitempty"`
	// RemindHour is the hour of the day reminders go out from; default 9.
	RemindHour *int `json:"remindHour,omitempty"`
}

// Location is the user's time zone.
func (c Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(c.Timezone)
}

// RemindHourOr9 is RemindHour, or 9 when it is not set.
func (c Config) RemindHourOr9() int {
	if c.RemindHour == nil {
		return 9
	}
	return *c.RemindHour
}

// LoadConfig reads and checks agent.json. Missing rules take the defaults.
func LoadConfig(path string) (Config, error) {
	c := Config{Rules: monitoring.DefaultRules()}
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	for _, u := range append([]string{c.Heartbeat}, c.Rules.Probes...) {
		if u == "" {
			continue
		}
		if p, err := url.Parse(u); err != nil || p.Scheme != "https" || p.Host == "" {
			return c, fmt.Errorf("%s: %q is not an https URL", path, u)
		}
	}
	if _, err := c.Location(); err != nil {
		return c, fmt.Errorf("%s: timezone %q: %w", path, c.Timezone, err)
	}
	if h := c.RemindHourOr9(); h < 0 || h > 23 {
		return c, fmt.Errorf("%s: remindHour %d is not an hour of the day", path, h)
	}
	if c.Renewal != nil {
		if err := c.Renewal.Validate(); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	return c, nil
}

// LoadNotify reads notify.json and builds its channels. A missing file means
// no channels: alerts then reach only the journal.
func LoadNotify(path string) ([]notify.Channel, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c notify.Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON", path) // the error could quote a token
	}
	return notify.Build(c)
}

// SystemdUnitActive reports whether systemd says unit is active.
func SystemdUnitActive(ctx context.Context, unit string) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// HTTPSProbe fetches url and fails unless it answers below 400.
func HTTPSProbe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
