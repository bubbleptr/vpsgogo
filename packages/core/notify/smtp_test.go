package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server. It offers STARTTLS on a plain listener
// (unless noStartTLS) or speaks TLS from the first byte (implicit).
type fakeSMTP struct {
	ln         net.Listener
	tlsConf    *tls.Config
	implicit   bool
	noStartTLS bool
	wg         sync.WaitGroup

	mu       sync.Mutex
	cmds     []string // verbs received, in order
	authUser string
	authPass string
	from     string
	rcpts    []string
	data     string
}

func newFakeSMTP(t *testing.T, implicit, noStartTLS bool) (*fakeSMTP, string) {
	t.Helper()
	// Borrow httptest's certificate (valid for 127.0.0.1) and hand its PEM to the channel as CA.
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{
		ln:         ln,
		tlsConf:    &tls.Config{Certificates: certSrv.TLS.Certificates},
		implicit:   implicit,
		noStartTLS: noStartTLS,
	}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() { ln.Close(); f.wg.Wait() })
	return f, caPEM(certSrv)
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			defer c.Close()
			c.SetDeadline(time.Now().Add(10 * time.Second))
			f.session(c)
		}()
	}
}

func (f *fakeSMTP) record(verb string) {
	f.mu.Lock()
	f.cmds = append(f.cmds, verb)
	f.mu.Unlock()
}

func (f *fakeSMTP) session(c net.Conn) {
	secure := false
	if f.implicit {
		tc := tls.Server(c, f.tlsConf)
		if tc.Handshake() != nil {
			return
		}
		c, secure = tc, true
	}
	tp := textproto.NewConn(c)
	tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		f.record(verb)
		switch verb {
		case "EHLO", "HELO":
			exts := []string{"250-fake", "250-8BITMIME"}
			if !secure && !f.noStartTLS {
				exts = append(exts, "250-STARTTLS")
			}
			exts = append(exts, "250 AUTH PLAIN")
			for _, e := range exts {
				tp.PrintfLine("%s", e)
			}
		case "STARTTLS":
			if secure || f.noStartTLS {
				tp.PrintfLine("502 no")
				continue
			}
			tp.PrintfLine("220 go ahead")
			tc := tls.Server(c, f.tlsConf)
			if tc.Handshake() != nil {
				return
			}
			c, secure = tc, true
			tp = textproto.NewConn(c)
		case "AUTH":
			mech, ir, _ := strings.Cut(arg, " ")
			raw, err := base64.StdEncoding.DecodeString(ir)
			if strings.ToUpper(mech) != "PLAIN" || err != nil {
				tp.PrintfLine("504 unsupported")
				continue
			}
			parts := strings.Split(string(raw), "\x00")
			if len(parts) != 3 {
				tp.PrintfLine("501 bad")
				continue
			}
			f.mu.Lock()
			f.authUser, f.authPass = parts[1], parts[2]
			f.mu.Unlock()
			tp.PrintfLine("235 ok")
		case "MAIL":
			f.mu.Lock()
			f.from = arg
			f.mu.Unlock()
			tp.PrintfLine("250 ok")
		case "RCPT":
			f.mu.Lock()
			f.rcpts = append(f.rcpts, arg)
			f.mu.Unlock()
			tp.PrintfLine("250 ok")
		case "DATA":
			tp.PrintfLine("354 send")
			b, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.data = string(b)
			f.mu.Unlock()
			tp.PrintfLine("250 queued")
		case "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("250 ok")
		}
	}
}

func (f *fakeSMTP) snapshot() (cmds []string, user, pass, from string, rcpts []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...), f.authUser, f.authPass, f.from, append([]string(nil), f.rcpts...), f.data
}

func smtpConfig(port int, ca string) ChannelConfig {
	return ChannelConfig{
		Type: "smtp", Host: "127.0.0.1", Port: port, CA: ca,
		Username: "alerts", Password: "hunter2",
		From: "alerts@example.com", To: []string{"me@example.com", "ops@example.com"},
	}
}

func TestSMTPStartTLSDelivers(t *testing.T) {
	srv, ca := newFakeSMTP(t, false, false)
	ch := build1(t, smtpConfig(srv.port(), ca))

	if err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	srv.ln.Close()
	srv.wg.Wait()
	cmds, user, pass, from, rcpts, data := srv.snapshot()

	if i, j := indexOf(cmds, "STARTTLS"), indexOf(cmds, "AUTH"); i < 0 || j < i {
		t.Errorf("AUTH must come after STARTTLS; commands: %v", cmds)
	}
	if user != "alerts" || pass != "hunter2" {
		t.Errorf("auth = %q/%q", user, pass)
	}
	if !strings.HasPrefix(from, "FROM:<alerts@example.com>") {
		t.Errorf("MAIL %q", from)
	}
	if len(rcpts) != 2 || !strings.Contains(rcpts[1], "ops@example.com") {
		t.Errorf("RCPT %v", rcpts)
	}

	m, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parse message %q: %v", data, err)
	}
	subj, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[VPSGoGo] tokyo-1: Alert: disk / 91%"; subj != want {
		t.Errorf("Subject = %q, want %q", subj, want)
	}
	if ct := m.Header.Get("Content-Type"); !strings.EqualFold(ct, "text/plain; charset=utf-8") {
		t.Errorf("Content-Type = %q", ct)
	}
	if _, err := m.Header.Date(); err != nil {
		t.Errorf("Date header: %v", err)
	}
	if got := m.Header.Get("From"); got != "alerts@example.com" {
		t.Errorf("From = %q", got)
	}
	if got := m.Header.Get("To"); !strings.Contains(got, "me@example.com") || !strings.Contains(got, "ops@example.com") {
		t.Errorf("To = %q", got)
	}
	body, _ := io.ReadAll(m.Body)
	if got := strings.TrimRight(string(body), "\r\n"); got != msg.Text {
		t.Errorf("body = %q, want %q", got, msg.Text)
	}
}

