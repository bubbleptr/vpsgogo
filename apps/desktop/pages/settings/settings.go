// Package settings is the settings page: the user's timezone and reminder
// hour, notification channels with a test message each, and the Anthropic
// API key (docs/SPEC.md §4.3, §4.4). Secrets go in password inputs and are
// never shown as text.
package settings

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/egoist/mygo/ui"
)

// Types are the channel types, as notify names them.
var Types = []string{"webhook", "telegram", "ntfy", "smtp"}

var titles = map[string]string{"webhook": "Webhook", "telegram": "Telegram", "ntfy": "ntfy", "smtp": "Email (SMTP)"}

// Options are what the page needs.
type Options struct {
	Backend services.Backend
	Runner  async.Runner
	Store   *store.Store
	Info    services.AppInfo
}

// channel is one channel being edited.
type channel struct {
	cfg    notify.ChannelConfig
	port   float64 // smtp
	to     string  // smtp: comma-separated
	result string
	failed bool
}

func (ch *channel) config() notify.ChannelConfig {
	cfg := ch.cfg
	if cfg.Type == "smtp" {
		cfg.Port = int(ch.port)
		cfg.To = nil
		for _, a := range strings.Split(ch.to, ",") {
			if a = strings.TrimSpace(a); a != "" {
				cfg.To = append(cfg.To, a)
			}
		}
	}
	return cfg
}

// Page is the settings page's state: a draft of the settings until Save.
type Page struct {
	o        Options
	timezone string
	hour     float64
	channels []*channel
	key      string // the saved key, or the one set since
	newKey   string
	newType  string
	msg      string
	err      string
	busy     bool // a save is in flight
	// gen counts drafts: a save's result shows only on the draft it saved.
	gen int
}

// New returns the page; Open fills it from the store.
func New(o Options) *Page { return &Page{o: o, newType: Types[0]} }

// Open starts a draft of the stored settings.
// While a save is in flight it keeps the draft being saved, which the
// store does not hold yet.
func (p *Page) Open() {
	if p.busy {
		return
	}
	s := p.o.Store.Settings
	*p = Page{o: p.o, gen: p.gen + 1, newType: Types[0], timezone: s.Timezone, hour: float64(s.RemindHour), key: s.AnthropicKey}
	for _, cfg := range s.Channels {
		ch := &channel{cfg: cfg, port: float64(cfg.Port), to: strings.Join(cfg.To, ", ")}
		if cfg.Type == "smtp" && cfg.Port == 0 {
			ch.port = 587
		}
		p.channels = append(p.channels, ch)
	}
}

// View builds the page.
func (p *Page) View(c *ui.Context) {
	pal := theme.For(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Padding(32).Gap(16).Children(func() {
			ui.Text(c, "Preferences").FontSize(22).FontWeight(800).TextColor(pal.Ink)
			ui.Form(c, func() {
				ui.Field(c, "Timezone", func() { ui.TextInput(c, &p.timezone).Placeholder("System") }).
					Description("Such as Asia/Shanghai. Renewal dates and reminders follow it.")
				ui.Field(c, "Remind at hour", func() { ui.NumberInput(c, &p.hour, 0, 23, 1) })
			})
			p.channelsView(c, pal)
			p.keyView(c, pal)
			ui.Row(c).Gap(12).Children(func() {
				if ui.PrimaryButton(c, "Save").Disabled(p.busy).Clicked() {
					p.save()
				}
				if p.msg != "" {
					ui.Text(c, p.msg).TextColor(theme.Green)
				}
				if p.err != "" {
					ui.Text(c, p.err).TextColor(theme.Coral)
				}
			})
			ui.Divider(c)
			ui.Text(c, fmt.Sprintf("VPSGoGo v%s · %s/%s", p.o.Info.Version, p.o.Info.OS, p.o.Info.Arch)).TextColor(pal.Ink).Opacity(.6)
		})
	})
}

