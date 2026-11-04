package spaceview

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// activityRun feeds one running box's samples and reports what each shows.
func activityRun(t *testing.T, samples ...spacetelemetry.BoxSample) []activity {
	t.Helper()
	d := newData()
	d.applyInventory(fixtureStart, &spacetelemetry.Inventory{Boxes: []spacetelemetry.Box{{Name: "b", State: "running", Runtime: "running", Session: "running"}}})
	var out []activity
	for i, s := range samples {
		d.applySample(fixtureStart.Add(time.Duration(i+1)*time.Second), &spacetelemetry.Sample{Uptime: float64(i + 1), Boxes: map[string]spacetelemetry.BoxSample{"b": s}})
		a, _ := d.act.State("b")
		out = append(out, a)
	}
	return out
}

func pty(session string, output, input uint64) *spacetelemetry.PTYCounters {
	return &spacetelemetry.PTYCounters{Session: session, OutputBytes: output, InputPackets: input, InputBytes: input}
}

const (
	unknown = activityUnknown
	idle    = activityIdle
	active  = activityActive
)

// Terminal output is activity; input, network and brief CPU or disk load are
// not. Sustained load is covered by the tracker's own tests.
func TestActivityComesFromOutputNotBriefLoad(t *testing.T) {
	for _, c := range []struct {
		name    string
		samples []spacetelemetry.BoxSample
		want    []activity
	}{
		{"two seconds of CPU alone are idle", []spacetelemetry.BoxSample{
			{CPUUsec: 0, PTY: pty("s", 10, 1)}, {CPUUsec: 2e6, PTY: pty("s", 10, 1)}, {CPUUsec: 4e6, PTY: pty("s", 10, 1)}},
			[]activity{unknown, idle, idle}},
		{"pty output, held for one more sample", []spacetelemetry.BoxSample{
			{PTY: pty("s", 10, 0)}, {PTY: pty("s", 11, 0)}, {PTY: pty("s", 11, 0)}, {PTY: pty("s", 11, 0)}},
			[]activity{unknown, active, active, idle}},
		{"accepted keystrokes alone are idle", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTY: pty("s", 0, 0)}, {PTY: pty("s", 0, 1)}, {PTY: pty("s", 0, 9)}},
			[]activity{unknown, idle, idle, idle}},
		{"network and disk bytes alone are idle", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {NetRx: 1, PTY: pty("s", 0, 0)}, {NetRx: 1, NetTx: 5, PTY: pty("s", 0, 0)}, {NetRx: 1, IORead: 3, IOWrite: 1, PTY: pty("s", 0, 0)}},
			[]activity{unknown, idle, idle, idle}},
		{"input, network and disk together are idle", []spacetelemetry.BoxSample{
			{PTY: pty("s", 7, 0)}, {NetRx: 9, NetTx: 9, IORead: 9, IOWrite: 9, PTY: pty("s", 7, 4)}},
			[]activity{unknown, idle}},
		{"output after other movement is active", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {NetRx: 1, PTY: pty("s", 0, 1)}, {NetRx: 2, PTY: pty("s", 1, 1)}},
			[]activity{unknown, idle, active}},
		{"a new holder session restarts the baseline", []spacetelemetry.BoxSample{
			{PTY: pty("s", 50, 5)}, {PTY: pty("s", 50, 5)}, {PTY: pty("t", 900, 9)}, {PTY: pty("t", 900, 9)}},
			[]activity{unknown, idle, unknown, idle}},
		{"a new holder drops the previous hold", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTY: pty("s", 1, 0)}, {PTY: pty("t", 0, 0)}, {PTY: pty("t", 0, 0)}},
			[]activity{unknown, active, unknown, idle}},
		{"a replacement container with larger output", []spacetelemetry.BoxSample{
			{Container: "a", PTY: pty("s", 100, 0)}, {Container: "a", PTY: pty("s", 100, 0)}, {Container: "b", PTY: pty("s", 900, 0)}, {Container: "b", PTY: pty("s", 900, 0)}},
			[]activity{unknown, idle, unknown, idle}},
		{"a replaced container drops the hold even when unreadable", []spacetelemetry.BoxSample{
			{Container: "a", PTY: pty("s", 0, 0)}, {Container: "a", PTY: pty("s", 1, 0)}, {Container: "b", Error: "cgroup gone", PTYError: "stats unavailable"}},
			[]activity{unknown, active, unknown}},
		{"an unreadable output counter is unknown, not idle", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTYError: "session stats unavailable"}, {PTYError: "session stats unavailable"}, {NetTx: 9, IOWrite: 9, PTYError: "session stats unavailable"}},
			[]activity{unknown, unknown, unknown, unknown}},
		{"an unread counter drops the hold at once", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTY: pty("s", 1, 0)}, {PTYError: "session stats unavailable"}, {PTY: pty("s", 1, 0)}},
			[]activity{unknown, active, unknown, unknown}},
		{"an unknown session state drops the hold as unknown", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTY: pty("s", 1, 0)}, {PTYError: "session state unknown"}},
			[]activity{unknown, active, unknown}},
		{"an ended session drops the hold at once", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTY: pty("s", 1, 0)}, {}, {}},
			[]activity{unknown, active, idle, idle}},
		{"a read after a failed one is a fresh baseline", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {PTYError: "session stats unavailable"}, {PTY: pty("s", 50, 0)}, {PTY: pty("s", 50, 0)}},
			[]activity{unknown, unknown, unknown, idle}},
		{"unread network and disk counters do not matter", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0), Unavailable: []string{"io", "net"}}, {PTY: pty("s", 0, 0), Unavailable: []string{"io", "net"}}, {PTY: pty("s", 1, 0), Unavailable: []string{"io", "net"}}},
			[]activity{unknown, idle, active}},
		{"cgroup metric errors do not matter", []spacetelemetry.BoxSample{
			{PTY: pty("s", 0, 0)}, {Error: "box cgroup gone", PTY: pty("s", 0, 0)}, {Error: "box cgroup gone", PTY: pty("s", 3, 0)}},
			[]activity{unknown, idle, active}},
		{"no session: nothing to observe", []spacetelemetry.BoxSample{
			{}, {}, {IORead: 4, NetRx: 4}},
			[]activity{unknown, idle, idle}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := activityRun(t, c.samples...); !equalActivities(got, c.want) {
				t.Fatalf("activity = %v, want %v", got, c.want)
			}
		})
	}
}