func TestSMTPRefusesServerWithoutStartTLS(t *testing.T) {
	srv, ca := newFakeSMTP(t, false, true)
	ch := build1(t, smtpConfig(srv.port(), ca))

	if err := ch.Send(context.Background(), msg); err == nil {
		t.Fatal("Send succeeded over a connection without STARTTLS")
	}
	srv.ln.Close()
	srv.wg.Wait()
	cmds, user, pass, _, _, _ := srv.snapshot()
	for _, verb := range []string{"AUTH", "MAIL", "RCPT", "DATA"} {
		if indexOf(cmds, verb) >= 0 {
			t.Errorf("sent %s in clear text; commands: %v", verb, cmds)
		}
	}
	if user != "" || pass != "" {
		t.Error("credentials reached the server")
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	srv, ca := newFakeSMTP(t, true, false)
	ch := build1(t, smtpConfig(465, ca))
	sc := ch.(*smtpChannel)
	var dialed string
	sc.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		var d net.Dialer
		return d.DialContext(ctx, network, srv.ln.Addr().String())
	}

	if err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if dialed != "127.0.0.1:465" {
		t.Errorf("dialed %q", dialed)
	}
	srv.ln.Close()
	srv.wg.Wait()
	cmds, user, _, _, _, data := srv.snapshot()
	if indexOf(cmds, "STARTTLS") >= 0 {
		t.Errorf("STARTTLS on an implicit-TLS connection: %v", cmds)
	}
	if user != "alerts" || !strings.Contains(data, msg.Text) {
		t.Errorf("message not delivered: user %q data %q", user, data)
	}
}

func TestSMTPUntrustedCertFails(t *testing.T) {
	srv, _ := newFakeSMTP(t, false, false)
	ch := build1(t, smtpConfig(srv.port(), ""))
	if err := ch.Send(context.Background(), msg); err == nil {
		t.Fatal("Send succeeded with an untrusted server certificate")
	}
	srv.ln.Close()
	srv.wg.Wait()
	if cmds, user, _, _, _, _ := srv.snapshot(); user != "" || indexOf(cmds, "MAIL") >= 0 {
		t.Errorf("mail sent despite failed verification: %v", cmds)
	}
}

func TestSMTPSubjectCRLFInjection(t *testing.T) {
	srv, ca := newFakeSMTP(t, false, false)
	ch := build1(t, smtpConfig(srv.port(), ca))

	m := msg
	m.Server = "evil\r\nBcc: victim@example.com"
	m.Title = "x\nX-Injected: yes"
	if err := ch.Send(context.Background(), m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	srv.ln.Close()
	srv.wg.Wait()
	_, _, _, _, rcpts, data := srv.snapshot()

	parsed, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Header.Get("Bcc") != "" || parsed.Header.Get("X-Injected") != "" {
		t.Errorf("header injected:\n%s", data)
	}
	if len(rcpts) != 2 {
		t.Errorf("RCPT %v", rcpts)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if !strings.Contains(subj, "Bcc: victim@example.com") || strings.ContainsAny(subj, "\r\n") {
		t.Errorf("Subject = %q", subj)
	}
}

func TestSMTPEncodesNonASCIISubject(t *testing.T) {
	srv, ca := newFakeSMTP(t, false, false)
	ch := build1(t, smtpConfig(srv.port(), ca))
	m := msg
	m.Server = "东京-1"
	if err := ch.Send(context.Background(), m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	srv.ln.Close()
	srv.wg.Wait()
	_, _, _, _, _, data := srv.snapshot()
	parsed, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	subj, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subj != "[VPSGoGo] 东京-1: Alert: disk / 91%" {
		t.Errorf("Subject = %q (%v)", subj, err)
	}
}

func TestSMTPRespectsContext(t *testing.T) {
	// A server that accepts but never greets.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			io.Copy(io.Discard, c)
		}
	}()
	ch := build1(t, smtpConfig(ln.Addr().(*net.TCPAddr).Port, ""))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = ch.Send(ctx, msg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("Send took %v after the deadline", time.Since(start))
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
