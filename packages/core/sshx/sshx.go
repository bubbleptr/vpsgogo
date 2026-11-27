// Package sshx connects to servers over SSH with a pinned host key and runs
// commands on them. See docs/SPEC.md §4.1 and §5.1.
package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch means the server's host key is not the pinned one:
// the server was reinstalled, or someone is in the middle.
var ErrHostKeyMismatch = errors.New("sshx: host key does not match the pinned fingerprint")

const dialTimeout = 15 * time.Second

// Target is where to connect and as whom.
type Target struct {
	Host string
	Port int
	User string
}

func (t Target) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// Client is an authenticated connection to one server.
type Client struct {
	conn *ssh.Client
}

// ExecResult is the outcome of a command that ran to completion. A non-zero
// Code is a result, not an error.
type ExecResult struct {
	Stdout string
	Stderr string
	Code   int
}

// Probe connects without authenticating and returns the server's host key
// fingerprint (SHA256:…), for the user to confirm before it is pinned.
func Probe(ctx context.Context, host string, port int) (string, error) {
	var fp string
	errSeen := errors.New("host key seen")
	cfg := &ssh.ClientConfig{
		User: "probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp = ssh.FingerprintSHA256(key)
			return errSeen
		},
		Timeout: dialTimeout,
	}
	conn, err := dialConn(ctx, Target{Host: host, Port: port}.addr(), cfg)
	if conn != nil {
		conn.Close()
	}
	if fp != "" {
		return fp, nil
	}
	return "", fmt.Errorf("sshx: probe %s: %w", host, err)
}

// Dial connects to t, accepting only a server whose host key has the
// fingerprint pin. An empty pin is refused: every host is confirmed first
// with Probe.
func Dial(ctx context.Context, t Target, auth ssh.AuthMethod, pin string) (*Client, error) {
	if pin == "" {
		return nil, errors.New("sshx: refusing to connect without a pinned host key")
	}
	var mismatch error
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != pin {
				mismatch = fmt.Errorf("%w: got %s, pinned %s", ErrHostKeyMismatch, got, pin)
				return mismatch
			}
			return nil
		},
		Timeout: dialTimeout,
	}
	conn, err := dialConn(ctx, t.addr(), cfg)
	if mismatch != nil {
		return nil, mismatch
	}
	if err != nil {
		return nil, fmt.Errorf("sshx: connect %s@%s: %w", t.User, t.addr(), err)
	}
	return &Client{conn: conn}, nil
}

func dialConn(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: cfg.Timeout}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(cfg.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	nc.SetDeadline(deadline)
	c, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// Exec runs cmd and waits for it. When ctx ends first, the session is closed
// and ctx's error returned.
func (c *Client) Exec(ctx context.Context, cmd string) (ExecResult, error) {
	return c.ExecInput(ctx, cmd, nil)
}

// ExecInput is Exec with stdin streamed from in, for content too large or too
// secret for a command line, such as a binary or a file holding tokens.
// A nil in gives the command an empty stdin.
func (c *Client) ExecInput(ctx context.Context, cmd string, in io.Reader) (ExecResult, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("sshx: new session: %w", err)
	}
	defer sess.Close()

	var stdout, stderr bytes.Buffer
	sess.Stdin = in
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if err := sess.Start(cmd); err != nil {
		return ExecResult{}, fmt.Errorf("sshx: start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

	select {
	case <-ctx.Done():
		sess.Close()
		return ExecResult{}, ctx.Err()
	case err := <-done:
		res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
		var exit *ssh.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exit):
			res.Code = exit.ExitStatus()
		default:
			return res, fmt.Errorf("sshx: %q: %w", cmd, err)
		}
		return res, nil
	}
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Key is a freshly generated ed25519 key pair.
type Key struct {
	PrivatePEM    []byte // OpenSSH private key, for the vault
	AuthorizedKey string // one authorized_keys line, ending in comment
}

// GenerateKey makes an ed25519 key pair whose public half carries comment.
func GenerateKey(comment string) (Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return Key{}, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Key{}, err
	}
	return Key{PrivatePEM: pem.EncodeToMemory(block), AuthorizedKey: authorizedLine(sshPub, comment)}, nil
}

// AuthorizedKey returns the authorized_keys line for a private key from
// GenerateKey, so a stored key can be installed again.
func AuthorizedKey(privatePEM []byte, comment string) (string, error) {
	signer, err := ssh.ParsePrivateKey(privatePEM)
	if err != nil {
		return "", fmt.Errorf("sshx: parse private key: %w", err)
	}
	return authorizedLine(signer.PublicKey(), comment), nil
}

func authorizedLine(pub ssh.PublicKey, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + comment
}

// PrivateKey turns a PEM private key from GenerateKey into an AuthMethod.
func PrivateKey(privatePEM []byte) (ssh.AuthMethod, error) {
	signer, err := ssh.ParsePrivateKey(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("sshx: parse private key: %w", err)
	}
	return ssh.PublicKeys(signer), nil
}

// Password authenticates with a password, used once for a fresh server's root.
func Password(password string) ssh.AuthMethod { return ssh.Password(password) }

// IsAuthError reports whether err means the server refused the credentials,
// as opposed to an unreachable server or a host key mismatch.
func IsAuthError(err error) bool {
	return err != nil && !errors.Is(err, ErrHostKeyMismatch) && strings.Contains(err.Error(), "ssh: unable to authenticate")
}