// A box that stops is no longer sampled: its hold is dropped at once and
// its activity is unknown, never a lingering pulse.
func TestStoppedBoxDropsTheHold(t *testing.T) {
	tr := spacetelemetry.NewActivityTracker()
	for _, n := range []uint64{0, 1} {
		tr.Observe(&spacetelemetry.Sample{Boxes: map[string]spacetelemetry.BoxSample{"b": {PTY: pty("s", n, 0)}}})
	}
	if a, _ := tr.State("b"); a != active {
		t.Fatalf("output = %v, want active", a)
	}
	tr.Observe(&spacetelemetry.Sample{Boxes: map[string]spacetelemetry.BoxSample{}})
	if a, why := tr.State("b"); a != unknown || why != "not sampled" {
		t.Fatalf("stopped box = %v %q, want unknown", a, why)
	}
}

// setActivity drives the shared tracker into one state for a box, through
// the same samples the stream would deliver.
func setActivity(m *Model, name string, a activity, why string) {
	m.d.act = spacetelemetry.NewActivityTracker()
	base := spacetelemetry.BoxSample{PTY: pty("s", 0, 0)}
	next := base
	switch a {
	case active:
		next.PTY = pty("s", 1, 0)
	case unknown:
		base, next = spacetelemetry.BoxSample{PTYError: why}, spacetelemetry.BoxSample{PTYError: why}
	}
	m.d.act.Observe(&spacetelemetry.Sample{Boxes: map[string]spacetelemetry.BoxSample{name: base}})
	m.d.act.Observe(&spacetelemetry.Sample{Boxes: map[string]spacetelemetry.BoxSample{name: next}})
}

