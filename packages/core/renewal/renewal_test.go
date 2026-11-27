package renewal

import (
	"encoding/json"
	"strings"
	"testing"
)

func d(s string) Date {
	v, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func monthly(expires string) Subscription {
	return Subscription{ServerID: "s1", ExpiresAt: d(expires), Cycle: Monthly}
}

func TestDateJSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(struct{ D Date }{d("2028-02-29")})
	if err != nil || string(b) != `{"D":"2028-02-29"}` {
		t.Fatalf("%s %v", b, err)
	}
	var v struct{ D Date }
	if err := json.Unmarshal(b, &v); err != nil || v.D != d("2028-02-29") {
		t.Fatalf("%+v %v", v, err)
	}
	for _, bad := range []string{"2027-02-29", "2026-13-01", "26-1-1", ""} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) accepted", bad)
		}
	}
}

func TestDaysLeftCountsCalendarDays(t *testing.T) {
	s := monthly("2026-03-01")
	for today, want := range map[string]int{
		"2026-03-01": 0,  // the day it expires
		"2026-02-28": 1,  // across a month end
		"2026-02-22": 7,  // a week before
		"2026-03-02": -1, // a day late
		"2025-03-01": 365,
	} {
		if got := s.DaysLeft(d(today)); got != want {
			t.Errorf("DaysLeft on %s = %d, want %d", today, got, want)
		}
	}
}

func TestDueOnTheDay(t *testing.T) {
	s := monthly("2026-12-01")
	r, ok := s.Due(d("2026-12-01"), Sent{})
	if !ok || r.Threshold != 0 || r.DaysLeft != 0 {
		t.Fatalf("on the day: %+v %v", r, ok)
	}
}

func TestDueOnEachDefaultThresholdOnce(t *testing.T) {
	s := monthly("2026-12-01")
	sent := Sent{}
	var got []int
	for day := d("2026-11-20"); !day.After(d("2026-12-03")); day = day.AddDays(1) {
		if r, ok := s.Due(day, sent); ok {
			got = append(got, r.DaysLeft)
			sent.Record(r)
		}
	}
	want := []int{7, 3, 1, 0, -1}
	if len(got) != len(want) {
		t.Fatalf("reminded with days left %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reminded with days left %v, want %v", got, want)
		}
	}
}

func TestDueWhenExpired(t *testing.T) {
	s := monthly("2026-12-01")
	sent := Sent{}
	r, ok := s.Due(d("2026-12-10"), sent)
	if !ok || r.Threshold != Expired || r.DaysLeft != -9 {
		t.Fatalf("expired: %+v %v", r, ok)
	}
	sent.Record(r)
	if r, ok := s.Due(d("2026-12-11"), sent); ok {
		t.Errorf("expired twice: %+v", r)
	}
}

func TestDueAddedLateSendsOneNotice(t *testing.T) {
	// Added two days before expiry: 7 and 3 are both reached, one notice.
	s := monthly("2026-12-01")
	sent := Sent{}
	r, ok := s.Due(d("2026-11-29"), sent)
	if !ok || r.Threshold != 3 || r.DaysLeft != 2 {
		t.Fatalf("%+v %v", r, ok)
	}
	sent.Record(r)
	if r, ok := s.Due(d("2026-11-29"), sent); ok {
		t.Errorf("second notice the same day: %+v", r)
	}
	if r, ok := s.Due(d("2026-11-30"), sent); !ok || r.Threshold != 1 {
		t.Errorf("a day before: %+v %v", r, ok)
	}
}

func TestDueBeforeAnyThreshold(t *testing.T) {
	if r, ok := monthly("2026-12-01").Due(d("2026-11-01"), Sent{}); ok {
		t.Errorf("a month early: %+v", r)
	}
}

func TestDueCustomDays(t *testing.T) {
	s := monthly("2026-12-01")
	s.RemindDays = []int{14}
	sent := Sent{}
	r, ok := s.Due(d("2026-11-17"), sent)
	if !ok {
		t.Fatal("no notice 14 days before")
	}
	sent.Record(r)
	if r, ok := s.Due(d("2026-11-30"), sent); ok {
		t.Errorf("notice on a day not asked for: %+v", r)
	}
}

func TestRenewingStartsTheRemindersOver(t *testing.T) {
	s := monthly("2026-12-01")
	sent := Sent{}
	r, _ := s.Due(d("2026-12-01"), sent)
	sent.Record(r)
	s, err := s.Renewed()
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s.Due(d("2026-12-25"), sent); !ok || r.Threshold != 7 {
		t.Errorf("after renewing: %+v %v", r, ok)
	}
}

