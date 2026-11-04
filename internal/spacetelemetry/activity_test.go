package spacetelemetry

import (
	"fmt"
	"testing"
)

// loadRun feeds one box's samples at the given VM uptimes and reports the
// activity each shows. A nil sample leaves the box out of that sample.
func loadRun(t *testing.T, uptimes []float64, samples []*BoxSample) []Activity {
	t.Helper()
	if len(uptimes) != len(samples) {
		t.Fatal("fixture: one uptime per sample")
	}
	tr := NewActivityTracker()
	var out []Activity
	for i, b := range samples {
		s := &Sample{Uptime: uptimes[i], Boxes: map[string]BoxSample{}}
		if b != nil {
			s.Boxes["b"] = *b
		}
		tr.Observe(s)
		a, _ := tr.State("b")
		out = append(out, a)
	}
	return out
}

// busy returns n cumulative samples of a box using cores of CPU and
// diskRate bytes per second, one second apart, with an idle session.
func busyRun(n int, cores, diskRate float64) ([]float64, []*BoxSample) {
	var up []float64
	var out []*BoxSample
	for i := range n {
		up = append(up, float64(i))
		out = append(out, &BoxSample{CPUUsec: uint64(float64(i) * cores * 1e6), IOWrite: uint64(float64(i) * diskRate),
			PTY: &PTYCounters{Session: "s"}})
	}
	return up, out
}

const (
	unknownA = ActivityUnknown
	idleA    = ActivityIdle
	activeA  = ActivityActive
)

func same(got, want []Activity) bool { return fmt.Sprint(got) == fmt.Sprint(want) }

// Sustained CPU or disk use is activity once it covers ActivitySustainTime
// of the VM's uptime, and is held like output; a shorter burst never is.
// Network traffic never counts, however heavy.
func TestSustainedLoadIsActivityAndSpikesAreNot(t *testing.T) {
	up, cpu := busyRun(7, 0.5, 0)
	if got, want := loadRun(t, up, cpu), []Activity{unknownA, idleA, idleA, activeA, activeA, activeA, activeA}; !same(got, want) {
		t.Fatalf("sustained CPU: %v, want %v", got, want)
	}
	up, disk := busyRun(5, 0, 2<<20)
	if got, want := loadRun(t, up, disk), []Activity{unknownA, idleA, idleA, activeA, activeA}; !same(got, want) {
		t.Fatalf("sustained disk: %v, want %v", got, want)
	}
	// Three busy seconds, then quiet: held for one more sample, as output is.
	up, held := busyRun(4, 1, 0)
	last := *held[3]
	for i := 4; i < 8; i++ {
		up = append(up, float64(i))
		quiet := last
		held = append(held, &quiet)
	}
	if got, want := loadRun(t, up, held), []Activity{unknownA, idleA, idleA, activeA, activeA, idleA, idleA, idleA}; !same(got, want) {
		t.Fatalf("hold after load: %v, want %v", got, want)
	}
	// Two busy seconds, then quiet: a spike.
	up, spike := busyRun(3, 2, 0)
	for i := 3; i < 6; i++ {
		up = append(up, float64(i))
		quiet := *spike[2]
		spike = append(spike, &quiet)
	}
	if got := loadRun(t, up, spike); !same(got, []Activity{unknownA, idleA, idleA, idleA, idleA, idleA}) {
		t.Fatalf("spike: %v", got)
	}
	// Below both thresholds: idle however long it lasts.
	up, low := busyRun(8, ActivityCPUCores/2, ActivityIOBytes/2)
	for i, a := range loadRun(t, up, low) {
		if i > 0 && a != idleA {
			t.Fatalf("below thresholds at %d: %v", i, a)
		}
	}
	// Heavy network only.
	up, netOnly := busyRun(6, 0, 0)
	for i, b := range netOnly {
		b.NetRx, b.NetTx = uint64(i)<<30, uint64(i)<<30
	}
	for i, a := range loadRun(t, up, netOnly) {
		if i > 0 && a != idleA {
			t.Fatalf("network counted at %d: %v", i, a)
		}
	}
}