func equalActivities(a, b []activity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Only the dot reflects activity: an active box pulses, an idle one is
// steady, and unproven activity is dimmed. The label is the box state.
func TestRunningDotShowsObservedActivity(t *testing.T) {
	m := fixtureModel(t, 160, 50)
	box, _ := m.box("api-refactor")
	dot := func() (Color, string) {
		b := &builder{m: m, t: m.theme, targets: []target{{}}, index: map[string]int{}}
		pill := b.statePill(box, 0)
		return pill[0].style.FG, pill[1].text
	}
	setActivity(m, "api-refactor", idle, "")
	m.motion = true
	start := m.now
	frames := int(PulseCycle / PulseFrame)
	seen := map[Color]bool{}
	for step := range frames {
		m.now = start.Add(time.Duration(step) * PulseFrame)
		c, label := dot()
		seen[c] = true
		if label != " RUNNING" {
			t.Fatalf("label = %q", label)
		}
	}
	if len(seen) != 1 || !seen[m.theme.OK] {
		t.Fatalf("idle dot changed or was not OK: %v", seen)
	}

	setActivity(m, "api-refactor", active, "")
	clear(seen)
	for step := range frames {
		m.now = start.Add(time.Duration(step) * PulseFrame)
		c, _ := dot()
		if want := m.theme.ActiveTone(m.theme.OK, true, m.now); c != want {
			t.Fatalf("step %d: active dot %v, want shared tone %v", step, c, want)
		}
		seen[c] = true
	}
	if len(seen) < 4 {
		t.Fatal("active dot does not breathe")
	}
	m.motion = false
	if c, _ := dot(); c != m.theme.ActiveTone(m.theme.OK, false, m.now) {
		t.Fatalf("motion off: active dot %v", c)
	}

	setActivity(m, "api-refactor", unknown, "session stats unavailable")
	if c, _ := dot(); c != m.theme.Dim {
		t.Fatalf("unknown activity drawn as %v, want dim", c)
	}
	m.hover = target{tBox, "api-refactor"}.key()
	m.View()
	m.lastTargets = m.last.targets
	m.View()
	if footer := strings.Split(m.last.c.plain(), "\n")[m.h-1]; !strings.Contains(footer, "activity unknown (session stats unavailable)") {
		t.Fatalf("footer does not explain unknown activity: %q", footer)
	}
}

// Evidence is current only while samples arrive: a lost, paused or stalled
// stream makes activity unknown, and after a gap a fresh baseline is needed.
func TestActivityNeedsALiveFreshStream(t *testing.T) {
	m := fixtureModel(t, 160, 50)
	if a, _ := m.activity("api-refactor"); a != activityActive {
		t.Fatalf("fixture box not active: %v", a)
	}
	m.Update(lostMsg{err: errors.New("stream closed")})
	if a, why := m.activity("api-refactor"); a != activityUnknown || why != "telemetry not live" {
		t.Fatalf("lost stream: %v %q", a, why)
	}
	sample := func(step int) {
		m.Update(frameMsg(spacetelemetry.Frame{Kind: spacetelemetry.KindSample, At: fixtureStart.Add(time.Duration(step) * time.Second), Sample: fixtureSample(step)}))
	}
	sample(41)
	if a, why := m.activity("api-refactor"); a != activityUnknown || why != "waiting for fresh samples" {
		t.Fatalf("first sample after reconnect compared across the gap: %v %q", a, why)
	}
	sample(42)
	if a, _ := m.activity("api-refactor"); a != activityActive {
		t.Fatalf("fresh pair: %v", a)
	}
	m.now = m.sampleSeen.Add(10 * time.Second)
	if a, why := m.activity("api-refactor"); a != activityUnknown || why != "no recent sample" {
		t.Fatalf("stalled stream: %v %q", a, why)
	}
	// A stream that stalls while connected and then resumes starts a fresh
	// baseline too, rather than rating across the gap.
	m.sampleSeen = time.Now().Add(-time.Minute)
	sample(43)
	if a, why := m.activity("api-refactor"); a != activityUnknown || why != "waiting for fresh samples" {
		t.Fatalf("resumed stalled stream compared across the gap: %v %q", a, why)
	}
	sample(44)
	if a, _ := m.activity("api-refactor"); a != activityActive {
		t.Fatalf("fresh pair after a stall: %v", a)
	}
	m.now = m.sampleSeen
	m.paused = true
	if a, why := m.activity("api-refactor"); a != activityUnknown || why != "updates paused" {
		t.Fatalf("paused: %v %q", a, why)
	}
}

// hue is a color's HSV hue in degrees.
func hue(r, g, b uint8) float64 {
	rf, gf, bf := float64(r), float64(g), float64(b)
	hi, lo := max(rf, gf, bf), min(rf, gf, bf)
	if hi == lo {
		return 0
	}
	var h float64
	switch hi {
	case rf:
		h = math.Mod((gf-bf)/(hi-lo), 6)
	case gf:
		h = (bf-rf)/(hi-lo) + 2
	default:
		h = (rf-gf)/(hi-lo) + 4
	}
	return math.Mod(h*60+360, 360)
}

// The one activity breath every view draws: a two-second cycle that starts
// and ends at the dot's own color, brightens smoothly to a visible peak
// halfway and back, never leaves the dot's hue or drops below its color,
// and holds the peak with motion off. Green and yellow, in both themes.
// The peak was chosen from rendered swatches: 0.35 read as too soft.
func TestActiveToneBreathesGently(t *testing.T) {
	if PulseCycle != 2*time.Second || PulseFrame != 160*time.Millisecond || pulseLift != 0.55 {
		t.Fatalf("breath changed: cycle %v frame %v lift %v", PulseCycle, PulseFrame, pulseLift)
	}
	start := time.UnixMilli(1_800_000_000_000) // a cycle boundary
	for _, dark := range []bool{true, false} {
		th := newTheme(dark, nil, nil)
		for _, base := range []Color{th.OK, th.Warn} {
			br, bgc, bb, ok := base.rgb()
			if !ok {
				t.Fatalf("base %v has no known value", base)
			}
			if c := th.ActiveTone(base, true, start); c != base {
				t.Fatalf("cycle start %v, want the dot's own %v", c, base)
			}
			peak := th.ActiveTone(base, true, start.Add(PulseCycle/2))
			if peak != lift(base, pulseLift) || peak != th.ActiveTone(base, false, start.Add(time.Second/3)) {
				t.Fatalf("peak %v, steady %v", peak, th.ActiveTone(base, false, start))
			}
			pr, pg, pb, _ := peak.rgb()
			for _, ch := range [][2]uint8{{pr, br}, {pg, bgc}, {pb, bb}} {
				if float64(ch[0]-ch[1]) < pulseLift*float64(255-ch[1])-1 {
					t.Fatalf("dark=%v base %v: peak %v does not reach the visible lift", dark, base, peak)
				}
			}
			if th.ActiveTone(base, true, start.Add(PulseCycle+300*time.Millisecond)) != th.ActiveTone(base, true, start.Add(300*time.Millisecond)) {
				t.Fatal("the breath does not repeat every cycle")
			}
			prev := -1
			for ms := 0; ms <= int(PulseCycle.Milliseconds()); ms += 40 {
				c := th.ActiveTone(base, true, start.Add(time.Duration(ms)*time.Millisecond))
				r, g, b, _ := c.rgb()
				if d := math.Abs(hue(r, g, b) - hue(br, bgc, bb)); d > 2 && d < 358 {
					t.Fatalf("dark=%v base %v at %dms: hue moved %.1f°", dark, base, ms, d)
				}
				for _, ch := range [][2]uint8{{r, br}, {g, bgc}, {b, bb}} {
					if ch[0] < ch[1] || float64(ch[0]-ch[1]) > pulseLift*float64(255-ch[1])+1 {
						t.Fatalf("dark=%v base %v at %dms: %v outside the gentle range", dark, base, ms, c)
					}
				}
				sum := int(r) + int(g) + int(b)
				rising := ms <= int(PulseCycle.Milliseconds())/2
				if prev >= 0 && ((rising && sum < prev) || (!rising && sum > prev)) {
					t.Fatalf("dark=%v base %v at %dms: the breath is not smooth", dark, base, ms)
				}
				prev = sum
			}
		}
	}
}
