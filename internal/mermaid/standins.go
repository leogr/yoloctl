package mermaid

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// The renderer sizes text by whole Unicode sequences but writes it into its
// grid one character at a time. A cluster of several characters can then
// occupy a different number of cells than the view draws: 🛠️ (U+1F6E0 + VS16)
// is sized as two cells but written into one, 👍🏽 is sized as two but written
// into four. Before rendering, each such cluster is replaced by stand-in
// characters that the renderer measures and writes as the cluster's width in
// the view's method; the cluster returns in place of its stand-ins afterwards.
var standInPools = [...]struct{ first, last rune }{
	1: {0xF0000, 0xFFFFD}, // Supplementary Private Use Area-A: one cell
	2: {0x20000, 0x2A6DF}, // CJK Unified Ideographs Extension B: two cells
}

// standIn describes one stand-in character. The first character of a run
// carries the whole run and the cluster it replaces; the others are empty.
type standIn struct {
	run, cluster string
}

// standsIn reports whether a cluster of several characters may be replaced.
// Mermaid syntax is ASCII (punctuation, and letters such as the x and o link
// ends), and a combining mark joins the character before it (":" + U+0301) as
// a prepended mark joins the one after it (U+0600 + "]"). A cluster holding
// any ASCII character other than a leading digit therefore stays for the
// parser. A leading digit is a keycap such as 1️⃣: admission refuses marks on
// identifiers, so the digit is text.
func standsIn(cluster string) bool {
	for i, r := range cluster {
		if r < utf8.RuneSelf && (i > 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// withStandIns replaces every cluster of more than one character that has a
// width in method and stands in. Single characters, and the source's own
// characters from the stand-in pools, pass unchanged; a cluster keeps its text
// if a pool runs out.
func withStandIns(source string, method ansi.Method) (string, map[rune]standIn) {
	present := map[rune]bool{}
	for _, r := range source {
		if r >= standInPools[2].first {
			present[r] = true
		}
	}
	next := [len(standInPools)]rune{1: standInPools[1].first, 2: standInPools[2].first}
	take := func(cells int) (rune, bool) {
		for r := next[cells]; r <= standInPools[cells].last; r++ {
			if !present[r] {
				next[cells] = r + 1
				return r, true
			}
		}
		next[cells] = standInPools[cells].last + 1
		return 0, false
	}
	standIns := map[rune]standIn{}
	runs := map[string]string{}
	var out strings.Builder
	for s := source; s != ""; {
		cluster, cells := ansi.FirstGraphemeCluster(s, method)
		if cluster == "" {
			_, n := utf8.DecodeRuneInString(s)
			cluster = s[:n]
		}
		s = s[len(cluster):]
		if cells == 0 || utf8.RuneCountInString(cluster) < 2 || !standsIn(cluster) {
			out.WriteString(cluster)
			continue
		}
		run, seen := runs[cluster]
		if !seen {
			run = cluster
			var chars []rune
			for left := cells; left > 0; {
				size := min(left, 2)
				r, ok := take(size)
				if !ok {
					chars = nil
					break
				}
				chars = append(chars, r)
				left -= size
			}
			if chars != nil {
				run = string(chars)
				for _, r := range chars[1:] {
					standIns[r] = standIn{}
				}
				standIns[chars[0]] = standIn{run, cluster}
			}
			runs[cluster] = run
		}
		out.WriteString(run)
	}
	return out.String(), standIns
}

// restoreStandIns puts each cluster back in place of its whole run. It reports
// false when the renderer split or cut a run (only clusters wider than two
// cells, in wcwidth, have runs of more than one character): the drawing would
// lose that cluster, so the view shows the source instead.
func restoreStandIns(drawing string, standIns map[rune]standIn) (string, bool) {
	if len(standIns) == 0 {
		return drawing, true
	}
	var out strings.Builder
	for i := 0; i < len(drawing); {
		r, n := utf8.DecodeRuneInString(drawing[i:])
		s, ok := standIns[r]
		switch {
		case !ok:
			out.WriteString(drawing[i : i+n])
		case s.run != "" && strings.HasPrefix(drawing[i:], s.run):
			out.WriteString(s.cluster)
			n = len(s.run)
		default:
			return "", false
		}
		i += n
	}
	return out.String(), true
}
