package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// renewals sends the plan's reminders (docs/SPEC.md §4.3), each once, even
// across restarts: every sync of the config restarts the agent.
type renewals struct {
	o    Options
	sent renewal.Sent
}

func newRenewals(o Options) *renewals {
	r := &renewals{o: o, sent: renewal.Sent{}}
	if data, err := os.ReadFile(o.RenewalStatePath); err == nil {
		if err := json.Unmarshal(data, &r.sent); err != nil {
			o.Log.Printf("ignoring sent renewal reminders: %v", err)
			r.sent = renewal.Sent{}
		}
	}
	return r
}

// run checks now and then every RenewalEvery until ctx ends.
func (r *renewals) run(ctx context.Context) {
	t := time.NewTicker(r.o.RenewalEvery)
	defer t.Stop()
	for {
		r.check(ctx, r.o.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// check sends today's reminder if one is due and it is past the remind hour
// where the user lives. A reminder no channel delivered is tried again at
// the next check.
func (r *renewals) check(ctx context.Context, now time.Time) {
	local := now.In(r.o.Location)
	if local.Hour() < r.o.RemindHour {
		return
	}
	rem, ok := r.o.Renewal.Due(renewal.DateOf(local), r.sent)
	if !ok {
		return
	}
	m := notify.Message{Server: r.o.Name, Title: rem.Title(), Text: rem.Text(), Time: now}
	r.o.Log.Printf("%s (%s)", m.Title, m.Text)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := r.o.Notify(ctx, m); err != nil {
		r.o.Log.Printf("notify: %v", err)
		if !notify.Delivered(err) {
			return
		}
	}
	r.sent.Record(rem)
	if err := writeAtomic(r.o.RenewalStatePath, func(w io.Writer) error { return json.NewEncoder(w).Encode(r.sent) }); err != nil {
		r.o.Log.Printf("save sent renewal reminders: %v", err)
	}
}
