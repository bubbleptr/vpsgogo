package settings

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bubbleptr/vpsgogo/apps/desktop/entities/store"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/apps/desktop/services/fake"
	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/async"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/egoist/mygo/ui"
)

const (
	hookURL = "https://hooks.example.com/s3cr3t-path"
	apiKey  = "sk-ant-test-key-123"
	botTok  = "123456:bot-token-secret"
)

type rig struct {
	t  *testing.T
	b  *fake.Backend
	r  *async.Manual
	s  *store.Store
	p  *Page
	tt *ui.Tester
}

func newRig(t *testing.T, saved services.Settings) *rig {
	g := &rig{t: t, b: fake.New(), r: &async.Manual{}, s: &store.Store{Loaded: true, Settings: saved}}
	g.p = New(Options{Backend: g.b, Runner: g.r, Store: g.s,
		Info: services.AppInfo{Version: "0.1.0-test", OS: "darwin", Arch: "arm64"}})
	g.p.Open()
	g.tt = ui.NewTester(g.p.View, 1200, 1400)
	return g
}

func (g *rig) click(s string) {
	g.t.Helper()
	if err := g.tt.Click(s); err != nil {
		g.t.Fatalf("click %q: %v; texts %q", s, err, g.tt.Texts())
	}
	g.noSecrets()
}

func (g *rig) typeInto(label, text string) {
	g.t.Helper()
	g.click(label)
	g.tt.Type(text)
	g.noSecrets()
}

func (g *rig) flush() {
	g.r.Flush()
	g.tt.Frame()
	g.noSecrets()
}

func (g *rig) want(texts ...string) {
	g.t.Helper()
	for _, s := range texts {
		if !g.tt.HasText(s) {
			g.t.Fatalf("no %q among %q", s, g.tt.Texts())
		}
	}
}

// noSecrets fails if a secret shows as a text or as a control's value.
func (g *rig) noSecrets() {
	g.t.Helper()
	shown := g.tt.Texts()
	for _, a := range g.tt.AccessAll() {
		shown = append(shown, a.Value)
	}
	for _, s := range shown {
		for _, secret := range []string{hookURL, apiKey, botTok, "s3cr3t"} {
			if strings.Contains(s, secret) {
				g.t.Fatalf("a secret shows in %q", s)
			}
		}
	}
}

func TestShowsAppInfoAndDefaults(t *testing.T) {
	g := newRig(t, services.Settings{RemindHour: 9})
	g.want("VPSGoGo v0.1.0-test · darwin/arm64", "Anthropic API key: not set", "No channels yet")
}

func TestWebhookTestAndSave(t *testing.T) {
	g := newRig(t, services.Settings{RemindHour: 9})
	g.click("Add channel")
	g.want("Webhook")
	g.typeInto("Webhook URL", hookURL)
	g.click("Send test message")
	g.flush()
	g.want("Test message sent")
	if g.b.Count("TestChannel") != 1 {
		t.Fatal("no test was sent")
	}
	g.b.TestChannelErr = errors.New("notify: webhook answered 500")
	g.click("Send test message")
	g.flush()
	g.want("notify: webhook answered 500")

	g.typeInto("Timezone", "Asia/Shanghai")
	g.click("Save")
	g.flush()
	g.want("Settings saved")
	got := g.b.SavedSettings()
	if got.Timezone != "Asia/Shanghai" || got.RemindHour != 9 || len(got.Channels) != 1 ||
		!reflect.DeepEqual(got.Channels[0], notify.ChannelConfig{Type: "webhook", URL: hookURL}) {
		t.Fatalf("saved %+v", got)
	}
	if g.s.Settings.Timezone != "Asia/Shanghai" {
		t.Fatal("the store keeps the old settings")
	}
}

