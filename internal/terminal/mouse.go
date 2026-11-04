package terminal

import (
	"errors"
	"math"
)

// MousePixels is an actual host report relative to the child text grid. Cell
// dimensions identify the reporting viewer's font, which may differ from the
// latest resizer's canonical font. Never synthesize this from cell centers.
type MousePixels struct {
	X, Y                  int
	CellWidth, CellHeight int
}

type MouseAction uint32

const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMotion
)

// Mouse uses zero-based cell coordinates. Negative positions are allowed for
// releases/drags outside the captured surface. Button follows Ghostty's enum:
// none=0, left=1, right=2, middle=3, wheel=4..7, extra buttons=8..11.
type Mouse struct {
	X, Y   int
	Button uint32
	Mods   uint16 // same modifier bits as Key
	Action MouseAction
	Pixels *MousePixels
}

// MouseMode reports effective native enums, not independently retained DEC
// bits. Resetting one tracking/format mode can change the effective mode while
// another DEC bit still reads as set.
type MouseMode struct {
	Tracking     int // none=0, X10=1, normal=2, drag=3, all-motion=4
	Format       int // legacy=0, UTF8=1, SGR=2, urxvt=3, pixels=4
	ShiftCapture MouseShiftCapture
}

// MouseShiftCapture is the child's XTSHIFTESCAPE preference. Host terminal
// policy can still force or forbid capture; the protocol cannot override that.
type MouseShiftCapture uint8

const (
	MouseShiftDefault MouseShiftCapture = iota
	MouseShiftSelection
	MouseShiftReport
)

func (t *Terminal) MouseMode() MouseMode {
	return MouseMode{Tracking: t.Get(14), Format: t.Get(15), ShiftCapture: MouseShiftCapture(t.Get(16))}
}

func (t *Terminal) EncodeMouse(m Mouse) ([]byte, error) {
	if t.err != nil {
		return nil, t.err
	}
	if m.Action > MouseMotion || m.Button > 11 || m.X < -1<<31 || m.X > 1<<31-1 || m.Y < -1<<31 || m.Y > 1<<31-1 {
		return nil, errors.New("invalid mouse event")
	}
	mode := t.MouseMode()
	if mode.Tracking == 0 {
		return nil, nil
	}
	x, y, pixels := m.X, m.Y, uint64(0)
	if mode.Format == 4 {
		g := t.Geometry()
		if m.Pixels == nil || m.Pixels.CellWidth <= 0 || m.Pixels.CellWidth > 4096 || m.Pixels.CellHeight <= 0 || m.Pixels.CellHeight > 4096 || g.CellWidth == 0 || g.CellHeight == 0 {
			return nil, errors.New("pixel mouse reporting needs measured host and session cell dimensions")
		}
		// Preserve the position within a cell across different-font viewers.
		// Native encoding rounds physical positions to the nearest pixel too.
		px := math.Round(float64(m.Pixels.X) * float64(g.CellWidth) / float64(m.Pixels.CellWidth))
		py := math.Round(float64(m.Pixels.Y) * float64(g.CellHeight) / float64(m.Pixels.CellHeight))
		// Native input uses f32; this range retains every integer exactly and
		// includes the maximum supported grid (4096 cells * 4096 pixels).
		if px < -1<<24 || px > 1<<24 || py < -1<<24 || py > 1<<24 {
			return nil, errors.New("pixel mouse position outside supported range")
		}
		x, y, pixels = int(px), int(py), 1
	}
	n := t.call("yolo_vt_mouse", t.ptr, uint64(uint32(int32(x))), uint64(uint32(int32(y))), uint64(m.Button), uint64(m.Mods), uint64(m.Action), pixels, t.scratch, scratchSize)
	if t.err != nil {
		return nil, t.err
	}
	if n > scratchSize {
		return nil, errors.New("terminal cannot encode this mouse event")
	}
	p, ok := t.mod.Memory().Read(uint32(t.scratch), uint32(n))
	if !ok {
		return nil, errors.New("terminal mouse result outside memory")
	}
	return append([]byte(nil), p...), nil
}
