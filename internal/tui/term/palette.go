package term

import "image/color"

func (e *emulator) reply(p []byte) {
	if !e.replica {
		e.queries.Reply(p)
	}
}

func (e *emulator) SetPaletteColor(index int, c color.Color) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if index < 0 || index > 255 || c == nil {
		return
	}
	e.palette[index] = c
	e.applyHostColorsLocked()
}

func (e *emulator) SetPalette(palette [256]color.Color) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, c := range palette {
		if c != nil {
			e.palette[i] = c
		}
	}
	e.applyHostColorsLocked()
}
