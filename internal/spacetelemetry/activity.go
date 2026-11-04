package spacetelemetry

import (
	"slices"
	"time"
)

// Activity is what consecutive samples show a running box doing. Every view
// that shows box activity classifies it here, so the space view and the chat
// participants panel agree.
type Activity int

const (
	ActivityUnknown Activity = iota // evidence missing: never shown as idle
	ActivityIdle
	ActivityActive
)

// ActivityHold keeps a box active for this many sample pairs after the last
// output, or the last sustained resource use, so a dot does not flicker
// between samples. It is a time window, not a byte threshold: any output
// counts.
const ActivityHold = 2

// Sustained resource use is activity too: a box whose cgroup keeps working
// without terminal output (a build, a test run, a box exec) is busy. A sample
// pair is busy when the box averages at least ActivityCPUCores of CPU or
// ActivityIOBytes per second of disk reads and writes over it; network never
// counts. Busy pairs make a box active only once they cover
// ActivitySustainTime of the VM's uptime without a break, so a spike never
// does, however fast the samples come. A pair longer than ActivityMaxGap is
// no continuous evidence and starts over. These are provisional defaults,
// not values calibrated on live boxes.
const (
	ActivityCPUCores    = 0.25
	ActivityIOBytes     = 1 << 20
	ActivitySustainTime = 3 * time.Second
	ActivityMaxGap      = 2 * DefaultInterval
)

// DefaultInterval is the sampling interval every view of box activity
// uses, so the hold above lasts as long in each.
const DefaultInterval = time.Second

// StaleAfter is how long the last sample remains current evidence at a
// sampling interval.
func StaleAfter(interval time.Duration) time.Duration { return max(3*interval, 2*time.Second) }

// ActivityCurrent is the shared freshness gate for activity evidence: only
// a live, unpaused stream whose last sample arrived within StaleAfter (on
// the viewer's own clock) is current. Otherwise it says why not.
func ActivityCurrent(live, paused bool, lastSample, now time.Time, interval time.Duration) (bool, string) {
	switch {
	case !live:
		return false, "telemetry not live"
	case paused:
		return false, "updates paused"
	case now.Sub(lastSample) > StaleAfter(interval):
		return false, "no recent sample"
	}
	return true, ""
}

// boxActivity is one box's activity evidence from its last two samples.
type boxActivity struct {
	quiet    int     // consecutive sample pairs without output
	busy     float64 // seconds of continuous busy pairs (CPU or disk over its threshold)
	calm     int     // sample pairs since resource use was last sustained
	complete bool    // the output counter, if any, was read in both samples
	unknown  string  // why the evidence is incomplete
}

func (b *boxActivity) state() (Activity, string) {
	switch {
	case b.quiet < ActivityHold || b.calm < ActivityHold:
		return ActivityActive, ""
	case b.complete:
		return ActivityIdle, ""
	default:
		return ActivityUnknown, b.unknown
	}
}

func (b *boxActivity) reset(why string) {
	b.quiet, b.busy, b.calm, b.complete, b.unknown = ActivityHold, 0, ActivityHold, false, why
}

// observe compares two samples of one box, dt seconds apart. Terminal
// output is activity: any increase in the managed session's output bytes
// counts. Output is compared only within one holder session and one
// container. Its hold lasts only while output stays readable: a new holder
// or container, an unread output counter or an ended session drops it at
// once. An unread counter is unknown, never idle, and the next read is a
// fresh baseline. Accepted input and network never count. CPU and disk
// count only when sustained (see ActivitySustainTime), and only add
// activity: they never make a box idle or unknown.
func (b *boxActivity) observe(cur, prev BoxSample, dt float64) {
	b.observeResources(cur, prev, dt)
	moved, dropped := false, false
	b.complete, b.unknown = true, ""
	missing := func(why string) { b.complete, b.unknown = false, why }
	switch {
	case cur.Container != prev.Container:
		missing("new container")
		dropped = true
	case cur.PTY == nil && cur.PTYError == "":
		// No running session: there is no output to observe or hold.
		dropped = true
	case cur.PTY != nil && prev.PTY != nil && cur.PTY.Session == prev.PTY.Session:
		moved = cur.PTY.OutputBytes > prev.PTY.OutputBytes
	case cur.PTYError != "":
		missing(cur.PTYError)
		dropped = true
	default:
		missing("new session")
		dropped = true
	}
	switch {
	case moved:
		b.quiet = 0
	case dropped:
		b.quiet = ActivityHold // no current output stands behind a hold
	case b.quiet < ActivityHold:
		b.quiet++
	}
}

