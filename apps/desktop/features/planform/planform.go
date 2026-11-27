// Package planform is the form that enters or edits a server's renewal
// plan, shared by the add-server wizard and the server page. The rules
// are core's: Plan checks what was entered with Subscription.Validate.
package planform

import (
	"strings"
	"time"

	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// Cycles are the choices of the billing cycle, as core names them.
var Cycles = []string{string(renewal.Monthly), string(renewal.Quarterly), string(renewal.Yearly), string(renewal.Once)}

// Form is what the user entered.
type Form struct {
	Expires  time.Time // a UTC midnight
	Cycle    string
	Price    string
	Currency string
	RenewURL string
	// Err is the last error of Plan, shown below the fields.
	Err string
	// keep holds what the form does not edit, from the plan it started with.
	keep renewal.Subscription
}

// New returns an empty form whose plan expires a month after today.
func New(today renewal.Date) *Form {
	return &Form{
		Expires:  midnight(today.AddDays(30)),
		Cycle:    string(renewal.Monthly),
		Currency: "USD",
	}
}

// Edit returns a form holding s.
func Edit(s renewal.Subscription) *Form {
	return &Form{
		Expires:  midnight(s.ExpiresAt),
		Cycle:    string(s.Cycle),
		Price:    s.Price,
		Currency: s.Currency,
		RenewURL: s.RenewURL,
		keep:     s,
	}
}

func midnight(d renewal.Date) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// Plan returns server id's plan as entered, or the reason it is not valid,
// which it also keeps in Err.
func (f *Form) Plan(serverID string) (renewal.Subscription, error) {
	s := f.keep
	s.ServerID = serverID
	s.ExpiresAt = renewal.DateOf(f.Expires)
	if s.BillingDay != 0 && s.ExpiresAt != f.keep.ExpiresAt {
		s.BillingDay = 0 // a new expiry sets a new billing day
	}
	s.Cycle = renewal.Cycle(f.Cycle)
	s.Price = strings.TrimSpace(f.Price)
	s.Currency = strings.ToUpper(strings.TrimSpace(f.Currency))
	s.RenewURL = strings.TrimSpace(f.RenewURL)
	if err := s.Validate(); err != nil {
		f.Err = err.Error()
		return renewal.Subscription{}, err
	}
	f.Err = ""
	return s, nil
}

// View builds the fields and the last error.
func (f *Form) View(c *ui.Context) {
	ui.Form(c, func() {
		ui.Field(c, "Expires on", func() { ui.DateInput(c, &f.Expires) })
		ui.Field(c, "Billing cycle", func() { ui.Select(c, &f.Cycle, Cycles) })
		ui.Field(c, "Price", func() { ui.TextInput(c, &f.Price).Placeholder("5.99") })
		ui.Field(c, "Currency", func() { ui.TextInput(c, &f.Currency) })
		ui.Field(c, "Renew URL", func() { ui.TextInput(c, &f.RenewURL).Placeholder("https://") })
	})
	if f.Err != "" {
		ui.Text(c, f.Err).TextColor(theme.Coral)
	}
}
