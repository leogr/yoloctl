package terminal

import (
	"bytes"
	"context"
	"testing"
)

func TestTerminalReportsItsCellGrid(t *testing.T) {
	var replies bytes.Buffer
	vt := newTestTerminal(t, 47, 201, 0, &replies)
	_, _ = vt.Write([]byte("\x1b[18t"))
	if got, want := replies.String(), "\x1b[8;47;201t"; got != want {
		t.Fatalf("grid reply = %q, want %q", got, want)
	}
}

func TestTerminalMeasuredGeometryQueriesAndSnapshot(t *testing.T) {
	var replies bytes.Buffer
	vt := newTestTerminal(t, 47, 201, 0, &replies)
	g := Geometry{Rows: 47, Cols: 201, CellWidth: 19, CellHeight: 42}
	if err := vt.ResizeGeometry(g); err != nil {
		t.Fatal(err)
	}
	query := "\x1b[14t\x1b[16t\x1b[18t"
	want := "\x1b[4;1974;3819t\x1b[6;42;19t\x1b[8;47;201t"
	for _, b := range []byte(query) {
		_, _ = vt.Write([]byte{b})
	}
	if replies.String() != want || vt.Geometry() != g {
		t.Fatalf("geometry=%+v, replies=%q; want %+v, %q", vt.Geometry(), replies.String(), g, want)
	}
	data, err := vt.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	replies.Reset()
	restored, err := RestoreSnapshot(context.Background(), data, func(p []byte) { _, _ = replies.Write(p) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if replies.Len() != 0 {
		t.Fatal("snapshot restore emitted a size reply")
	}
	_, _ = restored.Write([]byte(query))
	if replies.String() != want || restored.Geometry() != g {
		t.Fatalf("restored geometry=%+v, replies=%q", restored.Geometry(), replies.String())
	}
	// Cell-only resize retains measured font dimensions. RIS resets terminal
	// modes, not the physical dimensions of the viewer.
	if err := restored.Resize(20, 80); err != nil {
		t.Fatal(err)
	}
	_, _ = restored.Write([]byte("\x1bc"))
	g.Rows, g.Cols = 20, 80
	if restored.Geometry() != g {
		t.Fatalf("cell resize or RIS lost measured geometry: %+v", restored.Geometry())
	}
}

func TestTerminalSizeNotificationsAndUnknownPixels(t *testing.T) {
	var replies bytes.Buffer
	vt := newTestTerminal(t, 10, 20, 0, &replies)
	_, _ = vt.Write([]byte("\x1b[14t\x1b[16t"))
	if got, want := replies.String(), "\x1b[4;0;0t\x1b[6;0;0t"; got != want {
		t.Fatalf("unknown pixels = %q, want %q", got, want)
	}
	_ = vt.ResizeGeometry(Geometry{Rows: 10, Cols: 20, CellWidth: 9, CellHeight: 18})
	replies.Reset()
	_, _ = vt.Write([]byte("\x1b[?2048h"))
	if got, want := replies.String(), "\x1b[48;10;20;180;180t"; got != want {
		t.Fatalf("initial size event = %q, want %q", got, want)
	}
	replies.Reset()
	// A font-only change must notify the application too.
	if err := vt.ResizeGeometry(Geometry{Rows: 10, Cols: 20, CellWidth: 11, CellHeight: 22}); err != nil {
		t.Fatal(err)
	}
	if got, want := replies.String(), "\x1b[48;10;20;220;220t"; got != want {
		t.Fatalf("font size event = %q, want %q", got, want)
	}
	replies.Reset()
	_, _ = vt.Write([]byte("\x1b[?2048l"))
	_ = vt.ResizeGeometry(Geometry{Rows: 12, Cols: 30})
	if replies.Len() != 0 || vt.Geometry().CellWidth != 0 {
		t.Fatalf("disabled notification or unknown dimensions lost: %q, %+v", replies.String(), vt.Geometry())
	}
}

func TestTerminalRejectsInvalidGeometryWithoutMutation(t *testing.T) {
	vt := newTestTerminal(t, 10, 20, 0, nil)
	for _, bad := range []Geometry{
		{}, {Rows: -1, Cols: 20}, {Rows: 10, Cols: 20, CellWidth: -1},
		{Rows: 10, Cols: 20, CellWidth: 9}, {Rows: 10, Cols: 20, CellHeight: 18},
		{Rows: 10, Cols: 20, CellWidth: 4097, CellHeight: 18},
		{Rows: 10, Cols: 20, CellWidth: 9, CellHeight: 4097},
		{Rows: 4096, Cols: 4096},
	} {
		before := vt.Geometry()
		if err := vt.ResizeGeometry(bad); err == nil || vt.Geometry() != before || vt.Err() != nil {
			t.Fatalf("invalid geometry %+v changed terminal: %+v, %v", bad, vt.Geometry(), err)
		}
	}
}
