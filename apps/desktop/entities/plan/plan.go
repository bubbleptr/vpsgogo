// Package plan shows a renewal plan the same way on every page: how many
// days it has left, in words and in the color of its urgency.
package plan

import (
	"fmt"
	"strings"

	"github.com/bubbleptr/vpsgogo/apps/desktop/shared/theme"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/ui"
)

// Left says how long s has left on today, as "12 days left" or
// "Expired 3 days ago".
func Left(s renewal.Subscription, today renewal.Date) string {
	switch n := s.DaysLeft(today); {
	case n == 0:
		return "Expires today"
	case n == 1:
		return "1 day left"
	case n > 1:
		return fmt.Sprintf("%d days left", n)
	case n == -1:
		return "Expired yesterday"
	default:
		return fmt.Sprintf("Expired %d days ago", -n)
	}
}

// Summary describes s without its days left, as
// "Expires 2026-12-15 · monthly · 5.99 USD".
func Summary(s renewal.Subscription) string {
	parts := []string{"Expires " + s.ExpiresAt.String(), string(s.Cycle)}
	if s.Price != "" {
		parts = append(parts, strings.TrimSpace(s.Price+" "+s.Currency))
	}
	return strings.Join(parts, " · ")
}

// Badge shows Left in the color of s's urgency.
func Badge(c *ui.Context, s renewal.Subscription, today renewal.Date) ui.Element {
	t := ui.Text(c, Left(s, today)).FontWeight(700)
	switch s.Urgency(today) {
	case renewal.Overdue:
		return t.TextColor(theme.Coral)
	case renewal.Soon:
		return t.TextColor(theme.Amber)
	}
	return t.TextColor(theme.For(c).Ink)
}
