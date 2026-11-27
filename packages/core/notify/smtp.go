package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

type smtpChannel struct {
	cfg ChannelConfig
	tls *tls.Config
	// dial opens the TCP connection; tests redirect it.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func newSMTPChannel(cc ChannelConfig, tc *tls.Config) *smtpChannel {
	tc = tc.Clone()
	tc.ServerName = cc.Host
	return &smtpChannel{cfg: cc, tls: tc, dial: (&net.Dialer{}).DialContext}
}

func (*smtpChannel) Name() string { return "smtp" }

// Send delivers m over TLS only: implicit TLS on port 465, otherwise
// mandatory STARTTLS. Nothing, credentials included, crosses in clear text.
func (s *smtpChannel) Send(ctx context.Context, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err := s.send(ctx, m)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		// The connection deadline is ctx's deadline; the net timeout can fire
		// before ctx's timer does. Wait for it so the error reports ctx.
		<-ctx.Done()
	}
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("smtp: %w", ctx.Err())
	}
	return err
}

func (s *smtpChannel) send(ctx context.Context, m Message) error {
	port := s.cfg.Port
	if port == 0 {
		port = 587
	}
	raw, err := s.dial(ctx, "tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("smtp: dial: %w", err)
	}
	defer raw.Close()
	// net/smtp has no context: bound the whole exchange by the deadline and
	// close the connection if ctx is canceled first.
	deadline, _ := ctx.Deadline()
	raw.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()

	conn := raw
	implicit := port == 465
	if implicit {
		tc := tls.Client(raw, s.tls)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("smtp: tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: greeting: %w", err)
	}
	defer c.Close()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not offer STARTTLS; refusing to send in clear text")
		}
		if err := c.StartTLS(s.tls); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp: mail from: %w", err)
	}
	for _, to := range s.cfg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp: rcpt: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	if _, err := w.Write(s.message(m)); err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	c.Quit() // the message is accepted; a failed QUIT changes nothing
	return nil
}

func (s *smtpChannel) message(m Message) []byte {
	date := m.Time
	if date.IsZero() {
		date = time.Now()
	}
	subject := "[VPSGoGo] " + oneLine(m.Server) + ": " + oneLine(m.Title)
	var b strings.Builder
	for _, h := range [][2]string{
		{"From", s.cfg.From},
		{"To", strings.Join(s.cfg.To, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", date.Format(time.RFC1123Z)},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "8bit"},
	} {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(m.Text, "\r\n", "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}
