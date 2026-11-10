package audit

import "strings"

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