func TestInvalidChannelIsNotTested(t *testing.T) {
	g := newRig(t, services.Settings{})
	g.click("Add channel")
	g.typeInto("Webhook URL", "http://plain.example.com")
	g.click("Send test message")
	g.flush()
	g.want("url must be an https URL")
	if g.b.Count("TestChannel") != 0 {
		t.Fatal("tested a channel that is not valid")
	}
	g.click("Save")
	g.flush()
	if g.b.Count("SaveSettings") != 0 {
		t.Fatal("saved a channel that is not valid")
	}
}

func TestTelegramSecretsStayHidden(t *testing.T) {
	g := newRig(t, services.Settings{})
	g.click("webhook")
	g.click("telegram")
	g.click("Add channel")
	g.typeInto("Bot token", botTok)
	g.typeInto("Chat ID", "42")
	g.click("Save")
	g.flush()
	if ch := g.b.SavedSettings().Channels; len(ch) != 1 || ch[0].Token != botTok || ch[0].ChatID != "42" {
		t.Fatalf("saved %+v", ch)
	}
	g.click("Remove channel")
	g.want("No channels yet")
}

func TestSMTPRecipients(t *testing.T) {
	g := newRig(t, services.Settings{})
	g.click("webhook")
	g.click("smtp")
	g.click("Add channel")
	g.typeInto("SMTP host", "smtp.example.com")
	g.typeInto("From address", "gogo@example.com")
	g.typeInto("To addresses", "a@example.com, b@example.com")
	g.click("Save")
	g.flush()
	ch := g.b.SavedSettings().Channels
	if len(ch) != 1 || len(ch[0].To) != 2 || ch[0].To[1] != "b@example.com" || ch[0].Port != 587 {
		t.Fatalf("saved %+v", ch)
	}
}

func TestAnthropicKeySetAndClear(t *testing.T) {
	g := newRig(t, services.Settings{})
	g.typeInto("New API key", apiKey)
	g.click("Set key")
	g.want("Anthropic API key: set")
	g.click("Save")
	g.flush()
	if g.b.SavedSettings().AnthropicKey != apiKey {
		t.Fatal("the key was not saved")
	}
	g.click("Clear key")
	g.want("Anthropic API key: not set")
}

func TestUnknownTimezone(t *testing.T) {
	g := newRig(t, services.Settings{})
	g.typeInto("Timezone", "Mars/Olympus")
	g.click("Save")
	g.want(`Unknown timezone "Mars/Olympus"`)
	if g.r.Pending() != 0 {
		t.Fatal("saved an unknown timezone")
	}
}

func TestPrivateCAIsChecked(t *testing.T) {
	g := newRig(t, services.Settings{})
	g.click("Add channel")
	g.typeInto("Webhook URL", hookURL)
	g.typeInto("Private CA (PEM)", "not a certificate")
	g.click("Save")
	g.want("ca contains no PEM certificate")
	if g.r.Pending() != 0 {
		t.Fatal("saved a CA that is not PEM")
	}
}

func TestReopeningMidSaveKeepsTheDraft(t *testing.T) {
	g := newRig(t, services.Settings{RemindHour: 9})
	g.typeInto("Timezone", "Asia/Tokyo")
	g.click("Save")
	g.p.Open() // the user left and came back before the save finished
	g.tt.Frame()
	if a, _ := g.tt.AccessByRole(ui.RoleTextField, "Timezone"); a.Value != "Asia/Tokyo" {
		t.Fatalf("the draft was rebuilt mid-save: timezone %q", a.Value)
	}
	if a, _ := g.tt.AccessByRole(ui.RoleButton, "Save"); !a.Disabled {
		t.Fatal("Save is enabled while a save is in flight")
	}
	g.flush()
	g.want("Settings saved")
	if g.s.Settings.Timezone != "Asia/Tokyo" {
		t.Fatalf("the store has %q", g.s.Settings.Timezone)
	}
	g.p.Open()
	g.tt.Frame()
	if a, _ := g.tt.AccessByRole(ui.RoleTextField, "Timezone"); a.Value != "Asia/Tokyo" {
		t.Fatalf("after the save, the draft has %q", a.Value)
	}
}
