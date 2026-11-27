package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testServer is an in-process SSH server that understands a few commands:
// "echo hi", "fail" (stderr "boom", exit 3), "block" (waits until the
// client goes away) and "lines" (writes two lines, then blocks).
type testServer struct {
	host    string
	port    int
	hostKey ssh.Signer

	mu         sync.Mutex
	authorized []ssh.PublicKey
}

const testPassword = "root-pw"

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{hostKey: signer}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "root" && string(pw) == testPassword {
				return nil, nil
			}
			return nil, errDenied
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, k := range s.authorized {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return nil, nil
				}
			}
			return nil, errDenied
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	s.host = host
	s.port, _ = strconv.Atoi(port)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func (s *testServer) authorize(key ssh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorized = append(s.authorized, key)
}

func (s *testServer) fingerprint() string { return ssh.FingerprintSHA256(s.hostKey.PublicKey()) }

func (s *testServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				ssh.Unmarshal(req.Payload, &payload)
				req.Reply(true, nil)
				code := 0
				switch payload.Command {
				case "echo hi":
					ch.Write([]byte("hi\n"))
				case "sha256":
					h := sha256.New()
					io.Copy(h, ch)
					fmt.Fprintf(ch, "%x\n", h.Sum(nil))
				case "fail":
					ch.Stderr().Write([]byte("boom"))
					code = 3
				case "lines":
					ch.Write([]byte("one\ntwo\n"))
					for range chReqs {
					}
					return
				case "block":
					for range chReqs { // until the client closes the channel
					}
					return
				default:
					code = 127
				}
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, uint32(code))
				ch.SendRequest("exit-status", false, status)
				return
			}
		}()
	}
}

var errDenied = errors.New("denied")
