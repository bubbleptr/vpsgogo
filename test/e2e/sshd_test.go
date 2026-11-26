//go:build integration

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/sshx"
)

// freshVPS is a running sshd container that accepts root's password.
type freshVPS struct {
	host     string
	port     int
	password string
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func startFreshVPS(t *testing.T, base string) freshVPS {
	t.Helper()
	image := "vpsgogo-sshd:" + strings.NewReplacer(":", "-", ".", "-").Replace(base)
	docker(t, "build", "-q", "-t", image, "--build-arg", "BASE="+base, "sshd")

	pw := make([]byte, 12)
	rand.Read(pw)
	password := hex.EncodeToString(pw)
	id := docker(t, "run", "-d", "-e", "ROOT_PASSWORD="+password, "-p", "127.0.0.1::22", image)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })

	_, portStr, err := net.SplitHostPort(strings.Split(docker(t, "port", id, "22/tcp"), "\n")[0])
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return freshVPS{host: "127.0.0.1", port: port, password: password}
}

// probe waits for sshd to come up and returns its host key fingerprint.
func probe(t *testing.T, v freshVPS) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		fp, err := sshx.Probe(context.Background(), v.host, v.port)
		if err == nil {
			return fp
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd did not come up: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestFreshVPS(t *testing.T) {
	for _, base := range []string{"debian:12-slim", "ubuntu:24.04"} {
		t.Run(base, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			v := startFreshVPS(t, base)
			fp := probe(t, v)
			target := sshx.Target{Host: v.host, Port: v.port, User: "root"}

			t.Run("exec with pinned host key", func(t *testing.T) {
				c, err := sshx.Dial(ctx, target, sshx.Password(v.password), fp)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				res, err := c.Exec(ctx, "uname -a")
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("uname -a: %s", strings.TrimSpace(res.Stdout))
				if res.Code != 0 || !strings.HasPrefix(res.Stdout, "Linux ") {
					t.Fatalf("got %+v", res)
				}
			})

			t.Run("tampered fingerprint is refused", func(t *testing.T) {
				tampered := fp[:len(fp)-1] + flip(fp[len(fp)-1])
				_, err := sshx.Dial(ctx, target, sshx.Password(v.password), tampered)
				if !errors.Is(err, sshx.ErrHostKeyMismatch) {
					t.Fatalf("err = %v, want ErrHostKeyMismatch", err)
				}
			})
		})
	}
}

func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
