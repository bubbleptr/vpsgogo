package provisioning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
)

// fakeAgentHost is a server for the agent step: files with their owners and
// modes, a gogo-agent user, and a systemd that records what it was told.
type fakeAgentHost struct {
	*fakeHost
	arch      string
	blobs     map[string][]byte // files written by upload
	modes     map[string]string // path → "mode owner"
	userMade  bool
	inGroup   bool
	systemctl []string
	active    bool
	enabled   bool
}

func newFakeAgentHost() *fakeAgentHost {
	return &fakeAgentHost{fakeHost: newFakeHost(), arch: "arm64", blobs: map[string][]byte{}, modes: map[string]string{}}
}

func (h *fakeAgentHost) Arch(context.Context) (string, error) { return h.arch, nil }
func (h *fakeAgentHost) FileSHA256(_ context.Context, path string) (string, bool, error) {
	b, ok := h.blobs[path]
	if !ok {
		return "", false, nil
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true, nil
}
func (h *fakeAgentHost) Upload(_ context.Context, path, mode, owner string, data []byte) error {
	h.blobs[path] = append([]byte(nil), data...)
	h.modes[path] = mode + " " + owner
	return nil
}
func (h *fakeAgentHost) RemoveFile(_ context.Context, path string) error {
	delete(h.blobs, path)
	delete(h.fakeHost.files, path)
	return nil
}
func (h *fakeAgentHost) EnsureAgentUser(context.Context) (bool, error) {
	changed := !h.userMade || !h.inGroup
	h.userMade, h.inGroup = true, true
	return changed, nil
}
func (h *fakeAgentHost) Systemctl(_ context.Context, args ...string) error {
	cmd := strings.Join(args, " ")
	h.systemctl = append(h.systemctl, cmd)
	switch {
	case strings.HasPrefix(cmd, "restart"):
		h.active = true
	case strings.HasPrefix(cmd, "enable"):
		h.enabled = true
	}
	return nil
}
func (h *fakeAgentHost) AgentState(context.Context) (enabled, active bool, err error) {
	return h.enabled, h.active, nil
}

func testInstall() AgentInstall {
	return AgentInstall{
		Binary: func(arch string) ([]byte, error) { return []byte("agent-binary-" + arch), nil },
		Config: []byte(`{"name":"vps"}`),
		Notify: []byte(`{"channels":[{"type":"telegram","token":"SECRET-TOKEN","chatId":"1"}]}`),
	}
}

func healthy(context.Context) error { return nil }

func TestAgentInstallsForTheServersArch(t *testing.T) {
	h := newFakeAgentHost()
	h.arch = "amd64"
	changed, err := ensureAgent(context.Background(), h, testInstall(), healthy)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := string(h.blobs[agentBinPath]); got != "agent-binary-amd64" {
		t.Errorf("installed %q", got)
	}
	if got := h.modes[agentBinPath]; got != "0755 root:root" {
		t.Errorf("binary mode %q", got)
	}
	if got := h.modes[agentNotifyPath]; got != "0640 root:gogo-agent" {
		t.Errorf("notify.json mode %q, want 0640 root:gogo-agent", got)
	}
	if !h.userMade || !h.inGroup || !h.active || !h.enabled {
		t.Errorf("user=%v group=%v active=%v enabled=%v", h.userMade, h.inGroup, h.active, h.enabled)
	}
	if h.fakeHost.files[agentUnitPath] == "" {
		t.Error("no systemd unit written")
	}
}

func TestAgentRerunChangesNothing(t *testing.T) {
	h := newFakeAgentHost()
	ctx := context.Background()
	if _, err := ensureAgent(ctx, h, testInstall(), healthy); err != nil {
		t.Fatal(err)
	}
	h.systemctl = nil
	changed, err := ensureAgent(ctx, h, testInstall(), healthy)
	if err != nil || changed {
		t.Fatalf("rerun: changed=%v err=%v", changed, err)
	}
	for _, c := range h.systemctl {
		if strings.HasPrefix(c, "restart") {
			t.Errorf("rerun restarted the agent: %v", h.systemctl)
		}
	}
}

func TestAgentRestartsWhenConfigChanges(t *testing.T) {
	h := newFakeAgentHost()
	ctx := context.Background()
	in := testInstall()
	if _, err := ensureAgent(ctx, h, in, healthy); err != nil {
		t.Fatal(err)
	}
	h.systemctl = nil
	in.Notify = []byte(`{"channels":[]}`)
	changed, err := ensureAgent(ctx, h, in, healthy)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.HasPrefix(strings.Join(h.systemctl, ";"), "daemon-reload") || !strings.Contains(strings.Join(h.systemctl, ";"), "restart gogo-agent") {
		t.Errorf("systemctl calls %v", h.systemctl)
	}
}

func TestAgentStartsAStoppedAgent(t *testing.T) {
	h := newFakeAgentHost()
	ctx := context.Background()
	if _, err := ensureAgent(ctx, h, testInstall(), healthy); err != nil {
		t.Fatal(err)
	}
	h.active = false
	changed, err := ensureAgent(ctx, h, testInstall(), healthy)
	if err != nil || !changed || !h.active {
		t.Fatalf("changed=%v err=%v active=%v", changed, err, h.active)
	}
}

func TestAgentFailsWhenUnhealthy(t *testing.T) {
	h := newFakeAgentHost()
	_, err := ensureAgent(context.Background(), h, testInstall(), func(context.Context) error { return errors.New("no samples") })
	if err == nil || !strings.Contains(err.Error(), "no samples") {
		t.Fatalf("err = %v", err)
	}
}

// recordingSession records commands and their stdin, answering every one
// with success.
type recordingSession struct {
	cmds  []string
	stdin []string
}

func (s *recordingSession) Exec(_ context.Context, cmd string) (sshx.ExecResult, error) {
	s.cmds = append(s.cmds, cmd)
	return sshx.ExecResult{}, nil
}
func (s *recordingSession) ExecInput(_ context.Context, cmd string, in io.Reader) (sshx.ExecResult, error) {
	s.cmds = append(s.cmds, cmd)
	b, _ := io.ReadAll(in)
	s.stdin = append(s.stdin, string(b))
	return sshx.ExecResult{}, nil
}
func (s *recordingSession) Close() error { return nil }

func TestUploadKeepsContentOutOfCommandsAndLog(t *testing.T) {
	s := &recordingSession{}
	var log bytes.Buffer
	h := &remote{s: s, root: false, log: &log}
	secret := []byte(`{"token":"SECRET-TOKEN"}`)
	if err := h.Upload(context.Background(), agentNotifyPath, "0640", "root:gogo-agent", secret); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "SECRET-TOKEN") || strings.Contains(strings.Join(s.cmds, "\n"), "SECRET-TOKEN") {
		t.Fatalf("the secret reached a command or the log:\n%s\n%s", strings.Join(s.cmds, "\n"), log.String())
	}
	if len(s.stdin) != 1 || s.stdin[0] != string(secret) {
		t.Fatalf("stdin %q", s.stdin)
	}
	// The file is created with its final owner and mode before it is moved
	// into place, so the secret is never readable by others.
	cmd := s.cmds[0]
	if !strings.Contains(cmd, "chown root:gogo-agent") || !strings.Contains(cmd, "chmod 0640") || !strings.Contains(cmd, "sha256sum -c") {
		t.Errorf("upload command %q", cmd)
	}
}

