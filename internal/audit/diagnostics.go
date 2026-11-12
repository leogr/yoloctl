package audit

import (
	"strconv"
	"strings"
	"unicode"
)

// Retain a prefix, but always consume writes completely so Falco cannot block
// on a full stderr pipe. Event stdout never enters this diagnostic buffer.
type stderrPrefix struct {
	data      [4096]byte
	used      int
	lines     int
	truncated bool
}

func (p *stderrPrefix) Write(raw []byte) (int, error) {
	for _, b := range raw {
		if p.used == len(p.data) || p.lines == 8 {
			p.truncated = true
			break
		}
		p.data[p.used] = b
		p.used++
		if b == '\n' {
			p.lines++
		}
	}
	return len(raw), nil
}

func (p *stderrPrefix) summary() string {
	text := Clean(strings.TrimSpace(string(p.data[:p.used])))
	if text == "" {
		return ""
	}
	label := "; stderr prefix: "
	if p.truncated {
		label = "; stderr prefix (truncated): "
	}
	return label + text
}

// DiagnosticText escapes terminal controls while preserving ordinary diagnostic
// lines/tabs. Unlike retained event fields it has no feature size quota.
func DiagnosticText(raw string) string {
	var out strings.Builder
	for _, r := range strings.ToValidUTF8(raw, "�") {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029') {
			quoted := strconv.QuoteRuneToASCII(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
