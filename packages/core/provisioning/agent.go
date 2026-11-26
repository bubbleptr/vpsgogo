package provisioning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// AgentInstall is what the agent step installs (docs/SPEC.md §4.1 step 7).
type AgentInstall struct {
	// Binary returns gogo-agent for a Linux arch, "amd64" or "arm64".
	Binary func(arch string) ([]byte, error)
	// Config is /etc/gogo-agent/agent.json.
	Config []byte
	// Notify is /etc/gogo-agent/notify.json. It holds channel tokens, so it
	// travels over stdin and never reaches a command line or the log. Nil
	// removes the file.
	Notify []byte
}

const (
	agentBinPath    = "/usr/local/bin/gogo-agent"
	agentConfigDir  = "/etc/gogo-agent"
	agentConfigPath = agentConfigDir + "/agent.json"
	agentNotifyPath = agentConfigDir + "/notify.json"
	agentUnitPath   = "/etc/systemd/system/gogo-agent.service"
	agentUser       = "gogo-agent"
)

// agentUnit runs the agent as its own unprivileged user in a systemd sandbox
// (docs/SPEC.md §5.2). Its data is readable by the gogo-agent group, which
// the gogo user joins, and its socket is the only way in.
const agentUnit = `[Unit]
Description=VPSGoGo agent: metrics and alerts
After=network-online.target
Wants=network-online.target

[Service]
User=gogo-agent
Group=gogo-agent
ExecStart=/usr/local/bin/gogo-agent run
Restart=on-failure
RestartSec=5s
StateDirectory=gogo-agent
StateDirectoryMode=0750
RuntimeDirectory=gogo-agent
RuntimeDirectoryMode=0750
UMask=0007
NoNewPrivileges=yes
CapabilityBoundingSet=
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`

// agentHost is what the agent step needs of a server.
type agentHost interface {
	host
	Arch(ctx context.Context) (string, error)
	FileSHA256(ctx context.Context, path string) (sum string, exists bool, err error)
	// Upload writes data to path through stdin, with mode and owner set
	// before the file is moved into place.
	Upload(ctx context.Context, path, mode, owner string, data []byte) error
	EnsureAgentUser(ctx context.Context) (changed bool, err error)
	Systemctl(ctx context.Context, args ...string) error
	AgentState(ctx context.Context) (enabled, active bool, err error)
}

// ensureAgent installs or updates gogo-agent and checks it answers. healthy
// asks the agent for samples over a new gogo connection.
func ensureAgent(ctx context.Context, h agentHost, in AgentInstall, healthy func(context.Context) error) (bool, error) {
	arch, err := h.Arch(ctx)
	if err != nil {
		return false, err
	}
	bin, err := in.Binary(arch)
	if err != nil {
		return false, fmt.Errorf("provisioning: no gogo-agent for linux/%s: %w", arch, err)
	}
	changed, err := h.EnsureAgentUser(ctx)
	if err != nil {
		return false, err
	}
	restart := false
	if c, err := ensureBlob(ctx, h, agentBinPath, "0755", "root:root", bin); err != nil {
		return false, err
	} else {
		restart = restart || c
	}
	if c, err := ensureBlob(ctx, h, agentConfigPath, "0644", "root:root", in.Config); err != nil {
		return false, err
	} else {
		restart = restart || c
	}
	if in.Notify == nil {
		if _, exists, err := h.FileSHA256(ctx, agentNotifyPath); err != nil {
			return false, err
		} else if exists {
			if err := h.RemoveFile(ctx, agentNotifyPath); err != nil {
				return false, err
			}
			restart = true
		}
	} else if c, err := ensureBlob(ctx, h, agentNotifyPath, "0640", "root:"+agentUser, in.Notify); err != nil {
		return false, err
	} else {
		restart = restart || c
	}
	if c, err := ensureFile(ctx, h, agentUnitPath, agentUnit); err != nil {
		return false, err
	} else {
		restart = restart || c
	}

	enabled, active, err := h.AgentState(ctx)
	if err != nil {
		return false, err
	}
	if restart || !enabled || !active {
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "gogo-agent"}, {"restart", "gogo-agent"}} {
			if err := h.Systemctl(ctx, args...); err != nil {
				return false, err
			}
		}
		changed = true
	}
	if err := healthy(ctx); err != nil {
		return changed, fmt.Errorf("provisioning: gogo-agent is not answering: %w", err)
	}
	return changed || restart, nil
}

