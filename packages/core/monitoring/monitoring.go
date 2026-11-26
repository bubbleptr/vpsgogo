// Package monitoring holds the pure logic of server metrics: reading samples
// from /proc, keeping seven days of them, and turning them into alerts.
// gogo-agent runs it on the VPS; the desktop app reads its samples.
// See docs/SPEC.md §4.2.
package monitoring

import "time"

// Disk is the usage of one mounted filesystem.
type Disk struct {
	Mount string `json:"mount"`
	Used  uint64 `json:"used"`  // bytes
	Total uint64 `json:"total"` // bytes
}

// Sample is one reading of a server's state.
type Sample struct {
	Time      time.Time `json:"time"`
	CPU       float64   `json:"cpu"` // percent busy since the previous reading, 0–100
	MemUsed   uint64    `json:"memUsed"`
	MemTotal  uint64    `json:"memTotal"`
	SwapUsed  uint64    `json:"swapUsed"`
	SwapTotal uint64    `json:"swapTotal"`
	Disks     []Disk    `json:"disks"`
	NetRx     float64   `json:"netRx"` // bytes per second, all interfaces but loopback
	NetTx     float64   `json:"netTx"`
	Load1     float64   `json:"load1"`
	Load5     float64   `json:"load5"`
	Load15    float64   `json:"load15"`
	Uptime    float64   `json:"uptime"`   // seconds
	TCPConns  int       `json:"tcpConns"` // established TCP connections, v4 and v6
}

// Rules are the alert thresholds (docs/SPEC.md §4.2 defaults).
type Rules struct {
	CPUPercent  float64       `json:"cpuPercent"`  // fires when CPU stays above it for CPUFor
	CPUFor      time.Duration `json:"cpuFor"`
	MemPercent  float64       `json:"memPercent"`  // fires as soon as memory is above it
	DiskPercent float64       `json:"diskPercent"` // per mount
	Units       []string      `json:"units"`       // systemd units that must be active
	Probes      []string      `json:"probes"`      // HTTPS URLs that must answer 2xx/3xx
	Cooldown    time.Duration `json:"cooldown"`    // minimum time between two notices of one alert
}

// DefaultRules returns the defaults of docs/SPEC.md §4.2.
func DefaultRules() Rules {
	return Rules{CPUPercent: 90, CPUFor: 5 * time.Minute, MemPercent: 90, DiskPercent: 85, Cooldown: time.Hour}
}

// Observation is what the agent saw at one time: a sample, and the results
// of the unit and probe checks the rules ask for.
type Observation struct {
	Sample Sample
	Units  map[string]bool  // unit → active
	Probes map[string]error // URL → nil when it answered
}

// Event is an alert to send: firing (again, after the cooldown) or resolved.
type Event struct {
	Key    string    // identifies the alert, e.g. "cpu", "disk:/", "unit:caddy.service"
	Firing bool      // false: the alert recovered
	Text   string    // one line for people, e.g. "CPU 97% for 5m"
	Time   time.Time // the observation's time
}
