package spaceview

import (
	"fmt"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// minMemBar is the narrowest card memory bar worth drawing before its label
// gives up words.
const minMemBar = 6

// boxMemory is one box's memory from the current sample alone. A charge is
// instantaneous: it needs no second sample, and it never outlives the sample,
// container or stream that read it.
type boxMemory struct {
	Current  uint64 // memory.current: the whole charge, file cache included
	Max      uint64 // memory.max; 0 means no per-box limit
	MaxKnown bool
	Stat     *spacetelemetry.MemStat
}

// working is the estimated working set, current − inactive_file from the same
// sample. It is a display convention, not non-reclaimable memory: active file
// pages and shmem stay in it. The files are read separately, so a larger
// inactive_file floors the estimate at zero.
func (bm boxMemory) working() (uint64, bool) {
	if bm.Stat == nil || bm.Stat.InactiveFile == nil {
		return 0, false
	}
	return bm.Current - min(bm.Current, *bm.Stat.InactiveFile), true
}

// memory returns a running box's memory from the current sample, or why
// there is no current reading. The sample must name the container the
// inventory observed: the stream publishes a new inventory before its next
// sample, so a replaced box's last sample still belongs to the old one.
func (m *Model) memory(box spacetelemetry.Box) (boxMemory, string) {
	if box.Runtime != "running" {
		return boxMemory{}, "container " + box.Runtime
	}
	if box.Container == "" {
		return boxMemory{}, "container unknown"
	}
	if ok, why := m.sampleCurrent(); !ok {
		return boxMemory{}, why
	}
	if m.d.last == nil {
		return boxMemory{}, "waiting for the first sample"
	}
	s, ok := m.d.last.Boxes[box.Name]
	switch {
	case !ok:
		return boxMemory{}, "not sampled"
	case s.Error != "":
		return boxMemory{}, "unavailable"
	case s.Container != box.Container:
		return boxMemory{}, "new container not sampled"
	}
	return boxMemory{Current: s.MemCurrent, Max: s.MemMax, MaxKnown: !slices.Contains(s.Unavailable, spacetelemetry.SignalMemMax), Stat: s.MemStat}, ""
}

// memScale is the bar's scale for a box and the words that name it: the
// box's own limit, or the pool or VM it shares. Without a known limit no
// scale is honest.
func (m *Model) memScale(bm boxMemory) (scale uint64, name string, ok bool) {
	s := m.d.last
	switch {
	case !bm.MaxKnown:
		return 0, "limit unknown", false
	case bm.Max > 0:
		return bm.Max, humanBytes(bm.Max) + " cap", true
	case s != nil && s.Pool != nil && s.Pool.Max > 0:
		return s.Pool.Max, humanBytes(s.Pool.Max) + " pool", true
	case s != nil && s.MemTotal > 0:
		return s.MemTotal, humanBytes(s.MemTotal) + " VM", true
	}
	return 0, "scale unknown", false
}

// memRow is a card's memory. The bar's extent is the whole charge; with a
// breakdown the estimated working set takes the box's accent and the inactive
// file pages it excludes are muted. It never uses heat colors: a full charge
// is not pressure, and neither is a full cap.
func (b *builder) memRow(box spacetelemetry.Box, inner int) line {
	t, m := b.t, b.m
	hit := b.hit(tBoxMem, box.Name)
	out := line{{text: "MEM ", style: fg(t.Label), hit: hit}}
	bm, why := m.memory(box)
	if why != "" {
		return append(out, seg{text: strings.Repeat("·", max(inner-5-textWidth(why), 1)), style: fg(t.Faint), hit: hit},
			seg{text: " " + why, style: fg(t.Dim), hit: hit})
	}
	bold := func(s string) seg { return seg{text: s, style: fg(t.Text).bold(), hit: hit} }
	dim := func(s string) seg { return seg{text: s, style: fg(t.Dim), hit: hit} }
	// Every label that comes with a scaled bar names its scale: a shared
	// box's bar is drawn against the pool, not a private allowance.
	total := humanBytes(bm.Current)
	scale, name, scaled := m.memScale(bm)
	long, short := line{bold(total), dim(" of " + name)}, line{bold(total), dim("/" + name)}
	if !scaled {
		long = line{bold(total), dim(" · " + name)}
		short = long
	}
	work, estimated := bm.working()
	var variants []line
	if estimated {
		est := bold("~" + humanBytes(work))
		variants = append(variants,
			append(line{est, dim(" working · ")}, long...),
			append(line{est, dim(" work · ")}, short...),
			append(line{est, dim(" · ")}, short...))
	}
	variants = append(variants, long, short, line{bold(total)})
	label := variants[len(variants)-1]
	for _, v := range variants {
		if inner-7-v.width() >= minMemBar {
			label = v
			break
		}
	}
	cells := max(inner-7-label.width(), 1)
	switch {
	case !scaled:
		out = append(out, seg{text: strings.Repeat("·", cells+2), style: fg(t.Faint), hit: hit})
	case estimated:
		out = append(out, stacked(t, []part{{value: float64(work), color: m.accent(box.Name)}, {value: float64(bm.Current - work), color: t.Dim}}, float64(scale), cells, hit)...)
	default:
		out = append(out, stacked(t, []part{{value: float64(bm.Current), color: t.Label}}, float64(scale), cells, hit)...)
	}
	return append(append(out, seg{text: " ", hit: hit}), label...)
}

func (m *Model) openBoxMemory(name string) {
	box, _ := m.box(name)
	m.show(&popup{icon: m.icon(box), iconFG: m.theme.agent(box.Agent), title: name, titleFG: m.accent(name), kind: "box memory",
		local: func(m *Model) []line { return m.boxMemoryLines(name) }})
}

// boxMemoryLines renders live from the stream: the raw charge, the estimate
// and how it was made, and the overlapping categories it comes from.
func (m *Model) boxMemoryLines(name string) []line {
	t := m.theme
	box, ok := m.box(name)
	if !ok {
		return []line{{txt("box "+name+" is no longer in this space", t.Dim)}}
	}
	state := box.State
	if box.Session == "exited" && box.Runtime == "running" {
		state = "session exited · container running"
	}
	out := []line{kv(t, "State", txt(state, t.Text))}
	bm, why := m.memory(box)
	if why != "" {
		if s := m.d.last; s != nil && s.Boxes[name].Error != "" {
			why += ": " + s.Boxes[name].Error
		}
		return append(out, kv(t, "Memory", txt("no current reading", t.Dim), txt("   "+why, t.Dim)))
	}
	out = append(out, kv(t, "Total", txt(humanBytes(bm.Current), t.Text).bold(),
		txt(fmt.Sprintf("   memory.current, %d bytes, file cache included", bm.Current), t.Dim)))
	switch {
	case !bm.MaxKnown:
		out = append(out, kv(t, "Limit", txt("unknown", t.Bad), txt("   memory.max could not be read", t.Dim)))
	case bm.Max > 0:
		out = append(out, kv(t, "Limit", txt(humanBytes(bm.Max), t.Text), txt("   memory.max: caps the total, not the estimate", t.Dim)))
	default:
		shared := "   shares the workload pool"
		if s := m.d.last; s != nil && s.Pool != nil && s.Pool.Max > 0 {
			shared = "   shares the workload pool's " + humanBytes(s.Pool.Max) + " limit"
		}
		out = append(out, kv(t, "Limit", txt("none of its own", t.Text), txt(shared, t.Dim)))
	}
	switch work, ok := bm.working(); {
	case ok:
		out = append(out, kv(t, "Working set", txt("~"+humanBytes(work), t.Text).bold(), txt("   estimated: total − inactive_file", t.Dim)))
	case bm.Stat == nil:
		out = append(out, kv(t, "Working set", txt("no estimate", t.Dim), txt("   memory.stat could not be read", t.Dim)))
	default:
		out = append(out, kv(t, "Working set", txt("no estimate", t.Dim), txt("   memory.stat reported no inactive_file", t.Dim)))
	}
	if st := bm.Stat; st != nil {
		out = append(out, nil, line{txt("memory.stat", t.Label).bold(), txt("  categories overlap, so they do not add up to the total", t.Dim)})
		stat := func(key string, v *uint64, note string) {
			name := txt(fmt.Sprintf("  %-15s", key), t.Label)
			if v == nil {
				out = append(out, line{name, txt("not reported", t.Dim)})
				return
			}
			out = append(out, line{name, txt(humanBytes(*v), t.Text), txt("   "+note, t.Dim)})
		}
		stat("inactive_file", st.InactiveFile, "left out of the estimate; may not be reclaimable")
		stat("anon", st.Anon, "memory not backed by files: heaps, stacks")
		stat("file", st.File, "file pages, including shmem")
		stat("shmem", st.Shmem, "part of file: tmpfs and shared memory")
		if st.InactiveFile != nil && *st.InactiveFile > bm.Current {
			out = append(out, line{txt("  inactive_file read above the total (separate reads): the estimate is 0", t.Dim)})
		}
	}
	return append(out, nil,
		line{txt("The estimate is a display convention, not memory the kernel cannot", t.Dim)},
		line{txt("reclaim: active file pages and shmem stay in it. Box charges and VM", t.Dim)},
		line{txt("used are different measures and need not add up. Pool peak and OOM", t.Dim)},
		line{txt("kills are in the VM details.", t.Dim)})
}