// ensureBlob uploads data to path unless the file there already has its hash.
func ensureBlob(ctx context.Context, h agentHost, path, mode, owner string, data []byte) (bool, error) {
	sum := sha256.Sum256(data)
	cur, exists, err := h.FileSHA256(ctx, path)
	if err != nil {
		return false, err
	}
	if exists && cur == hex.EncodeToString(sum[:]) {
		return false, nil
	}
	return true, h.Upload(ctx, path, mode, owner, data)
}

// The agentHost implementation over SSH.

func (h *remote) Arch(ctx context.Context) (string, error) {
	out, err := h.run(ctx, "uname -m")
	if err != nil {
		return "", err
	}
	switch m := strings.TrimSpace(out); m {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("provisioning: unsupported architecture %q", m)
	}
}

func (h *remote) FileSHA256(ctx context.Context, path string) (string, bool, error) {
	q := shellQuote(path)
	res, err := h.exec(ctx, "if [ -f "+q+" ]; then sha256sum "+q+"; else exit 3; fi")
	switch {
	case err != nil:
		return "", false, err
	case res.Code == 3:
		return "", false, nil
	case res.Code != 0:
		return "", false, fmt.Errorf("provisioning: sha256sum %s: %s", path, strings.TrimSpace(res.Stderr))
	}
	sum, _, _ := strings.Cut(res.Stdout, " ")
	return sum, true, nil
}

func (h *remote) Upload(ctx context.Context, path, mode, owner string, data []byte) error {
	sum := sha256.Sum256(data)
	dir := path[:strings.LastIndex(path, "/")]
	// The directory exists (EnsureAgentUser makes /etc/gogo-agent) and keeps
	// its mode. mktemp creates the file 0600 root:root; it takes its final
	// owner and mode before it is renamed into place, and only if the bytes
	// arrived intact.
	script := fmt.Sprintf(`set -e
t=$(mktemp %[1]s/.vpsgogo-XXXXXX)
trap 'rm -f "$t"' EXIT
cat > "$t"
echo "%[2]s  $t" | sha256sum -c --quiet
chown %[3]s "$t"
chmod %[4]s "$t"
mv "$t" %[5]s
trap - EXIT`, shellQuote(dir), hex.EncodeToString(sum[:]), owner, mode, shellQuote(path))
	return h.runInput(ctx, script, bytes.NewReader(data))
}

// EnsureAgentUser creates the gogo-agent system user, which has no shell and
// no home, and puts gogo in its group to read samples.
func (h *remote) EnsureAgentUser(ctx context.Context) (bool, error) {
	out, err := h.run(ctx, `set -e
changed=0
if ! getent passwd gogo-agent >/dev/null; then
  useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin gogo-agent
  changed=1
fi
if ! id -nG gogo | tr ' ' '\n' | grep -qx gogo-agent; then
  usermod -aG gogo-agent gogo
  changed=1
fi
install -d -m 0750 -o root -g gogo-agent /etc/gogo-agent
echo changed=$changed`)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "changed=1"), nil
}

func (h *remote) Systemctl(ctx context.Context, args ...string) error {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	_, err := h.run(ctx, "systemctl "+strings.Join(q, " "))
	return err
}

func (h *remote) AgentState(ctx context.Context) (bool, bool, error) {
	res, err := h.exec(ctx, "systemctl is-enabled gogo-agent; systemctl is-active gogo-agent")
	if err != nil {
		return false, false, err
	}
	lines := strings.Fields(res.Stdout)
	enabled := len(lines) > 0 && lines[0] == "enabled"
	active := len(lines) > 1 && lines[1] == "active"
	return enabled, active, nil
}
