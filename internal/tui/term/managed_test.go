package term

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

func TestManagedSnapshotKeepsEachViewHistoryLimit(t *testing.T) {
	source, err := terminal.New(2, 12, 40, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	for i := range 20 {
		_, _ = source.Write(fmt.Appendf(nil, "line%02d\r\n", i))
	}
	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{3, 8} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			e := newEmulator(2, 12, nil, limit)
			defer func() { _ = e.Close() }()
			if err := e.applyView(context.Background(), session.ViewEvent{Snapshot: snapshot}); err != nil {
				t.Fatal(err)
			}
			if f := e.Frame(); f.History != limit {
				t.Fatalf("restored history=%d, configured cap=%d", f.History, limit)
			}
			e.Scroll(100)
			f := e.Frame()
			if f.Offset != limit || !strings.Contains(f.Lines[0], fmt.Sprintf("line%02d", 19-limit)) {
				t.Fatalf("capped history mapping: %+v", f)
			}
			if err := e.applyView(context.Background(), session.ViewEvent{Data: []byte("live\r\n")}); err != nil {
				t.Fatal(err)
			}
			if f := e.Frame(); f.History != limit || f.Offset != limit || !strings.Contains(f.Lines[0], fmt.Sprintf("line%02d", 20-limit)) {
				t.Fatalf("live output failed to clamp history anchor: %+v", f)
			}
			// A local visibility cap must not alter native retention or its snapshot.
			data, err := e.vt.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := terminal.RestoreSnapshot(context.Background(), data, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = restored.Close() }()
			if restored.Get(9) != 20 {
				t.Fatalf("view cap discarded canonical history: %d", restored.Get(9))
			}
		})
	}
}

func TestManagedSnapshotContinuationQuietEffectsAndInputModes(t *testing.T) {
	var replies bytes.Buffer
	source, err := terminal.New(6, 30, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	_, _ = source.Write([]byte("HEADER\x1b[3;4H\x1b[>3u\x1b[?2004h\x1b[?1002h\x1b[?1006h\a\x1b[38;2;"))
	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	e := newEmulator(2, 5, &replies, 100)
	defer func() { _ = e.Close() }()
	var copies []termio.OSC52
	bells := 0
	e.SetOSC52Handler(func(v termio.OSC52) { copies = append(copies, v) })
	e.SetBellHandler(func(n int) { bells += n })
	// A setup command left an unfinished control. It cannot contaminate native state.
	_, _ = e.Write([]byte("progress\x1b]52;c;"))
	if err := e.applyView(context.Background(), session.ViewEvent{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if bells != 0 || len(copies) != 0 || replies.Len() != 0 {
		t.Fatal("snapshot replayed effects")
	}
	if e.KeyboardFlags() != 3 || !e.vt.Mode(2004) || !e.vt.Mode(1002) {
		t.Fatal("snapshot lost input modes")
	}
	_, _ = e.writeOutput([]byte("\x1bcFORGED\x1b[>0u"), true)
	continuation := []byte("1;2;3mLIVE\x1b[6n\x1b[?u\a")
	_, _ = source.Write(continuation)
	if err := e.applyView(context.Background(), session.ViewEvent{Data: continuation}); err != nil {
		t.Fatal(err)
	}
	expected, _ := source.Lines(false, false, 0, 6)
	got := e.Frame().Lines
	if strings.Join(got, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("replica diverged: %q != %q", got, expected)
	}
	if replies.Len() != 0 || bells != 1 || len(copies) != 0 {
		t.Fatalf("effects replies=%q bells=%d copies=%v", replies.String(), bells, copies)
	}
	if err := e.applyView(context.Background(), session.ViewEvent{Clipboard: &termio.OSC52{Selection: "c", Payload: "YQ=="}}); err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 {
		t.Fatal("live clipboard event missing")
	}
}

func TestManagedGeometryAndHostColorsAreCanonical(t *testing.T) {
	source, err := terminal.New(6, 30, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	_, _ = source.Write([]byte("\x1b]4;1;#112233\a\x1b[31mINDEX"))
	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	e := newEmulator(2, 5, nil, 10)
	defer func() { _ = e.Close() }()
	if err := e.applyView(context.Background(), session.ViewEvent{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	var published terminal.Colors
	e.sendColors = func(c terminal.Colors) { published = c }
	e.SetColors(color.White, color.Black)
	e.SetPaletteColor(1, color.RGBA{R: 240, A: 255})
	if published.Defaults[0] == nil || published.Palette[1] == nil {
		t.Fatal("host metadata not reported")
	}
	// A local resize never mutates a replica before its canonical ordered event.
	e.Resize(8, 50)
	if f := e.Frame(); f.Rows != 6 || f.Cols != 30 {
		t.Fatalf("local resize mutated replica: %+v", f)
	}
	colors := terminal.Colors{}
	colors.Palette[1] = &color.RGBA{G: 100, A: 255}
	if err := e.applyView(context.Background(), session.ViewEvent{Size: terminal.Geometry{Rows: 8, Cols: 50}, Colors: &colors}); err != nil {
		t.Fatal(err)
	}
	c, modified := e.vt.PaletteColor(1)
	if f := e.Frame(); f.Rows != 8 || f.Cols != 50 || !modified || c != (color.RGBA{R: 17, G: 34, B: 51, A: 255}) {
		t.Fatalf("canonical state: frame=%+v color=%v modified=%v", f, c, modified)
	}
}

func TestManagedHoldEndsOnlyOnCanonicalEvent(t *testing.T) {
	source, err := terminal.New(2, 12, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	_, _ = source.Write([]byte("old"))
	snapshot, err := source.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	e := newEmulator(2, 12, nil, 10)
	defer func() { _ = e.Close() }()
	if err := e.applyView(context.Background(), session.ViewEvent{Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := e.applyView(context.Background(), session.ViewEvent{Data: []byte("\x1b[?2026h\x1b[Hnew\x1b[38;2;")}); err != nil {
		t.Fatal(err)
	}
	if f := e.Frame(); !strings.Contains(f.Lines[0], "old") || e.holdTimer != nil {
		t.Fatal("replica did not hold output or started an independent timer")
	}
	if err := e.applyView(context.Background(), session.ViewEvent{EndHold: true}); err != nil {
		t.Fatal(err)
	}
	if f := e.Frame(); !strings.Contains(f.Lines[0], "new") || e.vt.Mode(2026) {
		t.Fatal("canonical release did not publish the frame and mode")
	}
	if err := e.applyView(context.Background(), session.ViewEvent{Data: []byte("1;2;3mEND")}); err != nil {
		t.Fatal(err)
	}
	if got := e.GridText(); !strings.Contains(got, "newEND") {
		t.Fatalf("release corrupted continuation: %q", got)
	}
}
