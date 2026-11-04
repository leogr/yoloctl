package term

import (
	"testing"
	"time"
)

func TestBellsAreLiveEventsNotRenderedState(t *testing.T) {
	e := newTestEmulator(t, 2, 40, nil, 20)
	var bells int
	e.SetBellHandler(func(n int) {
		bells += n
		_ = e.Frame() // callback must run outside the terminal mutex
	})
	for _, part := range []string{"first\a\x1b]2;ti", "tle\a\x1bP$q\a", "\x1b\\\a"} {
		feed(t, e, part)
	}
	if bells != 2 {
		t.Fatalf("counted %d bells; OSC terminators and DCS payloads are not bells", bells)
	}
	for range 10 {
		_ = e.Frame()
		_ = e.RenderLines()
	}
	e.Scroll(1)
	e.ScrollToBottom()
	e.Clear()
	e.FlushOutputFilters()
	if bells != 2 {
		t.Fatal("render/reset replayed a bell")
	}
}

func TestSessionAndConsoleDeliverBells(t *testing.T) {
	const script = `printf '\007\033]2;title\007\007'`
	events := make(chan int, 4)
	s, err := Start(Options{Argv: []string{"sh", "-c", script}, Rows: 4, Cols: 40, OnBell: func(n int) { events <- n }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	console := NewConsole(4, 40, 20, nil)
	t.Cleanup(func() { _ = console.Close() })
	console.SetBellHandler(func(n int) { events <- n })
	if _, err := console.Run(nil, "bell probe", "sh", "-c", script); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	total := 0
	for total < 4 {
		select {
		case n := <-events:
			total += n
		case <-deadline:
			t.Fatalf("only received %d of four live bells", total)
		}
	}
	if total != 4 {
		t.Fatalf("received %d bells, want four", total)
	}
}
