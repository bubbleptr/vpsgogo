package agentconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// What the desktop app writes, the agent reads back unchanged.
func TestDesktopConfigParsesInTheAgent(t *testing.T) {
	hour := 0
	sub := renewal.Subscription{ServerID: "s1", ExpiresAt: renewal.Date{Year: 2026, Month: 12, Day: 1}, Cycle: renewal.Monthly}
	want := Config{Name: "vps", Rules: monitoring.DefaultRules(), Renewal: &sub, Timezone: "Asia/Tokyo", RemindHour: &hour}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "vps" || got.Rules.CPUPercent != 90 || got.Renewal == nil || got.Renewal.ExpiresAt != sub.ExpiresAt || got.RemindHourOr9() != 0 {
		t.Errorf("parsed %+v", got)
	}
	if loc, _ := got.Location(); loc.String() != "Asia/Tokyo" {
		t.Errorf("location %v", loc)
	}
}

func TestParseFillsDefaultRules(t *testing.T) {
	c, err := Parse([]byte(`{"name":"vps"}`))
	if err != nil || c.Rules.CPUPercent != 90 || c.RemindHourOr9() != 9 {
		t.Errorf("%+v %v", c, err)
	}
}

func TestValidate(t *testing.T) {
	bad := 24
	for name, c := range map[string]Config{
		"zone":      {Timezone: "Mars/Olympus"},
		"hour":      {RemindHour: &bad},
		"heartbeat": {Heartbeat: "http://hc-ping.com/abc"},
		"probe":     {Rules: monitoring.Rules{Probes: []string{"http://example.com"}}},
		"plan":      {Renewal: &renewal.Subscription{ServerID: "s1", Cycle: renewal.Monthly}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, c)
		}
	}
	err := Config{Heartbeat: "http://user:SECRET@hc-ping.com"}.Validate()
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
}
