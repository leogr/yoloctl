package spaceview

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"
)

// humanBytes is a compact binary size: 0, 512, 12k, 380M, 1.4G.
func humanBytes(v uint64) string {
	return humanFloat(float64(v))
}

// humanRate is humanBytes for a per-second rate.
func humanRate(v float64) string { return humanFloat(v) }

func humanFloat(v float64) string {
	units := []string{"", "k", "M", "G", "T"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 || v >= 10 {
		return fmt.Sprintf("%.0f%s", v, units[i])
	}
	return oneDecimal(v) + units[i]
}

// gib formats bytes as GiB with one decimal place.
func gib(v uint64) string {
	g := float64(v) / (1 << 30)
	if g >= 100 {
		return fmt.Sprintf("%.0f", g)
	}
	return oneDecimal(g)
}

// oneDecimal drops a trailing ".0" after rounding to tenths.
func oneDecimal(v float64) string {
	r := math.Round(v*10) / 10
	if r == math.Trunc(r) {
		return fmt.Sprintf("%.0f", r)
	}
	return fmt.Sprintf("%.1f", r)
}

// humanDuration is a coarse age: 12s, 4m, 2h41m, 3d4h.
func humanDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// homeShort abbreviates the host home directory to ~.
func homeShort(path, home string) string {
	if home == "" || path == "" {
		return path
	}
	home = filepath.Clean(home)
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
