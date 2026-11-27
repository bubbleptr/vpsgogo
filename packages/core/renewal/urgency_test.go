package renewal

import "testing"

func TestUrgencyBoundaries(t *testing.T) {
	today := d("2026-12-01")
	for _, c := range []struct {
		expires string
		want    Urgency
	}{
		{"2026-12-09", Calm},    // 8 days left
		{"2026-12-08", Soon},    // 7 days left: the first reminder
		{"2026-12-01", Soon},    // expires today
		{"2026-11-30", Overdue}, // expired yesterday
	} {
		if got := monthly(c.expires).Urgency(today); got != c.want {
			t.Errorf("expires %s: %v, want %v", c.expires, got, c.want)
		}
	}
}
