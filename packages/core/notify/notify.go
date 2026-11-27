// Package notify sends messages to the channels a user configured: Telegram,
// SMTP mail, webhooks and ntfy. gogo-agent sends alerts with it while the
// desktop app is closed. Every channel speaks TLS only. See docs/SPEC.md §4.4.
package notify

import (
	"context"
	"time"
)

// Message is one notice.
type Message struct {
	Server string    `json:"server"` // the server's display name
	Title  string    `json:"title"`  // short, e.g. "Alert: disk / 91%" or "Recovered: disk /"
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

// Channel delivers messages to one destination.
type Channel interface {
	Name() string // e.g. "webhook", "telegram"; for logs, never containing secrets
	Send(ctx context.Context, m Message) error
}

// Config is /etc/gogo-agent/notify.json. It holds tokens and passwords.
type Config struct {
	Channels []ChannelConfig `json:"channels"`
}

// ChannelConfig configures one channel; Type selects which fields apply.
// CA, when set, is a PEM bundle trusted in addition to the system roots, for
// self-hosted endpoints with a private CA.
type ChannelConfig struct {
	Type string `json:"type"` // "webhook", "telegram", "ntfy" or "smtp"

	URL string `json:"url,omitempty"` // webhook: POST target; ntfy: server URL with topic
	CA  string `json:"ca,omitempty"`

	Token  string `json:"token,omitempty"`  // telegram: bot token; ntfy: access token (optional)
	ChatID string `json:"chatId,omitempty"` // telegram
	// APIBase overrides https://api.telegram.org; tests set it.
	APIBase string `json:"apiBase,omitempty"`

	Host     string   `json:"host,omitempty"` // smtp
	Port     int      `json:"port,omitempty"` // smtp: 465 implicit TLS, otherwise STARTTLS required
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`
}
