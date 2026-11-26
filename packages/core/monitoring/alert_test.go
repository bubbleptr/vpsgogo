package monitoring

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type step struct {
	at   time.Duration // offset from t0
	obs  func(o *Observation)
	want []string // "key+" for firing, "key-" for resolved
}

func cpu(v float64) func(*Observation)  { return func(o *Observation) { o.Sample.CPU = v } }
func mem(pct uint64) func(*Observation) { return func(o *Observation) { o.Sample.MemUsed = pct * 10 } }

func run(t *testing.T, r Rules, steps []step) {
	t.Helper()
	e := NewEngine(r)
	for i, st := range steps {
		o := Observation{Sample: Sample{Time: t0.Add(st.at), MemTotal: 1000}}
		if st.obs != nil {
			st.obs(&o)
		}
		var got []string
		for _, ev := range e.Observe(o) {
			sign := "-"
			if ev.Firing {
				sign = "+"
			}
			got = append(got, ev.Key+sign)
			if ev.Text == "" || !ev.Time.Equal(o.Sample.Time) {
				t.Errorf("step %d: event %+v lacks text or has the wrong time", i, ev)
			}
		}
		if !reflect.DeepEqual(got, st.want) {
			t.Fatalf("step %d (+%v): events %q, want %q", i, st.at, got, st.want)
		}
	}
}

func TestEngineCPU(t *testing.T) {
	r := DefaultRules() // > 90% for 5m, cooldown 1h
	m := time.Minute
	tests := []struct {
		name  string
		steps []step
	}{
		{"needs the full duration", []step{
			{0, cpu(95), nil},
			{4 * m, cpu(95), nil},
			{5*m - time.Second, cpu(95), nil},
			{5 * m, cpu(95), []string{"cpu+"}},
		}},
		{"a dip resets the timer and emits nothing", []step{
			{0, cpu(95), nil},
			{4 * m, cpu(90), nil}, // at the threshold is not above it
			{5 * m, cpu(95), nil},
			{9 * m, cpu(95), nil},
			{10 * m, cpu(95), []string{"cpu+"}},
		}},
		{"resolves after a notice", []step{
			{0, cpu(95), nil},
			{5 * m, cpu(95), []string{"cpu+"}},
			{6 * m, cpu(40), []string{"cpu-"}},
			{7 * m, cpu(40), nil},
			{8 * m, cpu(95), nil}, // timer starts over
			{13 * m, cpu(95), []string{"cpu+"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { run(t, r, tt.steps) })
	}
}

func TestEngineCooldown(t *testing.T) {
	run(t, DefaultRules(), []step{
		{0, mem(95), []string{"mem+"}},
		{30 * time.Minute, mem(95), nil},
		{time.Hour - time.Second, mem(95), nil},
		{time.Hour, mem(95), []string{"mem+"}},
		{time.Hour + 10*time.Second, mem(95), nil},
		{time.Hour + 20*time.Second, mem(50), []string{"mem-"}},
		{time.Hour + 30*time.Second, mem(95), []string{"mem+"}}, // fresh alert, no cooldown
	})
}

func TestEngineMemTotalZeroNeverFires(t *testing.T) {
	e := NewEngine(DefaultRules())
	if ev := e.Observe(Observation{Sample: Sample{Time: t0, MemUsed: 5}}); len(ev) != 0 {
		t.Errorf("events %+v", ev)
	}
}

func TestEngineDisks(t *testing.T) {
	disks := func(d ...Disk) func(*Observation) { return func(o *Observation) { o.Sample.Disks = d } }
	run(t, DefaultRules(), []step{
		{0, disks(Disk{"/", 90, 100}, Disk{"/data", 10, 100}, Disk{"/empty", 0, 0}), []string{"disk:/+"}},
		{time.Minute, disks(Disk{"/", 50, 100}, Disk{"/data", 86, 100}), []string{"disk:/-", "disk:/data+"}},
		// /data disappears (unmounted, or statfs failed): resolve instead of keeping it forever.
		{2 * time.Minute, disks(Disk{"/", 50, 100}), []string{"disk:/data-"}},
	})
}

func TestEngineUnitsAndProbes(t *testing.T) {
	r := DefaultRules()
	r.Units = []string{"caddy.service", "docker.service", "gogo-agent.service"}
	r.Probes = []string{"https://a.example", "https://b.example", "https://c.example"}
	checks := func(units map[string]bool, probes map[string]error) func(*Observation) {
		return func(o *Observation) { o.Units, o.Probes = units, probes }
	}
	run(t, r, []step{
		{0, checks(
			map[string]bool{"caddy.service": true, "docker.service": false},                        // gogo-agent.service missing
			map[string]error{"https://a.example": nil, "https://b.example": errors.New("timeout")}, // c missing
		), []string{"probe:https://b.example+", "probe:https://c.example+", "unit:docker.service+", "unit:gogo-agent.service+"}},
		{time.Minute, checks(
			map[string]bool{"caddy.service": true, "docker.service": true, "gogo-agent.service": true},
			map[string]error{"https://a.example": nil, "https://b.example": nil, "https://c.example": nil},
		), []string{"probe:https://b.example-", "probe:https://c.example-", "unit:docker.service-", "unit:gogo-agent.service-"}},
	})
}

func TestEngineEventsSortedAndWorded(t *testing.T) {
	r := DefaultRules()
	r.CPUFor = 0
	r.Units = []string{"caddy.service"}
	r.Probes = []string{"https://x.example"}
	e := NewEngine(r)
	evs := e.Observe(Observation{
		Sample: Sample{Time: t0, CPU: 97, MemUsed: 93, MemTotal: 100, Disks: []Disk{{"/", 91, 100}}},
		Probes: map[string]error{"https://x.example": errors.New("connection refused")},
	})
	var keys []string
	texts := map[string]string{}
	for _, ev := range evs {
		keys = append(keys, ev.Key)
		texts[ev.Key] = ev.Text
	}
	if want := []string{"cpu", "disk:/", "mem", "probe:https://x.example", "unit:caddy.service"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %q, want %q", keys, want)
	}
	for key, frag := range map[string]string{
		"cpu": "97%", "disk:/": "91%", "mem": "93%",
		"probe:https://x.example": "connection refused", "unit:caddy.service": "caddy.service",
	} {
		if !strings.Contains(texts[key], frag) {
			t.Errorf("%s text %q lacks %q", key, texts[key], frag)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5 * time.Minute:                   "5m",
		90 * time.Second:                  "1m30s",
		time.Hour:                         "1h",
		time.Hour + 5*time.Minute:         "1h5m",
		45*time.Second + time.Millisecond: "45s",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
