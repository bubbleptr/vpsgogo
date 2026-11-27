package renewal

import (
	"reflect"
	"testing"
)

func TestSpendCountsEachRenewalInThePeriod(t *testing.T) {
	subs := []Subscription{
		{ServerID: "a", ExpiresAt: d("2027-01-31"), Cycle: Monthly, Price: "5.99", Currency: "USD"},
		{ServerID: "b", ExpiresAt: d("2027-03-15"), Cycle: Yearly, Price: "30", Currency: "USD"},
		{ServerID: "c", ExpiresAt: d("2027-02-10"), Cycle: Quarterly, Price: "0.1", Currency: "EUR"},
		{ServerID: "d", ExpiresAt: d("2027-01-05"), Cycle: Once, Price: "99", Currency: "USD"}, // paid already
		{ServerID: "e", ExpiresAt: d("2027-01-20"), Cycle: Monthly},                            // no price
	}
	// January–December 2027: a renews 12 times (Jan 31, Feb 28, …), b once,
	// c on Feb 10, May 10, Aug 10 and Nov 10.
	got := Spend(subs, d("2027-01-01"), d("2027-12-31"))
	want := []Amount{{Currency: "EUR", Total: "0.4"}, {Currency: "USD", Total: "101.88"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("2027: %+v, want %+v", got, want)
	}
	// February only: a on Feb 28, c on Feb 10.
	got = Spend(subs, d("2027-02-01"), d("2027-02-28"))
	want = []Amount{{Currency: "EUR", Total: "0.1"}, {Currency: "USD", Total: "5.99"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("February: %+v, want %+v", got, want)
	}
}

func TestSpendSkipsDatesBeforeThePeriod(t *testing.T) {
	// Expired in 2026 and never marked renewed: the period after it still
	// counts the renewals that come due, not the missed one.
	s := Subscription{ServerID: "a", ExpiresAt: d("2026-11-15"), Cycle: Monthly, Price: "2", Currency: "USD"}
	got := Spend([]Subscription{s}, d("2027-01-01"), d("2027-01-31"))
	want := []Amount{{Currency: "USD", Total: "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%+v, want %+v", got, want)
	}
	if got := Spend([]Subscription{s}, d("2026-10-01"), d("2026-10-31")); len(got) != 0 {
		t.Errorf("before the plan's first expiry: %+v", got)
	}
}
