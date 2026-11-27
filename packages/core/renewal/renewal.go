// Package renewal tracks when each server's plan expires and when to remind
// the user to renew it. See docs/SPEC.md §4.3.
//
// Dates are calendar dates: "expires on 2026-12-01" means that whole day in
// the user's time zone, and days are counted on the calendar, never as 24h.
package renewal

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Date is a calendar date, written "2006-01-02".
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the date t falls on in t's location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// ParseDate reads "2006-01-02".
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("renewal: date %q: want YYYY-MM-DD", s)
	}
	return DateOf(t), nil
}

func (d Date) String() string { return d.time().Format(time.DateOnly) }
func (d Date) IsZero() bool   { return d == Date{} }
func (d Date) time() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// AddDays returns the date n days later.
func (d Date) AddDays(n int) Date { return DateOf(d.time().AddDate(0, 0, n)) }

// After reports whether d is later than o.
func (d Date) After(o Date) bool { return d.time().After(o.time()) }

// DaysUntil counts calendar days from d to o; negative when o is earlier.
func (d Date) DaysUntil(o Date) int {
	// Both are UTC midnights, so the difference is a whole number of days.
	return int(o.time().Sub(d.time()).Hours() / 24)
}

func (d Date) MarshalText() ([]byte, error) {
	if d.IsZero() {
		return []byte{}, nil
	}
	return []byte(d.String()), nil
}

func (d *Date) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Date{}
		return nil
	}
	v, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// Cycle is how often a plan renews.
type Cycle string

const (
	Monthly   Cycle = "monthly"
	Quarterly Cycle = "quarterly"
	Yearly    Cycle = "yearly"
	Once      Cycle = "once" // bought once; it expires and is not renewed
)

func (c Cycle) months() (int, bool) {
	switch c {
	case Monthly:
		return 1, true
	case Quarterly:
		return 3, true
	case Yearly:
		return 12, true
	case Once:
		return 0, true
	}
	return 0, false
}

// DefaultRemindDays are the days before expiry a reminder goes out.
var DefaultRemindDays = []int{7, 3, 1, 0}

// Subscription is one server's plan.
type Subscription struct {
	ServerID  string `json:"serverId"`
	ExpiresAt Date   `json:"expiresAt"`
	// BillingDay is the day of the month the provider bills on. A plan billed
	// on the 31st expires on Feb 28 and then on Mar 31 again. 0 means the day
	// of ExpiresAt; renewing fills it in.
	BillingDay int    `json:"billingDay,omitempty"`
	Cycle      Cycle  `json:"cycle"`
	Price      string `json:"price,omitempty"`    // a decimal such as "5.99"
	Currency   string `json:"currency,omitempty"` // ISO 4217, such as "USD"
	RenewURL   string `json:"renewUrl,omitempty"`
	// RemindDays are the days before expiry to remind on; nil means
	// DefaultRemindDays.
	RemindDays []int `json:"remindDays,omitempty"`
}

var (
	priceRE    = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Validate checks what the user entered.
func (s Subscription) Validate() error {
	switch {
	case s.ServerID == "":
		return errors.New("renewal: no server")
	case s.ExpiresAt.IsZero():
		return errors.New("renewal: no expiry date")
	case s.BillingDay < 0 || s.BillingDay > 31:
		return fmt.Errorf("renewal: billing day %d", s.BillingDay)
	case s.Price != "" && !priceRE.MatchString(s.Price):
		return fmt.Errorf("renewal: price %q is not a number such as 5.99", s.Price)
	case s.Currency != "" && !currencyRE.MatchString(s.Currency):
		return fmt.Errorf("renewal: currency %q is not a code such as USD", s.Currency)
	}
	if _, ok := s.Cycle.months(); !ok {
		return fmt.Errorf("renewal: unknown cycle %q", s.Cycle)
	}
	if s.RenewURL != "" {
		if u, err := url.Parse(s.RenewURL); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("renewal: renew link %q is not an https URL", s.RenewURL)
		}
	}
	for _, n := range s.RemindDays {
		if n < 0 {
			return fmt.Errorf("renewal: remind %d days before", n)
		}
	}
	return nil
}

