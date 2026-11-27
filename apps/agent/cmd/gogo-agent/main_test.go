package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"0", time.Time{}, true}, // everything
		{"1764244800", time.Unix(1764244800, 0), true},
		{"2026-11-27T11:00:00Z", now.Add(-time.Hour), true},
		{"10m", now.Add(-10 * time.Minute), true},
		{"-10m", time.Time{}, false}, // the future is no "since"
		{"yesterday", time.Time{}, false},
	} {
		got, err := parseSince(tc.in, now)
		if (err == nil) != tc.ok || (tc.ok && !got.Equal(tc.want)) {
			t.Errorf("parseSince(%q) = %v, %v; want %v ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

// The agent shares packages/core's pure logic only: the desktop's storage
// and UI (mygo, SQLite) would bloat a binary uploaded to every server.
func TestAgentLinksNoDesktopCode(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/egoist/mygo") {
			t.Errorf("gogo-agent depends on %s", dep)
		}
	}
}
