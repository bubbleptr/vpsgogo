package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/agentconfig"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
)

// Config is /etc/gogo-agent/agent.json, which the desktop app writes.
type Config = agentconfig.Config

// LoadConfig reads and checks agent.json. Missing rules take the defaults.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{Rules: monitoring.DefaultRules()}, err
	}
	c, err := agentconfig.Parse(data)
	if err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
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
