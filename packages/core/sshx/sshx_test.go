package sshx

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func dialRoot(t *testing.T, s *testServer, pin string) (*Client, error) {
	t.Helper()
	return Dial(context.Background(), Target{Host: s.host, Port: s.port, User: "root"}, ssh.Password(testPassword), pin)
}

func TestProbeReturnsServerFingerprint(t *testing.T) {
	s := newTestServer(t)
	fp, err := Probe(context.Background(), s.host, s.port)
	if err != nil {
		t.Fatal(err)
	}
	if fp != s.fingerprint() {
		t.Fatalf("fingerprint = %q, want %q", fp, s.fingerprint())
	}
}

func TestExecWithPinnedHost(t *testing.T) {
	s := newTestServer(t)
	c, err := dialRoot(t, s, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Exec(context.Background(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hi\n" || res.Code != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestExecNonZeroExitIsAResultNotAnError(t *testing.T) {
	s := newTestServer(t)
	c, err := dialRoot(t, s, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Exec(context.Background(), "fail")
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 3 || res.Stderr != "boom" {
		t.Fatalf("got %+v", res)
	}
}

func TestDialRejectsChangedHostKey(t *testing.T) {
	s := newTestServer(t)
	other := newTestServer(t)

	_, err := dialRoot(t, s, other.fingerprint())
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want ErrHostKeyMismatch", err)
	}
}

func TestDialRefusesUnpinnedHost(t *testing.T) {
	s := newTestServer(t)
	_, err := dialRoot(t, s, "")
	if err == nil {
		t.Fatal("dialed without a pinned fingerprint")
	}
}

func TestWrongPasswordIsNotAHostKeyMismatch(t *testing.T) {
	s := newTestServer(t)
	_, err := Dial(context.Background(), Target{Host: s.host, Port: s.port, User: "root"}, ssh.Password("nope"), s.fingerprint())
	if err == nil || errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want an auth error", err)
	}
}

func TestGeneratedKeyLogsIn(t *testing.T) {
	s := newTestServer(t)
	key, err := GenerateKey("vpsgogo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key.AuthorizedKey, "ssh-ed25519 ") || !strings.HasSuffix(key.AuthorizedKey, " vpsgogo") {
		t.Fatalf("authorized key line = %q", key.AuthorizedKey)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.AuthorizedKey))
	if err != nil {
		t.Fatal(err)
	}
	s.authorize(pub)

	auth, err := PrivateKey(key.PrivatePEM)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Dial(context.Background(), Target{Host: s.host, Port: s.port, User: "gogo"}, auth, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestExecStopsWhenContextEnds(t *testing.T) {
	s := newTestServer(t)
	c, err := dialRoot(t, s, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, "block")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec ignored the context")
	}
}

func TestAuthorizedKeyMatchesGeneratedKey(t *testing.T) {
	key, err := GenerateKey("vpsgogo")
	if err != nil {
		t.Fatal(err)
	}
	line, err := AuthorizedKey(key.PrivatePEM, "vpsgogo")
	if err != nil {
		t.Fatal(err)
	}
	if line != key.AuthorizedKey {
		t.Fatalf("AuthorizedKey = %q, want %q", line, key.AuthorizedKey)
	}
}

func TestIsAuthError(t *testing.T) {
	s := newTestServer(t)
	_, err := Dial(context.Background(), Target{Host: s.host, Port: s.port, User: "root"}, ssh.Password("nope"), s.fingerprint())
	if !IsAuthError(err) {
		t.Fatalf("wrong password: IsAuthError(%v) = false", err)
	}
	_, err = dialRoot(t, s, newTestServer(t).fingerprint())
	if IsAuthError(err) {
		t.Fatalf("host key mismatch counted as auth error: %v", err)
	}
	_, err = Dial(context.Background(), Target{Host: "127.0.0.1", Port: 1, User: "root"}, ssh.Password("x"), "SHA256:x")
	if IsAuthError(err) {
		t.Fatalf("refused connection counted as auth error: %v", err)
	}
}

func TestExecInputStreamsLargeStdin(t *testing.T) {
	s := newTestServer(t)
	c, err := dialRoot(t, s, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Larger than any command line may be: a gogo-agent binary.
	data := make([]byte, 8<<20)
	rand.Read(data)
	res, err := c.ExecInput(context.Background(), "sha256", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%x\n", sha256.Sum256(data)); res.Stdout != want || res.Code != 0 {
		t.Fatalf("got %+v, want stdout %q", res, want)
	}
}
