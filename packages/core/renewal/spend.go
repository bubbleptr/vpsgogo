package renewal

import (
	"math/big"
	"sort"
	"strings"
)

// Amount is a sum of prices in one currency, written as exactly as the
// prices were: as many decimals as the most precise of them.
type Amount struct {
	Currency string // "" for plans without one
	Total    string
}

// Spend sums what falls due from from to to, both included: each date in
// the period a recurring plan expires on, and so is paid on, counts once.
// One-time plans were paid when bought; plans without a price count nothing.
// Totals are per currency, sorted by currency.
func Spend(subs []Subscription, from, to Date) []Amount {
	type sum struct {
		total    big.Rat
		decimals int
	}
	sums := map[string]*sum{}
	for _, s := range subs {
		if s.Price == "" || s.Cycle == Once {
			continue
		}
		price, ok := new(big.Rat).SetString(s.Price)
		if !ok {
			continue
		}
		for due := s; !due.ExpiresAt.After(to); {
			if !from.After(due.ExpiresAt) {
				t := sums[s.Currency]
				if t == nil {
					t = &sum{}
					sums[s.Currency] = t
				}
				t.total.Add(&t.total, price)
				if _, frac, ok := strings.Cut(s.Price, "."); ok {
					t.decimals = max(t.decimals, len(frac))
				}
			}
			var err error
			if due, err = due.Renewed(); err != nil {
				break
			}
		}
	}
	out := make([]Amount, 0, len(sums))
	for c, t := range sums {
		out = append(out, Amount{Currency: c, Total: t.total.FloatString(t.decimals)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}
