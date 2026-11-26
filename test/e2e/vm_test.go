//go:build vm

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
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

	var log bytes.Buffer
	run := func(dropIn string) ([]provisioning.StepResult, error) {
		var steps []provisioning.StepResult
		p := &provisioning.Provisioner{Fleet: fl, Log: &log, SSHDDropIn: dropIn, Progress: func(r provisioning.StepResult) { steps = append(steps, r) }}
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

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