func TestSyncAgentConfigRestartsOnlyOnChange(t *testing.T) {
	h := newFakeAgentHost()
	ctx := context.Background()
	if _, err := ensureAgent(ctx, h, testInstall(), healthy); err != nil {
		t.Fatal(err)
	}
	h.systemctl = nil
	checked := 0
	counted := func(context.Context) error { checked++; return nil }

	changed, err := syncAgentConfig(ctx, h, testInstall().Config, counted)
	if err != nil || changed || len(h.systemctl) != 0 {
		t.Fatalf("same config: changed=%v err=%v systemctl=%v", changed, err, h.systemctl)
	}
	cfg := []byte(`{"name":"vps","renewal":{"serverId":"s1","expiresAt":"2026-12-01","cycle":"monthly"}}`)
	changed, err = syncAgentConfig(ctx, h, cfg, counted)
	if err != nil || !changed {
		t.Fatalf("new config: changed=%v err=%v", changed, err)
	}
	if string(h.blobs[agentConfigPath]) != string(cfg) || h.modes[agentConfigPath] != "0644 root:root" {
		t.Errorf("agent.json %q %q", h.blobs[agentConfigPath], h.modes[agentConfigPath])
	}
	if strings.Join(h.systemctl, ";") != "restart gogo-agent" {
		t.Errorf("systemctl %v", h.systemctl)
	}
	if checked != 2 {
		t.Errorf("health checked %d times, want after every sync", checked)
	}
}

func TestSyncAgentConfigFailsWhenTheAgentDoesNotComeBack(t *testing.T) {
	h := newFakeAgentHost()
	ctx := context.Background()
	if _, err := ensureAgent(ctx, h, testInstall(), healthy); err != nil {
		t.Fatal(err)
	}
	_, err := syncAgentConfig(ctx, h, []byte(`{"timezone":"Mars/Olympus"}`), func(context.Context) error { return errors.New("connection refused") })
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncAgentConfigNeedsAnInstalledAgent(t *testing.T) {
	h := newFakeAgentHost()
	if _, err := syncAgentConfig(context.Background(), h, []byte(`{}`), healthy); err == nil {
		t.Fatal("synced to a server without gogo-agent")
	}
}
