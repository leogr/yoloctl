package terminal

import "testing"

func TestNativePixelMousePreservesCapturedCoordinates(t *testing.T) {
	vt := newTestTerminal(t, 47, 201, 0, nil)
	_ = vt.ResizeGeometry(Geometry{Rows: 47, Cols: 201, CellWidth: 19, CellHeight: 42})
	_, _ = vt.Write([]byte("\x1b[?1000h\x1b[?1016h"))
	for _, c := range []struct {
		x, y   int
		action MouseAction
		want   string
	}{
		{0, 7, MousePress, "\x1b[<0;0;7M"},
		{-1, 16, MousePress, "\x1b[<0;-1;16M"},
		{-1, 16, MouseRelease, "\x1b[<0;-1;16m"},
		{12, 13, MousePress, "\x1b[<0;12;13M"},
		{12, 13, MouseMotion, ""}, // mode1000 still owns filtering
	} {
		p, err := vt.EncodeMouse(Mouse{Button: 1, Action: c.action, Pixels: &MousePixels{c.x, c.y, 19, 42}})
		if err != nil || string(p) != c.want {
			t.Fatalf("pixel event %v: %q, %v", c, p, err)
		}
	}
	// Another viewer sent the latest resize with a font half as wide/tall.
	_ = vt.ResizeGeometry(Geometry{Rows: 47, Cols: 201, CellWidth: 10, CellHeight: 20})
	p, err := vt.EncodeMouse(Mouse{Button: 1, Pixels: &MousePixels{39, 85, 20, 40}})
	if err != nil || string(p) != "\x1b[<0;20;43M" {
		t.Fatalf("scaled pixels: %q, %v", p, err)
	}
	// A pixel report must not change the existing cell-encoded formats.
	_, _ = vt.Write([]byte("\x1b[?1006h"))
	p, err = vt.EncodeMouse(Mouse{X: 2, Y: 3, Button: 1, Pixels: &MousePixels{39, 85, 20, 40}})
	if err != nil || string(p) != "\x1b[<0;3;4M" {
		t.Fatalf("cell format changed: %q, %v", p, err)
	}
}

func TestPixelMouseNeverInventsUnmeasuredCoordinates(t *testing.T) {
	vt := newTestTerminal(t, 24, 80, 0, nil)
	_, _ = vt.Write([]byte("\x1b[?1000h\x1b[?1016h"))
	if _, err := vt.EncodeMouse(Mouse{Button: 1, Pixels: &MousePixels{1, 2, 10, 20}}); err == nil {
		t.Fatal("pixel input accepted without canonical geometry")
	}
	_ = vt.ResizeGeometry(Geometry{Rows: 24, Cols: 80, CellWidth: 10, CellHeight: 20})
	for _, pos := range []*MousePixels{nil, {1, 2, 0, 0}, {1, 2, -1, 20}, {1, 2, 4097, 20}, {1 << 30, 2, 1, 1}} {
		if _, err := vt.EncodeMouse(Mouse{Button: 1, Pixels: pos}); err == nil {
			t.Fatalf("accepted unmeasured/out-of-range input %+v", pos)
		}
	}
}
