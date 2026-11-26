package monitoring

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Engine turns observations into alert events. It is a pure state machine:
// the observation's sample time is its only clock. Not safe for concurrent use.
type Engine struct {
	rules    Rules
	cpuSince time.Time            // start of the current run of CPU above the threshold; zero when below
	notified map[string]time.Time // key → time of its last firing notice
}

// NewEngine returns an engine for the rules.
func NewEngine(r Rules) *Engine {
	return &Engine{rules: r, notified: map[string]time.Time{}}
}

// condition is the state of one alert key in one observation.
type condition struct {
	active bool
	firing string // text when active
	ok     string // text when it recovered
}

// Observe feeds one observation and returns the events to send, sorted by key.
func (e *Engine) Observe(o Observation) []Event {
	now := o.Sample.Time
	conds := e.conditions(o)

	// A key that notified but is no longer evaluated (a disk that vanished)
	// resolves rather than staying open forever.
	for key := range e.notified {
		if _, ok := conds[key]; !ok {
			conds[key] = condition{ok: vanishedText(key)}
		}
	}

	var events []Event
	for key, c := range conds {
		last, notified := e.notified[key]
		switch {
		case c.active && (!notified || now.Sub(last) >= e.rules.Cooldown):
			e.notified[key] = now
			events = append(events, Event{Key: key, Firing: true, Text: c.firing, Time: now})
		case !c.active && notified:
			delete(e.notified, key)
			events = append(events, Event{Key: key, Firing: false, Text: c.ok, Time: now})
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Key < events[j].Key })
	return events
}

func (e *Engine) conditions(o Observation) map[string]condition {
	s, r := o.Sample, e.rules
	conds := map[string]condition{}

	if s.CPU > r.CPUPercent {
		if e.cpuSince.IsZero() {
			e.cpuSince = s.Time
		}
	} else {
		e.cpuSince = time.Time{}
	}
	cpu := condition{ok: fmt.Sprintf("CPU back to %.0f%%", s.CPU)}
	if !e.cpuSince.IsZero() {
		if d := s.Time.Sub(e.cpuSince); d >= r.CPUFor {
			cpu.active = true
			cpu.firing = fmt.Sprintf("CPU %.0f%% for %s", s.CPU, formatDuration(d))
		}
	}
	conds["cpu"] = cpu

	if s.MemTotal > 0 {
		pct := float64(s.MemUsed) / float64(s.MemTotal) * 100
		conds["mem"] = condition{
			active: pct > r.MemPercent,
			firing: fmt.Sprintf("Memory %.0f%%", pct),
			ok:     fmt.Sprintf("Memory back to %.0f%%", pct),
		}
	}

	for _, d := range s.Disks {
		if d.Total == 0 {
			continue
		}
		pct := float64(d.Used) / float64(d.Total) * 100
		conds["disk:"+d.Mount] = condition{
			active: pct > r.DiskPercent,
			firing: fmt.Sprintf("Disk %s %.0f%%", d.Mount, pct),
			ok:     fmt.Sprintf("Disk %s back to %.0f%%", d.Mount, pct),
		}
	}

	for _, u := range r.Units {
		conds["unit:"+u] = condition{
			active: !o.Units[u],
			firing: fmt.Sprintf("Unit %s is not active", u),
			ok:     fmt.Sprintf("Unit %s is active again", u),
		}
	}

	for _, url := range r.Probes {
		err, seen := o.Probes[url]
		c := condition{active: !seen || err != nil, ok: fmt.Sprintf("Probe %s is answering again", url)}
		switch {
		case !seen:
			c.firing = fmt.Sprintf("Probe %s was not checked", url)
		case err != nil:
			c.firing = fmt.Sprintf("Probe %s failed: %v", url, err)
		}
		conds["probe:"+url] = c
	}
	return conds
}

func vanishedText(key string) string {
	if mount, ok := strings.CutPrefix(key, "disk:"); ok {
		return fmt.Sprintf("Disk %s is no longer reported", mount)
	}
	return fmt.Sprintf("%s is no longer checked", key)
}

// formatDuration prints d to the second without trailing zero units: 5m, 1h5m, 1m30s.
func formatDuration(d time.Duration) string {
	s := d.Round(time.Second).String()
	if strings.HasSuffix(s, "m0s") { // 5m0s → 5m, 1h0m0s → 1h0m
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") { // 1h0m → 1h
		s = s[:len(s)-2]
	}
	return s
}
