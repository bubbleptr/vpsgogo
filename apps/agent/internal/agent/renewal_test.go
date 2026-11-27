package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

func plan(expires string) *renewal.Subscription {
	d, err := renewal.ParseDate(expires)
	if err != nil {
		panic(err)
	}
	return &renewal.Subscription{ServerID: "s1", ExpiresAt: d, Cycle: renewal.Monthly, Price: "5.00", Currency: "USD"}
}

// renewalChecker returns a checker whose notices land in sent; fail makes
// sending fail.
func renewalChecker(t *testing.T, sub *renewal.Subscription, loc *time.Location) (*renewals, *[]notify.Message, *error) {
	var mu sync.Mutex
	var sent []notify.Message
	var fail error
	o := Options{
		Name:             "test-vps",
		Renewal:          sub,
		Location:         loc,
		RemindHour:       9,
		RenewalStatePath: filepath.Join(t.TempDir(), "renewal.json"),
		Notify: func(_ context.Context, m notify.Message) error {
			mu.Lock()
			defer mu.Unlock()
			if fail != nil {
				return fail
			}
			sent = append(sent, m)
			return nil
		},
	}
	o.defaults()
	return newRenewals(o), &sent, &fail
}

func TestRenewalRemindsAfterTheRemindHourInTheUsersZone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	r, sent, _ := renewalChecker(t, plan("2026-12-02"), la)
	ctx := context.Background()
	// 16:30 UTC on the 1st is 08:30 in Los Angeles: too early there.
	r.check(ctx, time.Date(2026, 12, 1, 16, 30, 0, 0, time.UTC))
	if len(*sent) != 0 {
		t.Fatalf("reminded before 9:00: %+v", *sent)
	}
	// 05:00 UTC on the 2nd is 21:00 on the 1st there: one day left, though
	// the UTC date is already the day it expires.
	r.check(ctx, time.Date(2026, 12, 2, 5, 0, 0, 0, time.UTC))
	if len(*sent) != 1 || (*sent)[0].Title != "Renewal: expires tomorrow" || (*sent)[0].Server != "test-vps" {
		t.Fatalf("at 21:00 on the 1st: %+v", *sent)
	}
	// 17:30 UTC on the 2nd is 09:30 on the 2nd there.
	r.check(ctx, time.Date(2026, 12, 2, 17, 30, 0, 0, time.UTC))
	if len(*sent) != 2 || (*sent)[1].Title != "Renewal: expires today" {
		t.Fatalf("at 09:30 on the 2nd: %+v", *sent)
	}
}

func TestRenewalRetriesWhenSendingFails(t *testing.T) {
	r, sent, fail := renewalChecker(t, plan("2026-12-02"), time.UTC)
	ctx := context.Background()
	*fail = errors.New("network down")
	r.check(ctx, time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC))
	*fail = nil
	r.check(ctx, time.Date(2026, 12, 1, 11, 0, 0, 0, time.UTC))
	r.check(ctx, time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC))
	if len(*sent) != 1 {
		t.Fatalf("want one notice after the retry, got %+v", *sent)
	}
}

func TestRenewalReminderOnceAcrossRestarts(t *testing.T) {
	h := newHarness(t)
	h.opts.Renewal = plan("2026-12-02")
	h.opts.Location = time.UTC
	h.opts.RemindHour = 9
	h.opts.Now = func() time.Time { return time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC) }
	h.opts.RenewalEvery = 5 * time.Millisecond
	h.opts.RenewalStatePath = filepath.Join(h.dir, "renewal.json")
	renewals := func() (n int) {
		for _, m := range h.messages() {
			if strings.HasPrefix(m.Title, "Renewal:") {
				n++
			}
		}
		return n
	}
	h.start()
	eventually(t, "a renewal reminder", func() bool { return renewals() > 0 })
	time.Sleep(50 * time.Millisecond) // many more checks
	h.stop()
	if _, err := os.Stat(h.opts.RenewalStatePath); err != nil {
		t.Fatalf("what was sent is not saved: %v", err)
	}
	h.start()
	time.Sleep(50 * time.Millisecond)
	if n := renewals(); n != 1 {
		t.Errorf("%d renewal reminders, want 1: %+v", n, h.messages())
	}
}

func TestConfigRenewal(t *testing.T) {
	dir := t.TempDir()
	write := func(cfg string) string {
		p := filepath.Join(dir, "agent.json")
		os.WriteFile(p, []byte(cfg), 0o600)
		return p
	}
	c, err := LoadConfig(write(`{"name":"vps","timezone":"Asia/Tokyo","renewal":{"serverId":"s1","expiresAt":"2026-12-01","cycle":"monthly"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Renewal == nil || c.Renewal.ExpiresAt.String() != "2026-12-01" || c.RemindHourOr9() != 9 {
		t.Errorf("%+v", c)
	}
	if loc, _ := c.Location(); loc.String() != "Asia/Tokyo" {
		t.Errorf("location %v", loc)
	}
	if c, _ := LoadConfig(write(`{"remindHour":0}`)); c.RemindHourOr9() != 0 {
		t.Errorf("remindHour 0 read as %d", c.RemindHourOr9())
	}
	for name, cfg := range map[string]string{
		"zone":       `{"timezone":"Mars/Olympus"}`,
		"hour":       `{"remindHour":24}`,
		"plan":       `{"renewal":{"serverId":"s1","cycle":"monthly"}}`,
		"plain http": `{"renewal":{"serverId":"s1","expiresAt":"2026-12-01","cycle":"monthly","renewUrl":"http://x.example"}}`,
	} {
		if _, err := LoadConfig(write(cfg)); err == nil {
			t.Errorf("%s: accepted %s", name, cfg)
		}
	}
}
