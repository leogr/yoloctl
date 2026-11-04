package spaceview

import (
	"fmt"
	"slices"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// historyLen keeps two minutes at the default one-second interval.
const historyLen = 120

// boxLive is the host's rate view of one box, derived from two samples.
type boxLive struct {
	CPU      float64 // cores in use
	CPULimit float64 // cores; 0 means no per-box cap
	Pids     uint64
	NetRx    float64 // bytes per second
	NetTx    float64
	IORead   float64
	IOWrite  float64
	Hist     []float64 // CPU as a fraction of the box's ceiling
	Err      string
	Rated    bool // at least two samples arrived
}

// activity is what the samples show a running box doing; the shared
// tracker classifies it so every view agrees.
type activity = spacetelemetry.Activity

const (
	activityUnknown = spacetelemetry.ActivityUnknown
	activityIdle    = spacetelemetry.ActivityIdle
	activityActive  = spacetelemetry.ActivityActive
)

func newBoxLive() *boxLive { return &boxLive{} }

func readBoth(cur, prev spacetelemetry.BoxSample, signal string) bool {
	return spacetelemetry.ReadBoth(cur, prev, signal)
}

// data accumulates frames. It never invents values: a box without two
// samples has no rates, and an inventory error stays an error.
type data struct {
	inv    *spacetelemetry.Inventory
	invAt  time.Time
	last   *spacetelemetry.Sample
	lastAt time.Time
	prev   *spacetelemetry.Sample
	prevAt time.Time

	boxes   map[string]*boxLive
	act     *spacetelemetry.ActivityTracker
	vmBusy  float64 // fraction of all CPUs
	vmHist  []float64
	poolCPU float64 // cores
	rated   bool

	accents    map[string]int
	nextAccent int
	// diskToasted is the worst level each disk has toasted since it was
	// last fine.
	diskToasted map[string]spacetelemetry.DiskLevel
	rebase      bool // the next sample starts a new baseline
}

func newData() *data {
	return &data{boxes: map[string]*boxLive{}, act: spacetelemetry.NewActivityTracker(), accents: map[string]int{},
		diskToasted: map[string]spacetelemetry.DiskLevel{}}
}

// event is a state transition worth a toast.
type event struct {
	box  string
	text string
	bad  bool
}

func (d *data) applyInventory(at time.Time, inv *spacetelemetry.Inventory) []event {
	var events []event
	old := map[string]spacetelemetry.Box{}
	if d.inv != nil {
		for _, b := range d.inv.Boxes {
			old[b.Name] = b
		}
	}
	names := make([]string, 0, len(inv.Boxes))
	for _, b := range inv.Boxes {
		names = append(names, b.Name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, ok := d.accents[name]; !ok {
			d.accents[name] = d.nextAccent
			d.nextAccent++
		}
	}
	if d.inv != nil && inv.BoxesError == "" && d.inv.BoxesError == "" {
		seen := map[string]bool{}
		for _, b := range inv.Boxes {
			seen[b.Name] = true
			prev, ok := old[b.Name]
			if !ok {
				events = append(events, event{box: b.Name, text: "appeared"})
				continue
			}
			events = append(events, transitions(prev, b)...)
		}
		for name := range old {
			if !seen[name] {
				events = append(events, event{box: name, text: "was removed"})
			}
		}
	}
	for name := range d.boxes {
		if !slices.Contains(names, name) {
			delete(d.boxes, name)
		}
	}
	d.inv, d.invAt = inv, at
	return events
}

func transitions(prev, cur spacetelemetry.Box) []event {
	var out []event
	switch {
	case prev.Runtime != "running" && cur.Runtime == "running":
		out = append(out, event{box: cur.Name, text: "started"})
	case prev.Runtime == "running" && cur.Runtime == "stopped":
		out = append(out, event{box: cur.Name, text: "stopped", bad: true})
	}
	if prev.Session != cur.Session {
		switch cur.Session {
		case "exited":
			code := "?"
			if cur.ExitCode != nil {
				code = fmt.Sprint(*cur.ExitCode)
			}
			out = append(out, event{box: cur.Name, text: "session exited (" + code + ")", bad: cur.ExitCode == nil || *cur.ExitCode != 0})
		case "lost":
			out = append(out, event{box: cur.Name, text: "session lost", bad: true})
		case "running":
			if prev.Session == "exited" || prev.Session == "none" {
				out = append(out, event{box: cur.Name, text: "session started"})
			}
		}
	}
	return out
}

// rebaseline makes the next sample a fresh baseline after the stream was
// lost or paused: activity observed before the gap is no longer current. The
// last CPU values stay on screen; memory needs a current sample.
func (d *data) rebaseline() {
	d.rebase = true
	d.act.Rebaseline()
}

func (d *data) applySample(at time.Time, s *spacetelemetry.Sample) []event {
	var events []event
	if d.last != nil && d.last.Pool != nil && s.Pool != nil && s.Pool.OOMKills > d.last.Pool.OOMKills {
		// The pool's counter can include kills under a box's own limit and
		// does not say which limit was reached.
		events = append(events, event{text: fmt.Sprintf("OOM kill in a box or build (%d in the workload pool since it was created)", s.Pool.OOMKills), bad: true})
	}
	events = append(events, d.diskEvents(s)...)
	d.prev, d.prevAt = d.last, d.lastAt
	d.last, d.lastAt = s, at
	d.act.Observe(s)
	if d.prev == nil || d.rebase {
		d.rebase = false
		d.updateCounters(0)
		return events
	}
	dt := at.Sub(d.prevAt).Seconds()
	if dt <= 0 {
		return events
	}
	d.updateCounters(dt)
	return events
}

func (d *data) cpus() int {
	if d.inv != nil && d.inv.CPUs > 0 {
		return d.inv.CPUs
	}
	return 1
}

// updateCounters refreshes instantaneous values and, with dt > 0, rates and
// history.
func (d *data) updateCounters(dt float64) {
	cur, prev := d.last, d.prev
	if dt > 0 {
		if total := float64(cur.CPU.Total) - float64(prev.CPU.Total); total > 0 {
			idle := float64(cur.CPU.Idle) - float64(prev.CPU.Idle)
			d.vmBusy = clamp01(1 - idle/total)
			d.vmHist = appendHist(d.vmHist, d.vmBusy)
			d.rated = true
		}
		if cur.Pool != nil && prev.Pool != nil && cur.Pool.CPUUsec >= prev.Pool.CPUUsec {
			d.poolCPU = float64(cur.Pool.CPUUsec-prev.Pool.CPUUsec) / 1e6 / dt
		}
	}
	for name, s := range cur.Boxes {
		b := d.boxes[name]
		if b == nil {
			b = newBoxLive()
			d.boxes[name] = b
		}
		b.Err = s.Error
		if s.Error != "" {
			continue
		}
		b.CPULimit, b.Pids = s.CPULimit, s.Pids
		if dt <= 0 || prev == nil {
			continue
		}
		p, ok := prev.Boxes[name]
		if !ok || p.Error != "" {
			continue
		}
		if p.Container != s.Container {
			// A replaced container's counters restart; never rate across them.
			b.CPU, b.NetRx, b.NetTx, b.IORead, b.IOWrite, b.Rated = 0, 0, 0, 0, 0, false
			continue
		}
		b.CPU = rate(s.CPUUsec, p.CPUUsec, dt) / 1e6
		// An unread counter is zero, not traffic: no rate from or to it.
		b.NetRx, b.NetTx, b.IORead, b.IOWrite = 0, 0, 0, 0
		if readBoth(s, p, spacetelemetry.SignalNet) {
			b.NetRx, b.NetTx = rate(s.NetRx, p.NetRx, dt), rate(s.NetTx, p.NetTx, dt)
		}
		if readBoth(s, p, spacetelemetry.SignalIO) {
			b.IORead, b.IOWrite = rate(s.IORead, p.IORead, dt), rate(s.IOWrite, p.IOWrite, dt)
		}
		ceiling := b.CPULimit
		if ceiling <= 0 {
			ceiling = float64(d.cpus())
		}
		b.Hist = appendHist(b.Hist, b.CPU/ceiling)
		b.Rated = true
	}
	for name, b := range d.boxes {
		if _, ok := cur.Boxes[name]; !ok {
			// Not sampled any more: stopped or vanished. Keep history visible
			// but drop live rates.
			b.CPU, b.NetRx, b.NetTx, b.IORead, b.IOWrite, b.Rated = 0, 0, 0, 0, 0, false
		}
	}
}

// rate tolerates counter resets (a restarted container) by reporting zero.
func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / dt
}

func appendHist(h []float64, v float64) []float64 {
	h = append(h, v)
	if len(h) > historyLen {
		h = h[len(h)-historyLen:]
	}
	return h
}
