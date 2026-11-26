// Package provisioning takes a freshly bought server to the state VPSGoGo
// manages: a gogo user with key login and sudo, hardened sshd, firewall,
// fail2ban, automatic security updates and gogo-agent. Every step is
// idempotent.
// See docs/SPEC.md §4.1.
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
	"github.com/bubbleptr/vpsgogo/packages/core/vault"
	"golang.org/x/crypto/ssh"
)

// OpsUser is the user VPSGoGo logs in as once a server is provisioned.
const OpsUser = "gogo"

// StepResult reports one finished step.
type StepResult struct {
	Step    string `json:"step"`
	Changed bool   `json:"changed"`
	Error   string `json:"error,omitempty"`
}

// Provisioner runs the pipeline against servers of a fleet.
type Provisioner struct {
	Fleet *fleet.Service
	// Log receives every command and its output. Defaults to io.Discard.
	Log io.Writer
	// Progress is called after each step. Optional.
	Progress func(StepResult)
	// SSHDDropIn replaces the hardened sshd drop-in; only tests set it.
	SSHDDropIn string
	// Dial connects to a server; defaults to sshx.Dial with the pinned key.
	Dial func(ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod) (Session, error)
	// Agent, when set, installs gogo-agent as the last step.
	Agent *AgentInstall
}

// agentHealthTime is how long the agent may take to answer with a sample:
// its first reading is a baseline, the second (10s later) its first sample.
const agentHealthTime = 60 * time.Second

// settleTime is how long a new connection may keep failing after sshd or the
// firewall changed before VPSGoGo concludes it is locked out.
const settleTime = 15 * time.Second

var basePackages = []string{"sudo", "unattended-upgrades", "ufw", "fail2ban", "python3-systemd"}

const autoUpgradesPath = "/etc/apt/apt.conf.d/20auto-upgrades"

const autoUpgrades = `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
`

const fail2banJailPath = "/etc/fail2ban/jail.d/vpsgogo.conf"