func (p *Page) channelsView(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "Notification channels").FontSize(18).FontWeight(700).TextColor(pal.Ink)
	ui.Text(c, "Make a bot or webhook just for VPSGoGo: a copy of its token is kept on each server.").TextColor(pal.Ink).Opacity(.7)
	if len(p.channels) == 0 {
		ui.Text(c, "No channels yet").TextColor(pal.Ink).Opacity(.7)
	}
	remove := -1
	for i, ch := range p.channels {
		// Labels stay unique when there are several channels.
		n := ""
		if i > 0 {
			n = fmt.Sprintf(" #%d", i+1)
		}
		ui.Column(c.Key(ch)).Gap(8).Padding(16).Radius(12).Background(pal.Paper).Children(func() {
			ui.Text(c, titles[ch.cfg.Type]+n).FontWeight(700).TextColor(pal.Ink)
			ui.Form(c, func() { fields(c, ch, n) })
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Send test message"+n).Clicked() {
					p.test(ch)
				}
				if ui.Button(c, "Remove channel"+n).Clicked() {
					remove = i
				}
				if ch.result != "" {
					color := theme.Green
					if ch.failed {
						color = theme.Coral
					}
					ui.Text(c, ch.result).TextColor(color)
				}
			})
		})
	}
	if remove >= 0 {
		p.channels = slices.Delete(p.channels, remove, remove+1)
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.Select(c, &p.newType, Types).Label("Channel type")
		if ui.Button(c, "Add channel").Clicked() {
			ch := &channel{cfg: notify.ChannelConfig{Type: p.newType}}
			if p.newType == "smtp" {
				ch.port = 587
			}
			p.channels = append(p.channels, ch)
		}
	})
}

// fields builds a channel's inputs; n tells apart those of several.
func fields(c *ui.Context, ch *channel, n string) {
	secret := func(label string, v *string) {
		ui.Field(c, label+n, func() { ui.TextInput(c, v).Password() })
	}
	plain := func(label string, v *string) {
		ui.Field(c, label+n, func() { ui.TextInput(c, v) })
	}
	switch ch.cfg.Type {
	case "webhook":
		secret("Webhook URL", &ch.cfg.URL) // its path is often the secret
	case "telegram":
		secret("Bot token", &ch.cfg.Token)
		plain("Chat ID", &ch.cfg.ChatID)
	case "ntfy":
		secret("ntfy URL", &ch.cfg.URL)
		secret("ntfy token", &ch.cfg.Token)
	case "smtp":
		plain("SMTP host", &ch.cfg.Host)
		ui.Field(c, "SMTP port"+n, func() { ui.NumberInput(c, &ch.port, 1, 65535, 1) })
		plain("SMTP username", &ch.cfg.Username)
		secret("SMTP password", &ch.cfg.Password)
		plain("From address", &ch.cfg.From)
		plain("To addresses", &ch.to)
	}
	ui.Field(c, "Private CA (PEM)"+n, func() { ui.TextArea(c, &ch.cfg.CA) }).
		Description("Only for a self-hosted endpoint whose certificate your own CA signed.")
}

func (p *Page) test(ch *channel) {
	cfg := ch.config()
	ch.result, ch.failed = "Sending…", false
	if err := (notify.Config{Channels: []notify.ChannelConfig{cfg}}).Validate(); err != nil {
		ch.result, ch.failed = err.Error(), true
		return
	}
	p.o.Runner.Go(func(post func(func())) {
		err := p.o.Backend.TestChannel(context.Background(), cfg)
		post(func() {
			if err != nil {
				ch.result, ch.failed = err.Error(), true
				return
			}
			ch.result = "Test message sent"
		})
	})
}

func (p *Page) keyView(c *ui.Context, pal theme.Palette) {
	ui.Text(c, "Claude").FontSize(18).FontWeight(700).TextColor(pal.Ink)
	state := "not set"
	if p.key != "" {
		state = "set"
	}
	ui.Text(c, "Anthropic API key: "+state).TextColor(pal.Ink)
	ui.Row(c).Gap(8).Children(func() {
		ui.Field(c, "New API key", func() { ui.TextInput(c, &p.newKey).Password() })
		if ui.Button(c, "Set key").Disabled(strings.TrimSpace(p.newKey) == "").Clicked() {
			p.key, p.newKey = strings.TrimSpace(p.newKey), ""
		}
		if ui.Button(c, "Clear key").Disabled(p.key == "").Clicked() {
			p.key = ""
		}
	})
}

func (p *Page) save() {
	p.msg, p.err = "", ""
	tz := strings.TrimSpace(p.timezone)
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			p.err = fmt.Sprintf("Unknown timezone %q", tz)
			return
		}
	}
	s := services.Settings{Timezone: tz, RemindHour: int(p.hour), AnthropicKey: p.key}
	for _, ch := range p.channels {
		s.Channels = append(s.Channels, ch.config())
	}
	if err := (notify.Config{Channels: s.Channels}).Validate(); err != nil {
		p.err = err.Error()
		return
	}
	p.busy = true
	gen := p.gen
	p.o.Runner.Go(func(post func(func())) {
		err := p.o.Backend.SaveSettings(context.Background(), s)
		post(func() {
			if err == nil {
				p.o.Store.Settings = s
			}
			if gen != p.gen {
				return
			}
			p.busy = false
			if err != nil {
				p.err = err.Error()
				return
			}
			p.msg = "Settings saved"
		})
	})
}