// observeResources accumulates continuous busy time and holds sustained
// use. No elapsed time, a gap over ActivityMaxGap, a new container, a
// decreasing CPU counter or a cgroup error is a fresh baseline that drops the
// hold at once, as for output. An unread or decreasing disk counter only
// removes disk from that pair: CPU still counts, and an existing hold runs
// its course.
func (b *boxActivity) observeResources(cur, prev BoxSample, dt float64) {
	busy, known := busyPair(cur, prev, dt)
	switch {
	case !known:
		b.busy, b.calm = 0, ActivityHold
		return
	case busy:
		b.busy += dt
	default:
		b.busy = 0
	}
	switch {
	case b.busy >= ActivitySustainTime.Seconds():
		b.calm = 0
	case b.calm < ActivityHold:
		b.calm++
	}
}

// busyPair reports whether a sample pair shows the box over its CPU or
// disk threshold, and whether its counters allow an answer at all.
func busyPair(cur, prev BoxSample, dt float64) (busy, known bool) {
	if cur.Container != prev.Container || cur.Error != "" || prev.Error != "" || dt <= 0 || dt > ActivityMaxGap.Seconds() || cur.CPUUsec < prev.CPUUsec {
		return false, false
	}
	if float64(cur.CPUUsec-prev.CPUUsec)/(dt*1e6) >= ActivityCPUCores {
		return true, true
	}
	if ReadBoth(cur, prev, SignalIO) && cur.IORead >= prev.IORead && cur.IOWrite >= prev.IOWrite {
		moved := (cur.IORead - prev.IORead) + (cur.IOWrite - prev.IOWrite)
		return float64(moved)/dt >= ActivityIOBytes, true
	}
	return false, true
}

// ReadBoth reports whether both samples read a byte counter.
func ReadBoth(cur, prev BoxSample, signal string) bool {
	return !slices.Contains(cur.Unavailable, signal) && !slices.Contains(prev.Unavailable, signal)
}

// ActivityTracker classifies every sampled box across one telemetry stream.
// It holds no connection state: a view reports a lost or paused stream with
// Rebaseline, and judges freshness with StaleAfter and its own clock.
type ActivityTracker struct {
	boxes  map[string]*boxActivity
	last   *Sample
	rebase bool
}

func NewActivityTracker() *ActivityTracker {
	return &ActivityTracker{boxes: map[string]*boxActivity{}}
}

// Observe applies the next sample. The first sample, and the first after a
// rebaseline, is only a baseline.
func (t *ActivityTracker) Observe(s *Sample) {
	prev := t.last
	t.last = s
	if t.rebase {
		prev, t.rebase = nil, false
	}
	for name, cur := range s.Boxes {
		b := t.boxes[name]
		if b == nil {
			b = &boxActivity{}
			b.reset("waiting for two samples")
			t.boxes[name] = b
		}
		if prev == nil {
			continue
		}
		if p, ok := prev.Boxes[name]; ok {
			// The VM's uptime is the samples' shared monotonic clock.
			b.observe(cur, p, s.Uptime-prev.Uptime)
		} else {
			b.complete, b.unknown = false, "waiting for two samples"
			b.busy, b.calm = 0, ActivityHold // a missing sample breaks continuity
		}
	}
	for name, b := range t.boxes {
		if _, ok := s.Boxes[name]; !ok {
			// Not sampled any more: stopped or vanished.
			b.reset("not sampled")
		}
	}
}

// Rebaseline makes the next sample a fresh baseline after the stream was
// lost, paused or stalled: activity observed before the gap is no longer
// current.
func (t *ActivityTracker) Rebaseline() {
	t.rebase = true
	for _, b := range t.boxes {
		b.reset("waiting for fresh samples")
	}
}

// State is a box's activity and, when unknown, why. It does not judge
// whether the stream is still live.
func (t *ActivityTracker) State(name string) (Activity, string) {
	b := t.boxes[name]
	if b == nil {
		return ActivityUnknown, "not sampled"
	}
	return b.state()
}
