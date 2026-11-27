// Command gogo-agent runs on the VPS: it collects metrics and sends alerts
// and renewal reminders while the desktop app is closed. See docs/SPEC.md
// §4.2 and §4.3.
//
//	gogo-agent run                     the daemon (systemd runs it)
//	gogo-agent query --since 10m --json  samples after a time, for the desktop app
//	gogo-agent stream --json           new samples as they are taken
//	gogo-agent version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // reminders follow the user's time zone; servers may lack tzdata

	"github.com/bubbleptr/vpsgogo/apps/agent/internal/agent"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const (
	configPath = "/etc/gogo-agent/agent.json"
	notifyPath = "/etc/gogo-agent/notify.json"
	statePath  = "/var/lib/gogo-agent/samples.json"
	sentPath   = "/var/lib/gogo-agent/renewal.json"
	socketPath = "/run/gogo-agent/agent.sock"
	interval   = 10 * time.Second
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "run":
		err = run(ctx)
	case "query":
		err = query(ctx, os.Args[2:])
	case "stream":
		err = stream(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gogo-agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gogo-agent run | query [--since T] [--json] | stream [--json] | version")
	os.Exit(2)
}

func run(ctx context.Context) error {
	cfg, err := agent.LoadConfig(configPath)
	if err != nil {
		return err
	}
	chans, err := agent.LoadNotify(notifyPath)
	if err != nil {
		return err
	}
	loc, err := cfg.Location()
	if err != nil {
		return err
	}
	collector := &monitoring.Collector{}
	o := agent.Options{
		Name:       cfg.Name,
		Rules:      cfg.Rules,
		Interval:   interval,
		Source:     collector.Collect,
		UnitActive: agent.SystemdUnitActive,
		Probe:      agent.HTTPSProbe,
		Notify:     func(ctx context.Context, m notify.Message) error { return notify.SendAll(ctx, chans, m) },
		SocketPath: socketPath,
		StatePath:  statePath,
		Log:        log.New(os.Stderr, "", 0),

		Renewal:          cfg.Renewal,
		Location:         loc,
		RemindHour:       cfg.RemindHourOr9(),
		RenewalStatePath: sentPath,
	}
	if cfg.Heartbeat != "" {
		o.Heartbeat = func(ctx context.Context) error { return agent.HTTPSProbe(ctx, cfg.Heartbeat) }
	}
	log.Printf("gogo-agent %s: %d notify channels, %d units, %d probes, renewal %v", version, len(chans), len(cfg.Rules.Units), len(cfg.Rules.Probes), cfg.Renewal != nil)
	return agent.Run(ctx, o)
}

func query(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	since := fs.String("since", "0", "samples after this time: Unix seconds, RFC 3339, or a duration ago such as 10m")
	asJSON := fs.Bool("json", false, "print a JSON array")
	fs.Parse(args)
	t, err := parseSince(*since, time.Now())
	if err != nil {
		return err
	}
	samples, err := agent.Query(ctx, socketPath, t)
	if err != nil {
		return err
	}
	if *asJSON {
		if samples == nil {
			samples = []monitoring.Sample{}
		}
		return json.NewEncoder(os.Stdout).Encode(samples)
	}
	for _, s := range samples {
		printSample(s)
	}
	return nil
}

func stream(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print one JSON sample a line")
	fs.Parse(args)
	enc := json.NewEncoder(os.Stdout)
	return agent.Stream(ctx, socketPath, func(s monitoring.Sample) error {
		if *asJSON {
			return enc.Encode(s)
		}
		printSample(s)
		return nil
	})
}

func printSample(s monitoring.Sample) {
	mem := 0.0
	if s.MemTotal > 0 {
		mem = float64(s.MemUsed) / float64(s.MemTotal) * 100
	}
	fmt.Printf("%s  cpu %5.1f%%  mem %5.1f%%  load %.2f  rx %.0f B/s  tx %.0f B/s\n",
		s.Time.Local().Format(time.DateTime), s.CPU, mem, s.Load1, s.NetRx, s.NetTx)
}

// parseSince reads Unix seconds, an RFC 3339 time, or a duration before now.
func parseSince(v string, now time.Time) (time.Time, error) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n == 0 {
			return time.Time{}, nil
		}
		return time.Unix(n, 0), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, errors.New("--since: want Unix seconds, RFC 3339 or a duration such as 10m")
}
