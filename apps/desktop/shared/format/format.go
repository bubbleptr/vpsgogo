// Package format writes sizes and rates for people.
package format

import "fmt"

// Bytes writes n in binary units, as "1.5 GB".
func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, i := float64(n)/unit, 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGTP"[i])
}

// Rate writes bytes per second, as "1.5 MB/s".
func Rate(perSecond float64) string {
	if perSecond < 0 {
		perSecond = 0
	}
	return Bytes(uint64(perSecond)) + "/s"
}

// Percent returns used of total as a percentage, 0 when total is.
func Percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}
