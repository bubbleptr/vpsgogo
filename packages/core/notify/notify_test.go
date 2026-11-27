package notify

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const secret = "123456:SECRET-bot-token"

var msg = Message{
	Server: "tokyo-1",
	Title:  "Alert: disk / 91%",
	Text:   "disk / is 91% full",
	Time:   time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC),
}

// caPEM returns the test server's certificate as a PEM bundle, the way a user
// pastes a private CA into the channel config.
func caPEM(srv *httptest.Server) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

type recorded struct {
	mu     sync.Mutex
	path   string
	header http.Header
	body   []byte
}

func (r *recorded) get() (string, http.Header, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path, r.header, r.body
}

// recordingServer answers every request with status and respBody and records the last request.
func recordingServer(t *testing.T, status int, respBody string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.path, rec.header, rec.body = r.URL.Path, r.Header.Clone(), b
		rec.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func build1(t *testing.T, cc ChannelConfig) Channel {
	t.Helper()
	chans, err := Build(Config{Channels: []ChannelConfig{cc}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(chans) != 1 {
		t.Fatalf("Build returned %d channels", len(chans))
	}
	return chans[0]
}

func TestValidate(t *testing.T) {
	const pw = "hunter2-SMTP-PASSWORD"
	smtpOK := ChannelConfig{Type: "smtp", Host: "smtp.example.com", From: "a@example.com", To: []string{"b@example.com"}, Username: "a", Password: pw}
	with := func(c ChannelConfig, f func(*ChannelConfig)) ChannelConfig { f(&c); return c }

	valid := []ChannelConfig{
		{Type: "webhook", URL: "https://hooks.example.com/x"},
		{Type: "telegram", Token: secret, ChatID: "42"},
		{Type: "ntfy", URL: "https://ntfy.sh/topic"},
		smtpOK,
		with(smtpOK, func(c *ChannelConfig) { c.Port = 465 }),
	}
	for _, cc := range valid {
		if err := (Config{Channels: []ChannelConfig{cc}}).Validate(); err != nil {
			t.Errorf("Validate(%s) = %v, want nil", cc.Type, err)
		}
	}

	invalid := map[string]ChannelConfig{
		"unknown type":      {Type: "slack", URL: "https://hooks.slack.com/x"},
		"webhook http":      {Type: "webhook", URL: "http://hooks.example.com/" + secret},
		"webhook no host":   {Type: "webhook", URL: "https:" + secret},
		"webhook missing":   {Type: "webhook"},
		"ntfy http":         {Type: "ntfy", URL: "http://ntfy.sh/topic", Token: secret},
		"telegram no token": {Type: "telegram", ChatID: "42"},
		"telegram no chat":  {Type: "telegram", Token: secret},
		"telegram apiBase":  {Type: "telegram", Token: secret, ChatID: "42", APIBase: "http://tg.example.com"},
		"bad CA":            {Type: "webhook", URL: "https://hooks.example.com/x", CA: "-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----\n"},
		"smtp no host":      with(smtpOK, func(c *ChannelConfig) { c.Host = "" }),
		"smtp no from":      with(smtpOK, func(c *ChannelConfig) { c.From = "" }),
		"smtp no to":        with(smtpOK, func(c *ChannelConfig) { c.To = nil }),
		"smtp bad port":     with(smtpOK, func(c *ChannelConfig) { c.Port = 70000 }),
		"smtp CRLF in to":   with(smtpOK, func(c *ChannelConfig) { c.To = []string{"b@example.com\r\nBcc: x@evil.test"} }),
	}
	for name, cc := range invalid {
		t.Run(name, func(t *testing.T) {
			cfg := Config{Channels: []ChannelConfig{{Type: "ntfy", URL: "https://ntfy.sh/ok"}, cc}}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate = nil, want error")
			}
			if !strings.Contains(err.Error(), "channel 1") {
				t.Errorf("error %q does not name the channel index", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), pw) {
				t.Errorf("error %q leaks a secret", err)
			}
			if _, err := Build(cfg); err == nil {
				t.Error("Build accepted an invalid config")
			}
		})
	}
}

func TestWebhookPostsMessageJSON(t *testing.T) {
	srv, rec := recordingServer(t, http.StatusNoContent, "")
	ch := build1(t, ChannelConfig{Type: "webhook", URL: srv.URL + "/hook", CA: caPEM(srv)})

	if err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	path, h, body := rec.get()
	if path != "/hook" || h.Get("Content-Type") != "application/json" {
		t.Errorf("path %q content-type %q", path, h.Get("Content-Type"))
	}
	var got Message
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if got != msg {
		t.Errorf("webhook body = %+v, want %+v", got, msg)
	}
}

func TestWithoutCATLSFails(t *testing.T) {
	srv, _ := recordingServer(t, http.StatusOK, "")
	ch := build1(t, ChannelConfig{Type: "webhook", URL: srv.URL})
	if err := ch.Send(context.Background(), msg); err == nil {
		t.Fatal("Send to a server with an untrusted certificate succeeded")
	}
}

func TestNon2xxIsError(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusFound} {
		srv, _ := recordingServer(t, status, "")
		ch := build1(t, ChannelConfig{Type: "webhook", URL: srv.URL, CA: caPEM(srv)})
		if err := ch.Send(context.Background(), msg); err == nil {
			t.Errorf("status %d: Send = nil, want error", status)
		}
	}
}

func TestTelegram(t *testing.T) {
	srv, rec := recordingServer(t, http.StatusOK, `{"ok":true,"result":{}}`)
	ch := build1(t, ChannelConfig{Type: "telegram", Token: secret, ChatID: "-10042", APIBase: srv.URL + "/", CA: caPEM(srv)})

	if err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	path, _, body := rec.get()
	if want := "/bot" + secret + "/sendMessage"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	want := map[string]any{"chat_id": "-10042", "text": "Alert: disk / 91%\ntokyo-1 · disk / is 91% full"}
	if len(got) != len(want) || got["chat_id"] != want["chat_id"] || got["text"] != want["text"] {
		t.Errorf("body = %v, want %v", got, want)
	}
	if strings.Contains(ch.Name(), secret) {
		t.Errorf("Name() %q leaks the token", ch.Name())
	}
}

func TestTelegramNotOK(t *testing.T) {
	srv, _ := recordingServer(t, http.StatusOK, `{"ok":false,"description":"chat not found"}`)
	ch := build1(t, ChannelConfig{Type: "telegram", Token: secret, ChatID: "42", APIBase: srv.URL, CA: caPEM(srv)})
	err := ch.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("Send = nil on ok:false")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q leaks the token", err)
	}
}

