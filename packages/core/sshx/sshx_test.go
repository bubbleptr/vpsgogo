package sshx

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// syncBuffer is a bytes.Buffer safe for the session's copy goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestExecStreamDeliversOutputUntilContextEnds(t *testing.T) {
	s := newTestServer(t)
	c, err := dialRoot(t, s, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- c.ExecStream(ctx, "lines", &out) }()
	for deadline := time.Now().Add(5 * time.Second); out.String() != "one\ntwo\n"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("output so far %q", out.String())
		}
	}
	select {
	case err := <-done:
		t.Fatalf("returned before the context ended: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecStream ignored the context")
	}
}

func TestExecStreamReturnsWhenTheCommandExits(t *testing.T) {
	s := newTestServer(t)
	c, err := dialRoot(t, s, s.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out syncBuffer
	if err := c.ExecStream(context.Background(), "echo hi", &out); err != nil || out.String() != "hi\n" {
		t.Fatalf("echo: err=%v out=%q", err, out.String())
	}
	err = c.ExecStream(context.Background(), "fail", &out)
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "3") {
		t.Fatalf("fail: err = %v, want exit 3 with its stderr", err)
	}
}
