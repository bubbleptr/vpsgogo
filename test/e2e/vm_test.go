//go:build vm

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/provisioning"
	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
	"github.com/bubbleptr/vpsgogo/packages/core/vault"
	"golang.org/x/crypto/ssh"
)

const vmName = "vpsgogo-fresh"

func limactl(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("limactl", args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustLimactl(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := limactl(t, stdin, args...)
	if err != nil {
		t.Fatalf("limactl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// vmShell runs a root shell command in the VM through lima's own access,
// independent of the code under test.
func vmShell(t *testing.T, script string) (string, error) {
	t.Helper()
	return limactl(t, "", "shell", vmName, "sudo", "sh", "-c", script)
}

// freshVM recreates the VM (lima cannot snapshot vz VMs), sets a random root
// password and plants a root SSH key, like a provider that offers both.
func freshVM(t *testing.T) (port int, password string, rootKey ssh.AuthMethod) {
	t.Helper()
	if os.Getenv("VPSGOGO_REUSE_VM") == "" {
		limactl(t, "", "delete", "-f", vmName)
		mustLimactl(t, "", "start", "--tty=false", "--name", vmName, "lima/fresh-vps.yaml")
		// Keep a failed run's VM for inspection; don't leave a passing one running.
		t.Cleanup(func() {
			if !t.Failed() {
				limactl(t, "", "delete", "-f", vmName)
			}
		})
	}
	port, err := strconv.Atoi(mustLimactl(t, "", "list", "--format", "{{.SSHLocalPort}}", vmName))
	if err != nil {
		t.Fatal(err)
	}

	b := make([]byte, 12)
	rand.Read(b)
	password = "pw-" + hex.EncodeToString(b)
	mustLimactl(t, "root:"+password+"\n", "shell", vmName, "sudo", "chpasswd")

	key, err := sshx.GenerateKey("e2e-root")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := vmShell(t, "install -d -m 700 /root/.ssh && echo '"+key.AuthorizedKey+"' >> /root/.ssh/authorized_keys"); err != nil {
		t.Fatalf("plant root key: %v %s", err, out)
	}
	rootKey, _ = sshx.PrivateKey(key.PrivatePEM)
	return port, password, rootKey
}

// dialOK logs in and out, retrying while sshd is still restarting. Its error
// is final: nil, an auth refusal, or something else after 15 s.
func dialOK(ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := sshx.Dial(ctx, sshx.Target{Host: srv.Host, Port: srv.Port, User: user}, auth, srv.HostKeyFP)
		if err == nil {
			c.Close()
			return nil
		}
		if sshx.IsAuthError(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// refused fails the test unless the server refuses user's credentials.
func refused(t *testing.T, ctx context.Context, srv fleet.Server, user string, auth ssh.AuthMethod, what string) {
	t.Helper()
	if err := dialOK(ctx, srv, user, auth); !sshx.IsAuthError(err) {
		t.Fatalf("%s: want an auth refusal, got %v", what, err)
	}
}

func TestProvisionFreshVPS(t *testing.T) {
	ctx := context.Background()
	port, password, rootKey := freshVM(t)

	fp, err := sshx.Probe(ctx, "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	dbDir := t.TempDir()
	repo, err := fleet.OpenSQLite(ctx, filepath.Join(dbDir, "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	fl := fleet.NewService(repo, vault.NewMemory())
	srv, err := fl.Add(ctx, fleet.NewServer{Host: "127.0.0.1", Port: port, Provider: "lima"}, fp)
	if err != nil {
		t.Fatal(err)
	}

	// The agent alerts a webhook on this machine, which the VM reaches as
	// host.lima.internal, over TLS with a CA of the test's own. Its URL
	// carries a secret, as webhook URLs often do.
	hook := startWebhook(t)
	sb := make([]byte, 12)
	rand.Read(sb)
	secret := "hook-" + hex.EncodeToString(sb)
	notifyJSON, _ := json.Marshal(notify.Config{Channels: []notify.ChannelConfig{{
		Type: "webhook", URL: fmt.Sprintf("https://host.lima.internal:%d/hook?key=%s", hook.port, secret), CA: hook.caPEM,
	}}})
	rules := monitoring.DefaultRules()
	rules.Units = []string{"vpsgogo-e2e-missing.service", "ssh.service"}
	agentJSON, _ := json.Marshal(map[string]any{"name": "lima-vps", "rules": rules})
	agentBin := buildAgent(t)
	install := &provisioning.AgentInstall{Binary: agentBin, Config: agentJSON, Notify: notifyJSON}

	var log bytes.Buffer
	run := func(dropIn string) ([]provisioning.StepResult, error) {
		var steps []provisioning.StepResult
		p := &provisioning.Provisioner{Fleet: fl, Log: &log, SSHDDropIn: dropIn, Agent: install, Progress: func(r provisioning.StepResult) { steps = append(steps, r) }}
		return steps, p.Run(ctx, srv.ID, password)
	}
	rootStillWorks := func(t *testing.T) {
		t.Helper()
		if err := dialOK(ctx, srv, "root", sshx.Password(password)); err != nil {
			t.Fatalf("root password login no longer works: %v", err)
		}
		if out, _ := vmShell(t, "test -e /etc/ssh/sshd_config.d/10-vpsgogo.conf && echo present || echo absent"); out != "absent" {
			t.Fatalf("drop-in after rollback: %s", out)
		}
	}

	t.Run("precondition: a fresh VPS", func(t *testing.T) {
		if err := dialOK(ctx, srv, "root", sshx.Password(password)); err != nil {
			t.Fatalf("root password: %v", err)
		}
		if err := dialOK(ctx, srv, "root", rootKey); err != nil {
			t.Fatalf("root key: %v", err)
		}
		if _, err := vmShell(t, "id gogo"); err == nil {
			t.Fatal("gogo already exists; the VM is not fresh")
		}
	})

	t.Run("sshd rejecting the config rolls back without reload", func(t *testing.T) {
		_, err := run("NotAnOption yes\n")
		if !errors.Is(err, provisioning.ErrSSHDConfigInvalid) {
			t.Fatalf("err = %v", err)
		}
		rootStillWorks(t)
	})

	t.Run("a config that locks VPSGoGo out rolls back", func(t *testing.T) {
		// Hardened in every other way, so only the lockout can fail it.
		lockout := "PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\nAllowUsers nobody\n"
		_, err := run(lockout)
		if !errors.Is(err, provisioning.ErrSSHDRolledBack) || !sshx.IsAuthError(err) {
			t.Fatalf("err = %v, want a rollback caused by an auth refusal", err)
		}
		rootStillWorks(t)
	})

	t.Run("provision", func(t *testing.T) {
		steps, err := run("")
		t.Logf("steps: %+v", steps)
		if err != nil {
			t.Fatalf("%v\n--- log tail ---\n%s", err, tail(log.String(), 3000))
		}
		refused(t, ctx, srv, "root", sshx.Password(password), "root password login")
		refused(t, ctx, srv, "root", rootKey, "root key login")
		pem, err := fl.PrivateKey(srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		gogo, _ := sshx.PrivateKey(pem)
		c, err := sshx.Dial(ctx, sshx.Target{Host: srv.Host, Port: srv.Port, User: "gogo"}, gogo, fp)
		if err != nil {
			t.Fatalf("gogo key login: %v", err)
		}
		defer c.Close()
		if res, err := c.Exec(ctx, "sudo -n true"); err != nil || res.Code != 0 {
			t.Fatalf("gogo sudo: %+v %v", res, err)
		}
		for _, check := range []string{
			`/usr/sbin/sshd -T | grep -qx 'passwordauthentication no'`,
			`/usr/sbin/sshd -T | grep -qx 'permitrootlogin no'`,
			`ufw status | grep -q 'Status: active'`,
			`fail2ban-client status sshd`,
			`grep -q 'Unattended-Upgrade "1"' /etc/apt/apt.conf.d/20auto-upgrades`,
		} {
			if out, err := vmShell(t, check); err != nil {
				t.Errorf("%s: %v %s", check, err, out)
			}
		}
		if got, _ := fl.Get(ctx, srv.ID); got.User != "gogo" {
			t.Errorf("fleet user = %q", got.User)
		}
	})

	t.Run("agent", func(t *testing.T) {
		if out, err := vmShell(t, "systemctl is-active gogo-agent"); out != "active" {
			t.Fatalf("gogo-agent is %q (%v)\n%s", out, err, mustVM(t, "journalctl -u gogo-agent --no-pager | tail -20"))
		}
		if out, _ := vmShell(t, "ps -o user= -C gogo-agent"); out != "gogo-agent" {
			t.Errorf("gogo-agent runs as %q", out)
		}
		// ss names processes only for root; seeing sshd proves it does here.
		ss := mustVM(t, "ss -lntup")
		if !strings.Contains(ss, "sshd") {
			t.Fatalf("ss -lntup shows no process names, so it cannot tell:\n%s", ss)
		}
		if strings.Contains(ss, "gogo-agent") {
			t.Errorf("gogo-agent listens on a port:\n%s", ss)
		}
		if out := mustVM(t, "stat -c '%a %U:%G' /etc/gogo-agent/notify.json"); out != "640 root:gogo-agent" {
			t.Errorf("notify.json is %s", out)
		}

		// gogo reads samples over a new login, without sudo.
		pem, _ := fl.PrivateKey(srv.ID)
		gogo, _ := sshx.PrivateKey(pem)
		c, err := sshx.Dial(ctx, sshx.Target{Host: srv.Host, Port: srv.Port, User: "gogo"}, gogo, fp)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var samples []monitoring.Sample
		for deadline := time.Now().Add(45 * time.Second); ; time.Sleep(2 * time.Second) {
			res, err := c.Exec(ctx, "gogo-agent query --since 0 --json")
			if err == nil && res.Code == 0 && json.Unmarshal([]byte(res.Stdout), &samples) == nil && len(samples) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("gogo-agent query as gogo: %+v %v", res, err)
			}
		}
		vmNow, _ := strconv.ParseInt(mustVM(t, "date +%s"), 10, 64)
		latest := samples[len(samples)-1]
		if age := time.Unix(vmNow, 0).Sub(latest.Time); age < 0 || age > 30*time.Second {
			t.Errorf("latest sample is %v old (VM clock): %+v", age, latest)
		}
		if latest.MemTotal == 0 || len(latest.Disks) == 0 || latest.Uptime == 0 {
			t.Errorf("latest sample is missing readings: %+v", latest)
		}

		// The missing unit alerts the webhook; the running ssh.service doesn't.
		var got []notify.Message
		for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
			got = hook.messages()
			if len(got) > 0 || time.Now().After(deadline) {
				break
			}
		}
		if len(got) == 0 {
			t.Fatalf("no alert reached the webhook\n%s", mustVM(t, "journalctl -u gogo-agent --no-pager | tail -20"))
		}
		if m := got[0]; m.Server != "lima-vps" || !strings.Contains(m.Title, "vpsgogo-e2e-missing.service") {
			t.Errorf("alert %+v", m)
		}
		for _, m := range got {
			if strings.Contains(m.Title, "ssh.service") {
				t.Errorf("a running unit alerted: %+v", m)
			}
		}
		if q := hook.lastQuery(); q != "key="+secret {
			t.Errorf("webhook called with query %q", q)
		}
	})

	t.Run("rerun changes nothing", func(t *testing.T) {
		ufwBefore, _ := vmShell(t, "ufw status verbose")
		steps, err := run("")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range steps {
			if s.Changed {
				t.Errorf("step %s changed on rerun", s.Step)
			}
		}
		if out, _ := vmShell(t, "wc -l < /home/gogo/.ssh/authorized_keys"); out != "1" {
			t.Errorf("authorized_keys lines = %s", out)
		}
		if out, _ := vmShell(t, "ufw status | grep -Ec '^22/tcp +ALLOW +Anywhere *$'"); out != "1" {
			t.Errorf("ufw IPv4 22/tcp rules = %s", out)
		}
		if after, _ := vmShell(t, "ufw status verbose"); after != ufwBefore {
			t.Errorf("ufw changed on rerun:\n%s\n---\n%s", ufwBefore, after)
		}
	})

	t.Run("root password is stored nowhere", func(t *testing.T) {
		if !strings.Contains(log.String(), "## step detect") {
			t.Fatal("the provisioning log is empty; nothing was checked")
		}
		if strings.Contains(log.String(), password) {
			t.Fatal("root password in the provisioning log")
		}
		if strings.Contains(log.String(), secret) {
			t.Fatal("the webhook secret from notify.json is in the provisioning log")
		}
		entries, _ := os.ReadDir(dbDir)
		for _, e := range entries {
			b, _ := os.ReadFile(filepath.Join(dbDir, e.Name()))
			if bytes.Contains(b, []byte(password)) {
				t.Fatalf("root password in %s", e.Name())
			}
		}
		if len(entries) == 0 {
			t.Fatal("no database files to check")
		}
	})
}

// mustVM is vmShell that fails the test on error.
func mustVM(t *testing.T, script string) string {
	t.Helper()
	out, err := vmShell(t, script)
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return out
}

// buildAgent cross-compiles gogo-agent for the VM's architecture.
func buildAgent(t *testing.T) func(arch string) ([]byte, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "gogo-agent")
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[mustVM(t, "uname -m")]
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, "../../apps/agent/cmd/gogo-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gogo-agent: %v\n%s", err, b)
	}
	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return func(a string) ([]byte, error) {
		if a != arch {
			return nil, fmt.Errorf("built for %s, asked for %s", arch, a)
		}
		return bin, nil
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
