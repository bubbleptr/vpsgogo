package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const sendTimeout = 15 * time.Second

// newHTTPClient returns a client that trusts tc's roots and never follows
// redirects: a redirect would turn the POST into a GET or leave https.
func newHTTPClient(tc *tls.Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tc
	return &http.Client{
		Transport:     tr,
		Timeout:       sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// post sends body to target and returns the response body of a 2xx reply.
// Transport errors drop the URL, which for telegram holds the bot token.
func post(ctx context.Context, client *http.Client, target, contentType string, body []byte, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build request")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("post: %w", ue.Err)
		}
		return nil, errors.New("post failed")
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return respBody, nil
}

type webhookChannel struct {
	url    string
	client *http.Client
}

func (*webhookChannel) Name() string { return "webhook" }

func (w *webhookChannel) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = post(ctx, w.client, w.url, "application/json", body, nil)
	return err
}

type telegramChannel struct {
	url    string // contains the bot token; never put it in an error
	chatID string
	client *http.Client
}

func (*telegramChannel) Name() string { return "telegram" }

func (t *telegramChannel) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(map[string]string{
		"chat_id": t.chatID,
		"text":    m.Title + "\n" + m.Server + " · " + m.Text,
	})
	if err != nil {
		return err
	}
	resp, err := post(ctx, t.client, t.url, "application/json", body, nil)
	if err != nil {
		return err
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return errors.New("telegram: response is not JSON")
	}
	if !r.OK {
		return fmt.Errorf("telegram: not ok: %s", r.Description)
	}
	return nil
}

type ntfyChannel struct {
	url    string
	token  string
	client *http.Client
}

func (*ntfyChannel) Name() string { return "ntfy" }

func (n *ntfyChannel) Send(ctx context.Context, m Message) error {
	h := http.Header{}
	h.Set("Title", oneLine(m.Title))
	if n.token != "" {
		h.Set("Authorization", "Bearer "+n.token)
	}
	_, err := post(ctx, n.client, n.url, "text/plain; charset=utf-8", []byte(m.Server+" · "+m.Text), h)
	return err
}
