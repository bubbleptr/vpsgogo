package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Validate checks every channel: a known Type, the fields that Type needs,
// https-only URLs and a parseable CA. Errors name the channel by index and
// Type and never quote URLs, tokens or passwords.
func (c Config) Validate() error {
	var errs []error
	for i, cc := range c.Channels {
		if err := cc.validate(); err != nil {
			errs = append(errs, fmt.Errorf("channel %d (%s): %w", i, cc.Type, err))
		}
	}
	return errors.Join(errs...)
}

func (cc ChannelConfig) validate() error {
	switch cc.Type {
	case "webhook", "ntfy":
		if err := checkHTTPS("url", cc.URL); err != nil {
			return err
		}
	case "telegram":
		if cc.Token == "" {
			return errors.New("token is required")
		}
		if cc.ChatID == "" {
			return errors.New("chatId is required")
		}
		if cc.APIBase != "" {
			if err := checkHTTPS("apiBase", cc.APIBase); err != nil {
				return err
			}
		}
	case "smtp":
		if cc.Host == "" || strings.ContainsAny(cc.Host, "\r\n/ ") {
			return errors.New("host is required and must be a bare host name")
		}
		if cc.Port < 0 || cc.Port > 65535 {
			return errors.New("port must be 0 (587) or 1-65535")
		}
		if cc.From == "" || strings.ContainsAny(cc.From, "\r\n") {
			return errors.New("from is required and must be a single line")
		}
		if len(cc.To) == 0 {
			return errors.New("at least one to address is required")
		}
		for _, to := range cc.To {
			if to == "" || strings.ContainsAny(to, "\r\n") {
				return errors.New("to addresses must be non-empty single lines")
			}
		}
	default:
		return errors.New("unknown channel type")
	}
	if cc.CA != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(cc.CA)) {
		return errors.New("ca contains no PEM certificate")
	}
	return nil
}

// checkHTTPS reports whether raw is an absolute https URL with a host. It
// never quotes raw, which may carry a secret in its path or userinfo.
func checkHTTPS(field, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s must be an https URL", field)
	}
	return nil
}

// Build validates c and returns its channels in order.
func Build(c Config) ([]Channel, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	chans := make([]Channel, 0, len(c.Channels))
	for _, cc := range c.Channels {
		tc, err := tlsConfig(cc.CA)
		if err != nil {
			return nil, err
		}
		switch cc.Type {
		case "webhook":
			chans = append(chans, &webhookChannel{url: cc.URL, client: newHTTPClient(tc)})
		case "telegram":
			base := cc.APIBase
			if base == "" {
				base = "https://api.telegram.org"
			}
			chans = append(chans, &telegramChannel{
				url:    strings.TrimRight(base, "/") + "/bot" + cc.Token + "/sendMessage",
				chatID: cc.ChatID,
				client: newHTTPClient(tc),
			})
		case "ntfy":
			chans = append(chans, &ntfyChannel{url: cc.URL, token: cc.Token, client: newHTTPClient(tc)})
		case "smtp":
			chans = append(chans, newSMTPChannel(cc, tc))
		}
	}
	return chans, nil
}

// tlsConfig trusts the system roots plus the PEM bundle ca, if any.
func tlsConfig(ca string) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if ca != "" && !roots.AppendCertsFromPEM([]byte(ca)) {
		return nil, errors.New("notify: ca contains no PEM certificate")
	}
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, nil
}

// SendAll sends m to every channel, continuing past failures. It returns the
// failures joined; Delivered tells whether any channel got m all the same.
func SendAll(ctx context.Context, chans []Channel, m Message) error {
	var errs []error
	for _, ch := range chans {
		if err := ch.Send(ctx, m); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.Name(), err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	err := errors.Join(errs...)
	if len(errs) < len(chans) {
		return partialError{err}
	}
	return err
}

// partialError is SendAll's error when some channels delivered.
type partialError struct{ error }

func (e partialError) Unwrap() error { return e.error }

// Delivered reports whether a send that returned err reached at least one
// channel: a notice that did should not be sent again because another
// channel is down.
func Delivered(err error) bool {
	return err == nil || errors.As(err, new(partialError))
}

// oneLine replaces CR and LF with spaces so a value is safe in a header.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
}