func TestTelegramTransportErrorRedactsToken(t *testing.T) {
	// No CA: the TLS handshake fails, and net/http's error quotes the URL, which holds the token.
	srv, _ := recordingServer(t, http.StatusOK, `{"ok":true}`)
	ch := build1(t, ChannelConfig{Type: "telegram", Token: secret, ChatID: "42", APIBase: srv.URL})
	err := ch.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("Send succeeded without trusting the server")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error %q leaks the token", err)
	}
}

func TestNtfy(t *testing.T) {
	srv, rec := recordingServer(t, http.StatusOK, "{}")
	ch := build1(t, ChannelConfig{Type: "ntfy", URL: srv.URL + "/alerts", Token: "tk_abc", CA: caPEM(srv)})

	m := msg
	m.Title = "Alert:\r\ndisk"
	if err := ch.Send(context.Background(), m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	path, h, body := rec.get()
	if path != "/alerts" {
		t.Errorf("path = %q", path)
	}
	if got := h.Get("Title"); got != "Alert: disk" {
		t.Errorf("Title header = %q", got)
	}
	if got := h.Get("Authorization"); got != "Bearer tk_abc" {
		t.Errorf("Authorization = %q", got)
	}
	if got, want := string(body), "tokyo-1 · disk / is 91% full"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	// Without a token there is no Authorization header.
	ch = build1(t, ChannelConfig{Type: "ntfy", URL: srv.URL + "/alerts", CA: caPEM(srv)})
	if err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, h, _ := rec.get(); h.Get("Authorization") != "" {
		t.Errorf("unexpected Authorization %q", h.Get("Authorization"))
	}
}

func TestSendRespectsContext(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ch := build1(t, ChannelConfig{Type: "telegram", Token: secret, ChatID: "42", APIBase: srv.URL, CA: caPEM(srv)})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ch.Send(ctx, msg) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Send = %v, want context.Canceled", err)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("error %q leaks the token", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancel")
	}
}

type fakeChannel struct {
	name string
	err  error
	got  []Message
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(_ context.Context, m Message) error {
	f.got = append(f.got, m)
	return f.err
}

func TestSendAllContinuesPastFailures(t *testing.T) {
	boom := errors.New("boom")
	a := &fakeChannel{name: "webhook"}
	b := &fakeChannel{name: "telegram", err: boom}
	c := &fakeChannel{name: "ntfy"}

	err := SendAll(context.Background(), []Channel{a, b, c}, msg)
	if !errors.Is(err, boom) {
		t.Fatalf("SendAll = %v, want it to wrap the failure", err)
	}
	if !strings.Contains(err.Error(), "telegram") || strings.Contains(err.Error(), "webhook") || strings.Contains(err.Error(), "ntfy") {
		t.Errorf("SendAll error %q should name only the failing channel", err)
	}
	if len(a.got) != 1 || len(c.got) != 1 {
		t.Errorf("channels after the failure were not all sent to: a=%d c=%d", len(a.got), len(c.got))
	}
	if err := SendAll(context.Background(), []Channel{a, c}, msg); err != nil {
		t.Errorf("SendAll with no failures = %v", err)
	}
}

func TestSendAllTellsWhetherAnyChannelDelivered(t *testing.T) {
	boom := errors.New("boom")
	ok := &fakeChannel{name: "webhook"}
	bad := &fakeChannel{name: "telegram", err: boom}
	worse := &fakeChannel{name: "ntfy", err: boom}
	if err := SendAll(context.Background(), []Channel{ok, bad}, msg); !Delivered(err) {
		t.Errorf("one of two delivered: Delivered(%v) = false", err)
	}
	if err := SendAll(context.Background(), []Channel{bad, worse}, msg); Delivered(err) {
		t.Errorf("none delivered: Delivered(%v) = true", err)
	}
	if !Delivered(nil) {
		t.Error("Delivered(nil) = false")
	}
}
