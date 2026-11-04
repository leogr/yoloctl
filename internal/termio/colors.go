package termio

import (
	"image/color"
	"strconv"
	"strings"
)

type PaletteEntry struct {
	Index int
	Color color.RGBA
}

// ParsePaletteReport accepts only complete, bounded OSC 4 RGB reports. Host
// replies are data, never controls to forward to a child. Reject an entire
// malformed batch rather than applying a partially decoded palette.
func ParsePaletteReport(sequence string) ([]PaletteEntry, bool) {
	if len(sequence) > 16*1024 {
		return nil, false
	}
	body, ok := strings.CutPrefix(sequence, "\x1b]4;")
	if !ok {
		body, ok = strings.CutPrefix(sequence, "\x9d4;")
	}
	if !ok {
		return nil, false
	}
	for _, end := range []string{"\x1b\\", "\a", "\x9c"} {
		if v, found := strings.CutSuffix(body, end); found {
			body, ok = v, true
			break
		} else {
			ok = false
		}
	}
	if !ok {
		return nil, false
	}
	fields := strings.Split(body, ";")
	if len(fields) < 2 || len(fields) > 512 || len(fields)%2 != 0 {
		return nil, false
	}
	entries := make([]PaletteEntry, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		index, err := strconv.ParseUint(fields[i], 10, 8)
		if err != nil || len(fields[i]) > 3 || fields[i] == "" || fields[i][0] == '+' {
			return nil, false
		}
		rgb, ok := strings.CutPrefix(fields[i+1], "rgb:")
		if !ok {
			return nil, false
		}
		parts := strings.Split(rgb, "/")
		if len(parts) != 3 {
			return nil, false
		}
		var channels [3]uint8
		for j, part := range parts {
			if len(part) < 1 || len(part) > 4 || part[0] == '+' {
				return nil, false
			}
			value, err := strconv.ParseUint(part, 16, 16)
			if err != nil {
				return nil, false
			}
			channels[j] = uint8((value * 65535 / ((1 << (4 * len(part))) - 1)) >> 8)
		}
		entries = append(entries, PaletteEntry{int(index), color.RGBA{R: channels[0], G: channels[1], B: channels[2], A: 255}})
	}
	return entries, true
}