// Continuity is measured in VM uptime: rapid samples need the same three
// seconds, a gap over ActivityMaxGap or a missing sample starts over, and so
// do a new container, a counter reset and an unreadable cgroup.
func TestSustainedLoadNeedsContinuousEvidence(t *testing.T) {
	// Quarter-second samples: active only after twelve busy pairs.
	var up []float64
	var fast []*BoxSample
	for i := range 14 {
		up = append(up, float64(i)/4)
		fast = append(fast, &BoxSample{CPUUsec: uint64(i) * 250_000, PTY: &PTYCounters{Session: "s"}})
	}
	got := loadRun(t, up, fast)
	for i, a := range got {
		want := idleA
		switch {
		case i == 0:
			want = unknownA
		case i >= 12:
			want = activeA
		}
		if a != want {
			t.Fatalf("rapid polling at %d (%.2fs): %v, want %v (%v)", i, up[i], a, want, got)
		}
	}
	busyAt := func(cpu uint64) *BoxSample { return &BoxSample{CPUUsec: cpu, PTY: &PTYCounters{Session: "s"}} }
	for _, c := range []struct {
		name    string
		uptimes []float64
		samples []*BoxSample
		want    []Activity
	}{
		{"a long gap starts over",
			[]float64{0, 1, 2, 7, 8, 9, 10},
			[]*BoxSample{busyAt(0), busyAt(1e6), busyAt(2e6), busyAt(7e6), busyAt(8e6), busyAt(9e6), busyAt(10e6)},
			[]Activity{unknownA, idleA, idleA, idleA, idleA, idleA, activeA}},
		{"a missing sample starts over",
			[]float64{0, 1, 2, 3, 4, 5, 6},
			[]*BoxSample{busyAt(0), busyAt(1e6), busyAt(2e6), nil, busyAt(4e6), busyAt(5e6), busyAt(6e6)},
			[]Activity{unknownA, idleA, idleA, unknownA, unknownA, idleA, idleA}},
		{"a counter reset starts over",
			[]float64{0, 1, 2, 3, 4, 5, 6},
			[]*BoxSample{busyAt(5e6), busyAt(6e6), busyAt(7e6), busyAt(0), busyAt(1e6), busyAt(2e6), busyAt(3e6)},
			[]Activity{unknownA, idleA, idleA, idleA, idleA, idleA, activeA}},
		{"an unreadable cgroup starts over and drops the hold, but the session stays idle",
			[]float64{0, 1, 2, 3, 4, 5},
			[]*BoxSample{busyAt(0), busyAt(1e6), busyAt(2e6), busyAt(3e6), {Error: "cgroup gone", PTY: &PTYCounters{Session: "s"}}, busyAt(5e6)},
			[]Activity{unknownA, idleA, idleA, activeA, idleA, idleA}},
	} {
		if got := loadRun(t, c.uptimes, c.samples); !same(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	container := []*BoxSample{
		{Container: "a", CPUUsec: 0}, {Container: "a", CPUUsec: 1e6}, {Container: "a", CPUUsec: 2e6},
		{Container: "b", CPUUsec: 3e6}, {Container: "b", CPUUsec: 4e6}, {Container: "b", CPUUsec: 5e6},
	}
	// The new container is also a new session baseline: unknown once.
	if got := loadRun(t, []float64{0, 1, 2, 3, 4, 5}, container); !same(got, []Activity{unknownA, idleA, idleA, unknownA, idleA, idleA}) {
		t.Errorf("a new container starts over: %v", got)
	}
}

// Load only adds activity. A box whose output counter cannot be read is
// active under sustained load and unknown otherwise; a box without a
// session is active under sustained load; an unread disk counter leaves CPU
// counting.
func TestSustainedLoadOnlyAddsActivity(t *testing.T) {
	up := []float64{0, 1, 2, 3}
	unread := func(cpu uint64) *BoxSample { return &BoxSample{CPUUsec: cpu, PTYError: "session stats unavailable"} }
	if got := loadRun(t, up, []*BoxSample{unread(0), unread(1e6), unread(2e6), unread(3e6)}); !same(got, []Activity{unknownA, unknownA, unknownA, activeA}) {
		t.Errorf("unread output under load: %v", got)
	}
	if got := loadRun(t, up, []*BoxSample{unread(0), unread(0), unread(0), unread(0)}); !same(got, []Activity{unknownA, unknownA, unknownA, unknownA}) {
		t.Errorf("unread output without load: %v", got)
	}
	noSession := func(cpu uint64) *BoxSample { return &BoxSample{CPUUsec: cpu} }
	if got := loadRun(t, up, []*BoxSample{noSession(0), noSession(1e6), noSession(2e6), noSession(3e6)}); !same(got, []Activity{unknownA, idleA, idleA, activeA}) {
		t.Errorf("no session under load: %v", got)
	}
	ioUnread := func(cpu, disk uint64) *BoxSample {
		return &BoxSample{CPUUsec: cpu, IOWrite: disk, Unavailable: []string{SignalIO}, PTY: &PTYCounters{Session: "s"}}
	}
	if got := loadRun(t, up, []*BoxSample{ioUnread(0, 0), ioUnread(0, 9<<20), ioUnread(0, 18<<20), ioUnread(0, 27<<20)}); !same(got, []Activity{unknownA, idleA, idleA, idleA}) {
		t.Errorf("unread disk counted: %v", got)
	}
	if got := loadRun(t, up, []*BoxSample{ioUnread(0, 0), ioUnread(1e6, 0), ioUnread(2e6, 0), ioUnread(3e6, 0)}); !same(got, []Activity{unknownA, idleA, idleA, activeA}) {
		t.Errorf("CPU with unread disk: %v", got)
	}
}