func TestRenewedByCycle(t *testing.T) {
	for _, c := range []struct {
		cycle   Cycle
		from    string
		want    string
		comment string
	}{
		{Monthly, "2026-05-15", "2026-06-15", "plain month"},
		{Monthly, "2026-12-15", "2027-01-15", "across a year end"},
		{Quarterly, "2026-11-30", "2027-02-28", "quarter ends short"},
		{Yearly, "2026-07-01", "2027-07-01", "plain year"},
	} {
		s := Subscription{ServerID: "s1", ExpiresAt: d(c.from), Cycle: c.cycle}
		got, err := s.Renewed()
		if err != nil || got.ExpiresAt != d(c.want) {
			t.Errorf("%s: %s %s → %v %v, want %s", c.comment, c.cycle, c.from, got.ExpiresAt, err, c.want)
		}
	}
}

func TestRenewedMonthEndKeepsTheBillingDay(t *testing.T) {
	// Billed on the 31st: short months clamp, longer ones return to the 31st.
	s := monthly("2027-01-31")
	var got []string
	for i := 0; i < 3; i++ {
		var err error
		if s, err = s.Renewed(); err != nil {
			t.Fatal(err)
		}
		got = append(got, s.ExpiresAt.String())
	}
	want := []string{"2027-02-28", "2027-03-31", "2027-04-30"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("renewals %v, want %v", got, want)
		}
	}
}

func TestRenewedLeapYear(t *testing.T) {
	s, _ := monthly("2028-01-31").Renewed()
	if s.ExpiresAt != d("2028-02-29") {
		t.Errorf("Jan 31 2028 + 1 month = %v, want 2028-02-29", s.ExpiresAt)
	}
	y := Subscription{ServerID: "s1", ExpiresAt: d("2028-02-29"), Cycle: Yearly}
	if y, _ = y.Renewed(); y.ExpiresAt != d("2029-02-28") {
		t.Errorf("Feb 29 2028 + 1 year = %v, want 2029-02-28", y.ExpiresAt)
	}
	if y, _ = y.Renewed(); y.ExpiresAt != d("2030-02-28") {
		t.Errorf("then %v, want 2030-02-28", y.ExpiresAt)
	}
	for y.ExpiresAt.Year < 2032 {
		y, _ = y.Renewed()
	}
	if y.ExpiresAt != d("2032-02-29") {
		t.Errorf("in the next leap year %v, want 2032-02-29", y.ExpiresAt)
	}
}

func TestRenewedOneTimeFails(t *testing.T) {
	s := Subscription{ServerID: "s1", ExpiresAt: d("2026-12-01"), Cycle: Once}
	if _, err := s.Renewed(); err == nil {
		t.Error("a one-time purchase was renewed")
	}
}

func TestValidate(t *testing.T) {
	ok := Subscription{ServerID: "s1", ExpiresAt: d("2026-12-01"), Cycle: Yearly, Price: "59.99", Currency: "USD", RenewURL: "https://example.com/billing"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, mut := range map[string]func(*Subscription){
		"no expiry":     func(s *Subscription) { s.ExpiresAt = Date{} },
		"bad cycle":     func(s *Subscription) { s.Cycle = "weekly" },
		"http link":     func(s *Subscription) { s.RenewURL = "http://example.com" },
		"bad price":     func(s *Subscription) { s.Price = "1,99" },
		"negative day":  func(s *Subscription) { s.RemindDays = []int{-1} },
		"no server":     func(s *Subscription) { s.ServerID = "" },
		"currency case": func(s *Subscription) { s.Currency = "usd" },
	} {
		s := ok
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, s)
		}
	}
}

func TestReminderText(t *testing.T) {
	s := Subscription{ServerID: "s1", ExpiresAt: d("2026-12-01"), Cycle: Monthly, Price: "5.00", Currency: "USD", RenewURL: "https://example.com/renew"}
	for today, want := range map[string]string{
		"2026-11-28": "Renewal: expires in 3 days",
		"2026-11-30": "Renewal: expires tomorrow",
		"2026-12-01": "Renewal: expires today",
		"2026-12-04": "Renewal: expired 3 days ago",
	} {
		r, ok := s.Due(d(today), Sent{})
		if !ok {
			t.Fatalf("%s: no reminder", today)
		}
		if r.Title() != want {
			t.Errorf("%s: title %q, want %q", today, r.Title(), want)
		}
		if body := r.Text(); !strings.Contains(body, "2026-12-01") || !strings.Contains(body, "5.00 USD") || !strings.Contains(body, "https://example.com/renew") {
			t.Errorf("%s: text %q", today, body)
		}
	}
}
