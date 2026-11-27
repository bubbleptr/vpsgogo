// Package agentconfig is /etc/gogo-agent/agent.json: the desktop app builds
// it, gogo-agent reads it. It holds no secrets; channel credentials are in
// notify.json. See docs/SPEC.md §4.2 and §4.3.
package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// Config is agent.json.
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

// Parse reads and checks agent.json. Missing rules take the defaults.
func Parse(data []byte) (Config, error) {
	c := Config{Rules: monitoring.DefaultRules()}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Validate checks what the agent cannot work with: URLs that are not https,
// an unknown time zone, an hour that is not one of the day, and a plan that
// does not validate. Errors never quote a URL, which may carry a secret.
func (c Config) Validate() error {
	if c.Heartbeat != "" && !isHTTPS(c.Heartbeat) {
		return errors.New("heartbeat is not an https URL")
	}
	for i, u := range c.Rules.Probes {
		if !isHTTPS(u) {
			return fmt.Errorf("probe %d is not an https URL", i+1)
		}
	}
	if _, err := c.Location(); err != nil {
		return fmt.Errorf("timezone %q: %w", c.Timezone, err)
	}
	if h := c.RemindHourOr9(); h < 0 || h > 23 {
		return fmt.Errorf("remindHour %d is not an hour of the day", h)
	}
	if c.Renewal != nil {
		return c.Renewal.Validate()
	}
	return nil
}

func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
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
