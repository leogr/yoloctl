package terminal

import (
	"bytes"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/termio"
)

// Colors is measured host metadata. A nil entry means unknown, never black.
// A single session uses one set of defaults; application overrides remain in VT.
type Colors struct {
	Defaults [2]*color.RGBA   `json:"defaults"`
	Palette  [256]*color.RGBA `json:"palette"`
}

// Merge retains measurements when a replacement viewer cannot report a value.
func (c Colors) Merge(next Colors) Colors {
	for i, v := range next.Defaults {
		if v != nil {
			c.Defaults[i] = v
		}
	}
	for i, v := range next.Palette {
		if v != nil {
			c.Palette[i] = v
		}
	}
	return c
}
func (c Colors) Empty() bool { return c.Equal(Colors{}) }
func (c Colors) Equal(other Colors) bool {
	equal := func(a, b *color.RGBA) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	for i, v := range c.Defaults {
		if !equal(v, other.Defaults[i]) {
			return false
		}
	}
	for i, v := range c.Palette {
		if !equal(v, other.Palette[i]) {
			return false
		}
	}
	return true
}

// ColorQueries belongs to the authoritative parser. Replica terminals must
// neither answer native queries nor retain pending host-color replies.
// All calls are serialized with the associated terminal by its owner.
type ColorQueries struct {
	Terminal       *Terminal
	Send           func([]byte)
	Known          Colors
	pending        [2]string
	palettePending [256]string
	parser         *ansi.Parser
	last           byte
}

func (q *ColorQueries) Observe(p []byte) {
	if q.parser == nil {
		q.parser = ansi.NewParser()
		q.parser.SetDataSize(256)
		q.parser.SetHandler(ansi.Handler{HandleOsc: func(cmd int, data []byte) {
			if cmd != 10 && cmd != 11 {
				return
			}
			fields := strings.Split(string(data), ";")
			for i, v := range fields[1:] {
				slot := cmd - 10 + i
				if slot >= 2 {
					break
				}
				if v == "?" && q.Known.Defaults[slot] == nil {
					q.pending[slot] = "\x1b\\"
					if q.last == '\a' {
						q.pending[slot] = "\a"
					}
				}
			}
		}})
	}
	for _, b := range p {
		q.last = b
		q.parser.Advance(b)
	}
}

func (q *ColorQueries) Reply(p []byte) {
	for bytes.HasPrefix(p, []byte("\x1b]4;")) {
		end := "\x1b\\"
		n := bytes.Index(p, []byte(end))
		if bell := bytes.IndexByte(p, '\a'); bell >= 0 && (n < 0 || bell < n) {
			n, end = bell, "\a"
		}
		if n < 0 {
			break
		}
		n += len(end)
		entries, ok := termio.ParsePaletteReport(string(p[:n]))
		if !ok {
			break
		}
		for _, entry := range entries {
			_, modified := q.Terminal.PaletteColor(entry.Index)
			if q.Known.Palette[entry.Index] == nil && !modified {
				q.palettePending[entry.Index] = end
				continue
			}
			q.palettePending[entry.Index] = ""
			q.replyPalette(entry.Index, entry.Color, end)
		}
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	for slot := range 2 {
		if bytes.Contains(p, []byte("\x1b]"+strconv.Itoa(10+slot)+";")) {
			q.pending[slot] = ""
		}
	}
	q.Send(p)
}

func (q *ColorQueries) replyPalette(index int, c color.Color, end string) {
	r, g, b, _ := c.RGBA()
	q.Send(fmt.Appendf(nil, "\x1b]4;%d;rgb:%04x/%04x/%04x%s", index, r, g, b, end))
}

func (q *ColorQueries) Apply(colors Colors) error {
	q.Known = colors
	for slot, c := range colors.Defaults {
		if c == nil {
			continue
		}
		q.Terminal.SetColor(slot, c)
		if q.pending[slot] != "" {
			r, g, b, _ := c.RGBA()
			q.Send(fmt.Appendf(nil, "\x1b]%d;rgb:%04x/%04x/%04x%s", slot+10, r, g, b, q.pending[slot]))
			q.pending[slot] = ""
		}
	}
	for index, c := range colors.Palette {
		if c == nil {
			continue
		}
		if err := q.Terminal.SetPaletteColor(index, c); err != nil {
			return err
		}
		if q.palettePending[index] != "" {
			current, _ := q.Terminal.PaletteColor(index)
			q.replyPalette(index, current, q.palettePending[index])
			q.palettePending[index] = ""
		}
	}
	return q.Terminal.Err()
}