// Run provisions server id. rootPassword is used only while the server still
// logs in as root (the first run); later runs log in as gogo with its key.
func (p *Provisioner) Run(ctx context.Context, id, rootPassword string) error {
	log := p.Log
	if log == nil {
		log = io.Discard
	}
	dial := p.Dial
	if dial == nil {
		dial = func(ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod) (Session, error) {
			return sshx.Dial(ctx, sshx.Target{Host: srv.Host, Port: srv.Port, User: user}, auth, srv.HostKeyFP)
		}
	}
	// settled retries a dial for a while when the connection itself fails:
	// right after a reload sshd re-executes and resets new connections.
	// Refused credentials and host key mismatches are final.
	settled := func(ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod) (Session, error) {
		deadline := time.Now().Add(settleTime)
		for {
			s, err := dial(ctx, srv, user, auth)
			if err == nil || sshx.IsAuthError(err) || errors.Is(err, sshx.ErrHostKeyMismatch) || time.Now().After(deadline) {
				return s, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	dropIn := p.SSHDDropIn
	if dropIn == "" {
		dropIn = sshdDropIn
	}
	step := func(name string, f func() (bool, error)) error {
		changed, err := f()
		r := StepResult{Step: name, Changed: changed}
		if err != nil {
			r.Error = err.Error()
			err = fmt.Errorf("provisioning: step %s: %w", name, err)
		}
		fmt.Fprintf(log, "## step %s changed=%v err=%v\n", name, changed, err)
		if p.Progress != nil {
			p.Progress(r)
		}
		return err
	}

	srv, err := p.Fleet.Get(ctx, id)
	if err != nil {
		return err
	}

	// Connect: as root with the password on the first run, as gogo after.
	asRoot := srv.User == "root"
	var first Session
	if asRoot {
		first, err = dial(ctx, srv, "root", sshx.Password(rootPassword))
	} else {
		var pem []byte
		if pem, err = p.Fleet.PrivateKey(id); err == nil {
			var auth ssh.AuthMethod
			if auth, err = sshx.PrivateKey(pem); err == nil {
				first, err = dial(ctx, srv, srv.User, auth)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("provisioning: connect: %w", err)
	}
	defer func() { first.Close() }()
	h := &remote{s: first, root: asRoot, log: log}

	if err := step("detect", func() (bool, error) {
		out, err := h.run(ctx, "cat /etc/os-release")
		if err != nil {
			return false, err
		}
		return false, parseOSRelease(out).supported()
	}); err != nil {
		return err
	}

	if err := step("packages", func() (bool, error) { return ensurePackages(ctx, h, basePackages) }); err != nil {
		return err
	}

	// The key: reuse the stored one so reruns don't pile up authorized keys.
	pem, err := p.Fleet.PrivateKey(id)
	if err != nil && !errors.Is(err, vault.ErrNotFound) {
		// Not proof the key is gone; replacing it could lock VPSGoGo out.
		return fmt.Errorf("provisioning: read the stored key: %w", err)
	}
	if err != nil {
		key, gerr := sshx.GenerateKey("vpsgogo")
		if gerr != nil {
			return gerr
		}
		if err := p.Fleet.SetPrivateKey(id, key.PrivatePEM); err != nil {
			return err
		}
		pem = key.PrivatePEM
	}
	line, err := sshx.AuthorizedKey(pem, "vpsgogo")
	if err != nil {
		return err
	}
	opsAuth, err := sshx.PrivateKey(pem)
	if err != nil {
		return err
	}

	if err := step("user", func() (bool, error) { return ensureOpsUser(ctx, h, line) }); err != nil {
		return err
	}

	// From here on work as gogo, proven to log in and sudo. This session
	// stays open through sshd and firewall changes as the way back.
	ops, err := settled(ctx, srv, OpsUser, opsAuth)
	if err != nil {
		return fmt.Errorf("provisioning: %s cannot log in with its key: %w", OpsUser, err)
	}
	defer func() { ops.Close() }()
	h = &remote{s: ops, root: false, log: log}
	if _, err := h.run(ctx, "true"); err != nil {
		return fmt.Errorf("provisioning: %s cannot sudo: %w", OpsUser, err)
	}
	if asRoot {
		if err := p.Fleet.SetUser(ctx, id, OpsUser); err != nil {
			return err
		}
		first.Close()
	}

	// verify proves over a new connection that gogo still gets in and that
	// sshd's effective configuration is the hardened one.
	verify := func(ctx context.Context) error {
		s, err := settled(ctx, srv, OpsUser, opsAuth)
		if err != nil {
			return fmt.Errorf("new %s login: %w", OpsUser, err)
		}
		defer s.Close()
		cfg, err := (&remote{s: s, log: log}).sshdEffective(ctx)
		if err != nil {
			return err
		}
		return checkHardened(cfg)
	}

	if err := step("ssh", func() (bool, error) { return hardenSSH(ctx, h, dropIn, verify) }); err != nil {
		return err
	}

	cfg, err := h.sshdEffective(ctx)
	if err != nil {
		return err
	}
	sshPort, err := strconv.Atoi(cfg["port"])
	if err != nil {
		return fmt.Errorf("provisioning: sshd port %q: %w", cfg["port"], err)
	}

	if err := step("firewall", func() (bool, error) { return ensureFirewall(ctx, h, sshPort, verify) }); err != nil {
		return err
	}
	if err := step("fail2ban", func() (bool, error) { return ensureFail2ban(ctx, h, sshPort) }); err != nil {
		return err
	}
	if err := step("updates", func() (bool, error) { return ensureFile(ctx, h, autoUpgradesPath, autoUpgrades) }); err != nil {
		return err
	}
	if p.Agent == nil {
		return nil
	}
	// healthy asks the agent for samples as gogo would: over a new
	// connection, which picks up gogo's new gogo-agent group, and without
	// sudo.
	healthy := func(ctx context.Context) error {
		s, err := settled(ctx, srv, OpsUser, opsAuth)
		if err != nil {
			return err
		}
		defer s.Close()
		deadline := time.Now().Add(agentHealthTime)
		for {
			res, err := s.Exec(ctx, "gogo-agent query --since 0 --json")
			fmt.Fprintf(log, "$ gogo-agent query --since 0 --json  (as %s)\n[exit %d]\n%s%s", OpsUser, res.Code, clip(res.Stdout), clip(res.Stderr))
			if err == nil && res.Code == 0 && strings.HasPrefix(strings.TrimSpace(res.Stdout), "[{") {
				return nil
			}
			if time.Now().After(deadline) {
				if err == nil {
					err = fmt.Errorf("query exited %d: %s", res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
				}
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return step("agent", func() (bool, error) { return ensureAgent(ctx, h, *p.Agent, healthy) })
}

// checkHardened looks at sshd -T output (keys are lower case).
func checkHardened(cfg map[string]string) error {
	var bad []string
	for k, want := range map[string]string{
		"passwordauthentication":       "no",
		"kbdinteractiveauthentication": "no",
		"permitrootlogin":              "no",
	} {
		if got := cfg[k]; got != want {
			bad = append(bad, fmt.Sprintf("%s is %q", k, got))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("sshd is not hardened (an earlier drop-in may override VPSGoGo's): %s", strings.Join(bad, ", "))
	}
	return nil
}

func ensurePackages(ctx context.Context, h *remote, pkgs []string) (bool, error) {
	list := strings.Join(pkgs, " ")
	out, _ := h.run(ctx, "dpkg-query -W -f='${db:Status-Status}\\n' "+list+" 2>/dev/null")
	if strings.Count(out, "installed\n") == len(pkgs) && !strings.Contains(out, "not-installed") {
		return false, nil
	}
	_, err := h.run(ctx, "export DEBIAN_FRONTEND=noninteractive; "+
		"apt-get -o DPkg::Lock::Timeout=300 update -q && "+
		"apt-get -o DPkg::Lock::Timeout=300 install -y -q --no-install-recommends "+list)
	return err == nil, err
}

func ensureOpsUser(ctx context.Context, h *remote, authorizedLine string) (bool, error) {
	k := shellQuote(authorizedLine)
	sudoers := shellQuote(OpsUser + " ALL=(ALL) NOPASSWD:ALL")
	out, err := h.run(ctx, `set -e
c=0
id -u `+OpsUser+` >/dev/null 2>&1 || { useradd -m -s /bin/bash `+OpsUser+`; c=1; }
install -d -m 700 -o `+OpsUser+` -g `+OpsUser+` /home/`+OpsUser+`/.ssh
f=/home/`+OpsUser+`/.ssh/authorized_keys
if ! grep -qxF `+k+` "$f" 2>/dev/null; then printf '%s\n' `+k+` >> "$f"; c=1; fi
chown `+OpsUser+`:`+OpsUser+` "$f"; chmod 600 "$f"
s=/etc/sudoers.d/90-vpsgogo
if [ "$(cat "$s" 2>/dev/null)" != `+sudoers+` ]; then
  printf '%s\n' `+sudoers+` > "$s.tmp"; chmod 440 "$s.tmp"; visudo -cf "$s.tmp" >/dev/null; mv "$s.tmp" "$s"; c=1
fi
echo "changed=$c"`)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "changed=1"), nil
}

func ensureFail2ban(ctx context.Context, h *remote, sshPort int) (bool, error) {
	jail := fmt.Sprintf("# Managed by VPSGoGo.\n[sshd]\nenabled = true\nport = %d\nbackend = systemd\nmaxretry = 5\n", sshPort)
	changed, err := ensureFile(ctx, h, fail2banJailPath, jail)
	if err != nil {
		return false, err
	}
	restart := "systemctl is-active --quiet fail2ban || systemctl restart fail2ban"
	if changed {
		restart = "systemctl restart fail2ban"
	}
	_, err = h.run(ctx, "systemctl enable --quiet fail2ban && "+restart+` &&
for i in $(seq 1 30); do fail2ban-client status sshd >/dev/null 2>&1 && exit 0; sleep 1; done
fail2ban-client status sshd`)
	if err != nil {
		return changed, errors.Join(errors.New("fail2ban's sshd jail is not running"), err)
	}
	return changed, nil
}
