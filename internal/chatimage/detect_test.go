package chatimage

import (
	"testing"
	"time"
)

func TestParseTerminalVersion(t *testing.T) {
	for reply, want := range map[string]struct {
		ok, supported bool
	}{
		"kitty(0.35.2)":         {true, true},
		"kitty(0.28.0)":         {true, true},
		"kitty(0.27.9)":         {true, false},
		"ghostty 1.3.1":         {true, true},
		"ghostty 1.4.0-dev+abc": {true, true},
		"ghostty 1.3.0":         {true, false},
		"libghostty":            {false, false},
		"xterm(388)":            {false, false},
		"WezTerm 20240203":      {false, false},
		"kitty":                 {false, false},
		"":                      {false, false},
	} {
		got, ok := ParseTerminalVersion(reply)
		if ok != want.ok || got.Supported() != want.supported {
			t.Errorf("%q: ok=%v supported=%v (%+v)", reply, ok, got.Supported(), got)
		}
	}
}

func TestProbeNeedsVersionGraphicsAndCells(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewProbe(3, 77, now)
	if q := p.GraphicsQuery(); q != "\x1b_Gi=77,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\" {
		t.Fatalf("query %q", q)
	}
	if _, done := p.Result(now, true); done {
		t.Fatal("done before any reply")
	}
	p.Version("kitty(0.35.2)")
	p.Graphics(12, "OK") // another query's reply does not count
	if _, done := p.Result(now, true); done {
		t.Fatal("done on an uncorrelated graphics reply")
	}
	p.Graphics(77, "OK")
	if ok, done := p.Result(now, false); ok || done {
		t.Fatal("supported without measured cells")
	}
	if ok, done := p.Result(now, true); !ok || !done || p.Terminal().Name != "kitty" {
		t.Fatalf("result %v %v", ok, done)
	}
	// Failures end the probe at once; silence ends it at the budget.
	refused := NewProbe(1, 5, now)
	refused.Graphics(5, "ENOTSUPPORTED:no")
	if ok, done := refused.Result(now, true); ok || !done {
		t.Fatal("refusal not final")
	}
	nested := NewProbe(1, 5, now)
	nested.Version("libghostty")
	if ok, done := nested.Result(now, true); ok || !done {
		t.Fatal("libghostty not final")
	}
	silent := NewProbe(1, 5, now)
	if _, done := silent.Result(now.Add(ProbeBudget-time.Millisecond), true); done {
		t.Fatal("ended early")
	}
	if ok, done := silent.Result(now.Add(ProbeBudget), true); ok || !done {
		t.Fatal("silence not ended at the budget")
	}
}

func TestIDsNeverRepeatOrReturnZero(t *testing.T) {
	a := NewIDs(0xfffffe)
	seen := map[uint32]bool{}
	for i := 0; i < 1000; i++ {
		id, ok := a.Next()
		if !ok || id == 0 || id > 0xffffff || seen[id] {
			t.Fatalf("id %d ok=%v at %d", id, ok, i)
		}
		seen[id] = true
	}
}

// Nothing counts after the budget, and Expire cannot be bypassed (review
// step 5 F1).
func TestProbeDeadlineBeatsLateSuccess(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewProbe(1, 9, now)
	p.Version("ghostty 1.3.1")
	p.Graphics(9, "OK")
	if ok, done := p.Result(now.Add(ProbeBudget+time.Millisecond), true); ok || !done {
		t.Fatalf("late success counted: %v %v", ok, done)
	}
	q := NewProbe(1, 9, now)
	q.Version("ghostty 1.3.1")
	q.Graphics(9, "OK")
	q.Expire()
	if ok, done := q.Result(now, true); ok || !done {
		t.Fatalf("expired probe succeeded: %v %v", ok, done)
	}
}