// DaysLeft counts calendar days from today to the expiry date: 0 on the day,
// negative once it has passed.
func (s Subscription) DaysLeft(today Date) int { return today.DaysUntil(s.ExpiresAt) }

// ErrOneTime is returned when renewing a plan that was bought once.
var ErrOneTime = errors.New("renewal: a one-time purchase does not renew")

// Renewed returns s after one more cycle was paid for: the expiry moves one
// cycle on from the current expiry, on the billing day.
func (s Subscription) Renewed() (Subscription, error) {
	n, ok := s.Cycle.months()
	if !ok {
		return s, fmt.Errorf("renewal: unknown cycle %q", s.Cycle)
	}
	if n == 0 {
		return s, ErrOneTime
	}
	if s.BillingDay == 0 {
		s.BillingDay = s.ExpiresAt.Day
	}
	first := time.Date(s.ExpiresAt.Year, s.ExpiresAt.Month+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	s.ExpiresAt = Date{first.Year(), first.Month(), min(s.BillingDay, last)}
	return s, nil
}

// Expired is the Threshold of the notice that a plan has expired.
const Expired = -1

// Reminder is a notice to send.
type Reminder struct {
	Sub       Subscription
	Threshold int // the remind day reached, or Expired
	DaysLeft  int
}

// Sent remembers, per expiry date, the lowest threshold already reminded
// on, so each goes out once and a renewal starts them over.
type Sent map[Date]int

// Record notes that r was sent. Older expiry dates are forgotten.
func (sent Sent) Record(r Reminder) {
	for k := range sent {
		if k != r.Sub.ExpiresAt {
			delete(sent, k)
		}
	}
	sent[r.Sub.ExpiresAt] = r.Threshold
}

// Due returns the reminder to send today, if any: the lowest remind day
// reached that is lower than any already sent for this expiry. Days the
// agent missed collapse into one notice with the days actually left.
func (s Subscription) Due(today Date, sent Sent) (Reminder, bool) {
	left := s.DaysLeft(today)
	threshold, reached := Expired, left < 0
	if !reached {
		days := s.RemindDays
		if days == nil {
			days = DefaultRemindDays
		}
		for _, n := range days {
			if left <= n && (!reached || n < threshold) {
				threshold, reached = n, true
			}
		}
	}
	if !reached {
		return Reminder{}, false
	}
	if last, ok := sent[s.ExpiresAt]; ok && last <= threshold {
		return Reminder{}, false
	}
	return Reminder{Sub: s, Threshold: threshold, DaysLeft: left}, true
}

// Title is the notice's headline.
func (r Reminder) Title() string {
	switch n := r.DaysLeft; {
	case n == 0:
		return "Renewal: expires today"
	case n == 1:
		return "Renewal: expires tomorrow"
	case n > 1:
		return "Renewal: expires in " + strconv.Itoa(n) + " days"
	case n == -1:
		return "Renewal: expired yesterday"
	default:
		return "Renewal: expired " + strconv.Itoa(-n) + " days ago"
	}
}

// Text says when, how much and where to renew.
func (r Reminder) Text() string {
	t := "The plan expires on " + r.Sub.ExpiresAt.String()
	if r.DaysLeft < 0 {
		t = "The plan expired on " + r.Sub.ExpiresAt.String()
	}
	if r.Sub.Price != "" {
		t += " · " + r.Sub.Price
		if r.Sub.Currency != "" {
			t += " " + r.Sub.Currency
		}
		if r.Sub.Cycle != Once {
			t += " " + string(r.Sub.Cycle)
		}
	}
	if r.Sub.RenewURL != "" {
		t += " · " + r.Sub.RenewURL
	}
	return t
}
