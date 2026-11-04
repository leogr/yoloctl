package terminal

import (
	"bytes"
	"image/color"
	"testing"
)

func TestColorObserverUsesNativeStringBoundaries(t *testing.T) {
	var replies bytes.Buffer
	var q ColorQueries
	vt, err := New(4, 30, 10, func(p []byte) { q.Reply(p) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	q.Terminal = vt
	q.Send = func(p []byte) { replies.Write(p) }
	// UTF-8 continuation bytes have values resembling C1 ST/OSC. They are title
	// text, and must not manufacture a foreground query when host colors arrive.
	title := []byte("\x1b]2;Ü\u009d10;?\a")
	q.Observe(title)
	_, _ = vt.Write(title)
	colors := Colors{}
	colors.Defaults[0] = &color.RGBA{R: 1, G: 2, B: 3, A: 255}
	if err := q.Apply(colors); err != nil {
		t.Fatal(err)
	}
	if replies.Len() != 0 {
		t.Fatalf("title manufactured query reply: %q", replies.String())
	}
}
