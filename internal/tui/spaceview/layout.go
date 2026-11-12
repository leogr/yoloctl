package spaceview

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Click targets. A target's key identifies it across frames, so hover and
// focus survive re-layout.
const (
	tBox        = "box"
	tBoxMem     = "box-mem" // a box's memory row: its live breakdown
	tWorkdir    = "workdir"
	tPort       = "port"
	tAccount    = "account"
	tAgent      = "agent"
	tFeature    = "feature"
	tCapability = "capability"
	tCaps       = "caps"
	tRoom       = "room"
	tVMCPU      = "vm-cpu"
	tVMMem      = "vm-mem"
	tDisk       = "disk"        // the data disk
	tSystemDisk = "system-disk" // the VM's root filesystem
	tGrant      = "grant"
	tUnconfined = "unconfined"
	tPrivileged = "privileged" // id is the box; empty on the title row
	tHelp       = "help"
	tQuit       = "quit"
	tPopup      = "popup"        // inside a popup: swallow clicks
	tPopupClose = "popup-close"  // the popup's close control
	tAction     = "popup-action" // id is the action index
)

type target struct{ kind, id string }

func (t target) key() string { return t.kind + "\x00" + t.id }

// frame is one laid-out screen: the canvas plus what each click target means.
type frame struct {
	c        *canvas
	targets  []target // index 0 means no target
	bodyY    int
	bodyH    int
	contentH int
	scroll   int
	boxRows  map[string][2]int // body rows of each card, for focus-follow
	order    []string          // boxes in display order
	cols     int
}

func (f *frame) targetAt(x, y int) (target, bool) {
	if f == nil {
		return target{}, false
	}
	i := f.c.hitAt(x, y)
	if i <= 0 || i >= len(f.targets) {
		return target{}, false
	}
	return f.targets[i], true
}

type builder struct {
	m       *Model
	t       Theme
	targets []target
	index   map[string]int
	// summaryInBar is set by titleBar when the space summary fits on its row,
	// so the body does not repeat it.
	summaryInBar bool
}

func (b *builder) hit(kind, id string) int {
	t := target{kind, id}
	if i, ok := b.index[t.key()]; ok {
		return i
	}
	b.targets = append(b.targets, t)
	b.index[t.key()] = len(b.targets) - 1
	return len(b.targets) - 1
}

func (b *builder) hovered(kind, id string) bool {
	return b.m.hover == target{kind, id}.key()
}

// chip is clickable text that underlines while hovered.
func (b *builder) chip(text string, c Color, kind, id string) seg {
	st := fg(c)
	if b.hovered(kind, id) {
		st = st.underline().bold()
	}
	return seg{text: text, style: st, hit: b.hit(kind, id)}
}

func txt(text string, c Color) seg { return seg{text: text, style: fg(c)} }

// layout breakpoints, by terminal columns.
func columnsFor(w int) int {
	switch {
	case w >= 160:
		return 4
	case w >= 120:
		return 3
	case w >= 80:
		return 2
	default:
		return 1
	}
}

const compactWidth = 48

// render lays out the whole screen for the current model state.
func (m *Model) render() *frame {
	b := &builder{m: m, t: m.theme, targets: []target{{}}, index: map[string]int{}}
	w, h := m.w, m.h
	f := &frame{c: newCanvas(w, h), boxRows: map[string][2]int{}}
	if w < 20 || h < 7 {
		f.c.putFirstRow(line{txt("Resize terminal to at least 20×7", m.theme.FirstRow().Dim)}.fit(w))
		return f
	}
	f.c.putFirstRow(b.titleBar(w))
	layoutBody := func(width int) []line {
		switch {
		case m.d.inv == nil:
			return b.waiting(width)
		case width < compactWidth:
			return b.compactBody(width, f)
		default:
			return b.body(width, f)
		}
	}
	f.bodyY, f.bodyH = 1, h-2
	body := layoutBody(w)
	if len(body) > f.bodyH {
		// The scrollbar owns the last column; lay out content beside it rather
		// than painting over the VM frame (or a compact row's last cell).
		body = layoutBody(w - 1)
	}
	f.contentH = len(body)
	f.scroll = clampScroll(m.scroll, f.contentH, f.bodyH)
	for i := 0; i < f.bodyH && f.scroll+i < len(body); i++ {
		f.c.put(0, f.bodyY+i, body[f.scroll+i].fit(w))
	}
	if f.contentH > f.bodyH {
		b.scrollbar(f)
	}
	f.c.put(0, h-1, b.footer(w, f))
	if m.popup != nil {
		b.popup(f)
	}
	f.targets = b.targets
	return f
}

func clampScroll(scroll, content, view int) int {
	return max(0, min(scroll, content-view))
}

// ---- title and footer ---------------------------------------------------------

// titleBar is the view's first row, drawn on FirstRowBG.
func (b *builder) titleBar(w int) line {
	t, m := b.t.FirstRow(), b.m
	bar := Style{FG: t.Text, BG: FirstRowBG}
	on := func(c Color) Style { return Style{FG: c, BG: FirstRowBG} }
	left := line{{text: " ", style: bar}}
	var warn func(bare bool) line
	if inv := m.d.inv; inv != nil && inv.BoxesError == "" {
		counts := map[string]int{}
		operators, privileged := 0, 0
		for _, box := range inv.Boxes {
			counts[box.State]++
			if slices.ContainsFunc(box.Capabilities, isOperator) {
				operators++
			}
			if privilegeMark(box) != "" {
				privileged++
			}
		}
		parts := []struct {
			state, glyph, label string
			c                   Color
		}{
			{"running", "●", "running", t.OK}, {"exited", "◇", "exited", t.Bad}, {"lost", "✖", "lost", t.Bad},
			{"stopped", "◌", "stopped", t.Dim}, {"configured", "○", "not created", t.Dim},
			{"unavailable", "?", "unavailable", t.Dim}, {"orphaned", "◬", "orphaned", t.Bad}, {"invalid", "✖", "invalid", t.Bad},
		}
		first := true
		for _, p := range parts {
			if counts[p.state] == 0 {
				continue
			}
			if !first {
				left = append(left, seg{text: " · ", style: on(t.Dim)})
			}
			first = false
			left = append(left, seg{text: fmt.Sprintf("%s %d", p.glyph, counts[p.state]), style: on(p.c).bold()})
			if w >= 90 {
				left = append(left, seg{text: " " + p.label, style: on(t.Dim)})
			}
		}
		if len(inv.Boxes) == 0 {
			left = append(left, seg{text: "no boxes yet", style: on(t.Dim)})
		}
		// The authority warnings follow the counts, in words from 90 columns.
		warn = func(bare bool) line {
			var out line
			if operators > 0 {
				label := " ▲ " + plural(operators, "operator box", "operator boxes") + " "
				if bare {
					label = fmt.Sprintf(" ▲ %d ", operators)
				}
				out = append(out, seg{text: "   ", style: bar},
					seg{text: label, style: on(t.OperatorFG).bold(), hit: b.hit(tCapability, operatorCapability)})
			}
			if privileged > 0 {
				label := fmt.Sprintf(" ▲ %d privileged ", privileged)
				if bare {
					label = fmt.Sprintf(" ▲ %d ", privileged)
				}
				out = append(out, seg{text: "   ", style: bar},
					seg{text: label, style: on(t.OperatorFG).bold(), hit: b.hit(tPrivileged, "")})
			}
			return out
		}
	} else {
		left = append(left, seg{text: "space view", style: on(t.Label).bold()})
	}
	var right line
	switch {
	case m.conn.state == connLive && m.paused:
		right = line{{text: "❚❚ PAUSED", style: on(t.Brand).bold()}}
	case m.conn.state == connLive:
		pc := t.OK
		if m.motion {
			pc = t.ActiveTone(t.OK, true, m.now)
		}
		right = line{{text: "◉", style: on(pc)}, {text: " LIVE", style: on(t.OK).bold()},
			{text: " · " + shortInterval(m.opts.Interval), style: on(t.Dim)}}
	case m.conn.state == connConnecting:
		right = line{{text: "◌ CONNECTING", style: on(t.Brand).bold()}}
	default:
		label := "◌ SIGNAL LOST"
		if !m.conn.retryAt.IsZero() {
			label += fmt.Sprintf(" · retry %ds", max(0, int(time.Until(m.conn.retryAt).Seconds()+0.5)))
		}
		right = line{{text: label, style: on(t.Bad).bold()}}
	}
	if w >= 70 {
		right = append(right, seg{text: "   ", style: bar},
			seg{text: "?", style: on(t.Brand).bold(), hit: b.hit(tHelp, "")}, seg{text: " help  ", style: on(t.Dim), hit: b.hit(tHelp, "")},
			seg{text: "q", style: on(t.Brand).bold(), hit: b.hit(tQuit, "")}, seg{text: " quit", style: on(t.Dim), hit: b.hit(tQuit, "")})
	}
	right = append(right, seg{text: " ", style: bar})
	// Authority and disk warnings share the slot after the counts.
	alerts := func(bare bool) line {
		var out line
		if warn != nil {
			out = warn(bare)
		}
		return append(out, b.diskAlerts(on, bar, bare)...)
	}
	if len(alerts(false)) > 0 {
		// A warning shortens to its bare count rather than being cut off.
		full := append(slices.Clone(left), alerts(w < 90)...)
		if w >= 90 && full.width()+right.width() > w {
			full = append(slices.Clone(left), alerts(true)...)
		}
		left = full
	}
	// The space summary joins the counters when the row has room, first in
	// words and then as bare counts; otherwise it opens the body.
	if m.d.inv != nil {
		for _, short := range []bool{false, true} {
			var chips line
			for _, c := range b.summaryChips(t, short) {
				chips = append(chips, c...)
			}
			if left.width()+3+chips.width()+right.width() <= w {
				left = append(append(left, seg{text: "   ", style: bar}), onSurface(chips, FirstRowBG)...)
				b.summaryInBar = true
				break
			}
		}
	}
	return spread(left, right, w, bar)
}

// onSurface gives segments without a background the row's background bg.
func onSurface(l line, bg Color) line {
	out := slices.Clone(l)
	for i := range out {
		if out[i].style.BG == 0 {
			out[i].style.BG = bg
		}
	}
	return out
}

func shortInterval(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func (b *builder) footer(w int, f *frame) line {
	t, m := b.t, b.m
	bar := Style{FG: t.Dim, BG: t.Surface}
	on := func(c Color) Style { return Style{FG: c, BG: t.Surface} }
	var left line
	switch {
	case m.popup != nil:
		left = line{{text: " ▸ ", style: on(t.Brand).bold()}, {text: m.popup.title, style: on(t.Bright).bold()},
			{text: "  esc or a click outside closes", style: bar}}
	case m.filtering:
		left = line{{text: " / ", style: on(t.Brand).bold()}, {text: m.filter + "▏", style: on(t.Bright)},
			{text: "  type to filter boxes · enter keeps · esc clears", style: bar}}
	case m.toast != nil && m.now.Before(m.toast.until):
		kind := m.toast.kind
		c := t.LogTone(kind)
		left = line{{text: " " + prettylog.DefaultGlyphs.For(kind) + " ", style: on(c).bold()}, {text: m.toast.text, style: on(c)}}
	default:
		left = b.hint(bar, on)
	}
	keys := "↑↓←→ move  ⏎ attach  i inspect  / filter  s sort  ? keys "
	switch {
	case w < 70:
		keys = "? keys "
	case w < 120:
		keys = "⏎ attach  i inspect  ? keys "
	}
	switch {
	case m.popup != nil:
		keys = "↑↓ scroll  esc close "
		if len(m.popup.actions) > 0 && w >= 70 {
			keys = "⏎ " + m.popup.actions[0].label + "  " + keys
		}
	case f.contentH > f.bodyH:
		keys += fmt.Sprintf(" ▾ %3d%% ", f.scroll*100/(f.contentH-f.bodyH))
	}
	right := line{{text: keys, style: bar}}
	if m.filter != "" && !m.filtering && m.popup == nil {
		right = append(line{{text: "filter: " + m.filter + "  ", style: on(t.Brand)}}, right...)
	}
	return spread(left, right, w, bar)
}

// hint describes the hovered (or focused) element and what a click does.
func (b *builder) hint(bar Style, on func(Color) Style) line {
	t, m := b.t, b.m
	key := m.hover
	var tg target
	for _, cand := range m.lastTargets {
		if cand.key() == key {
			tg = cand
		}
	}
	if tg.kind == "" && m.focus != "" {
		tg = target{tBox, m.focus}
	}
	name := line{{text: " ▸ ", style: on(t.Brand).bold()}, {text: tg.id, style: on(t.Bright).bold()}}
	var what string
	switch tg.kind {
	case tBox:
		box, ok := m.box(tg.id)
		if !ok {
			return line{{text: " ", style: bar}}
		}
		what = "  click to " + attachVerb(box) + " · right-click or i to inspect"
		if b.m.w < 100 {
			what = "  click: " + attachVerb(box)
		}
		if attachVerb(box) == "" {
			what = "  click to inspect"
		}
		if box.Runtime == "running" {
			state := "active"
			switch a, why := m.activity(box.Name); a {
			case activityIdle:
				state = "idle"
			case activityUnknown:
				state = "activity unknown (" + why + ")"
			}
			what = "  " + state + " · " + strings.TrimPrefix(what, "  ")
		}
	case tAccount:
		what = "  account · click for account show"
	case tWorkdir:
		if tg.id == "" {
			name[1].text = "ephemeral workdir"
		}
		what = "  click for the workdir and the boxes sharing it"
	case tPort:
		what = "  published port · click for forwards and to open it"
	case tAgent:
		what = "  agent · click for agent show"
	case tFeature:
		what = "  feature · click for feature show"
	case tCapability:
		what = "  capability · click for capability show"
	case tCaps:
		what = "  click for this box's effective capabilities"
	case tRoom:
		what = "  chat room · click for its observed connections"
	case tVMCPU, tVMMem:
		name[1].text = "VM"
		what = "  click for VM and workload pool details"
	case tDisk, tSystemDisk:
		d := m.disks()[1]
		if tg.kind == tSystemDisk {
			d = m.disks()[0]
		}
		name[1].text = d.name + " disk"
		what = "  " + d.about
		if u := d.usage; u != nil {
			what += fmt.Sprintf(" · %s of %s GiB used, %s GiB free", gib(u.Used()), gib(u.Total), gib(u.Free))
		}
		what += " · click for details"
	case tGrant:
		what = "  host mount grant"
	case tUnconfined:
		name[1].text = "unconfined network"
		what = "  this box has no egress allow-list"
	case tPrivileged:
		what = "  privileged: space-VM root, no OpenShell · click for box inspect"
		if tg.id == "" {
			name[1].text = "privileged boxes"
			what = "  space-VM root, no OpenShell · click a box's pill to inspect it"
		}
	case tHelp:
		name[1].text = "help"
		what = "  keys and gestures"
	case tQuit:
		name[1].text = "quit"
		what = "  leave the view; boxes keep running"
	default:
		return line{{text: " " + b.m.statusText(), style: bar}}
	}
	return append(name, seg{text: what, style: bar})
}

func attachVerb(box spacetelemetry.Box) string {
	switch {
	case box.Session == "lost" || box.State == "orphaned" || box.State == "invalid" || box.State == "unavailable":
		return ""
	case box.Runtime == "running" && box.Session == "running":
		return "attach"
	case box.Runtime == "running":
		return "start a new session"
	default:
		return "start and attach"
	}
}

// ---- scrollbar ------------------------------------------------------------------

// scrollbar draws in the rightmost column, reserved by render.
func (b *builder) scrollbar(f *frame) {
	view, content := f.bodyH, f.contentH
	thumb := max(1, view*view/content)
	at := 0
	if content > view {
		at = f.scroll * (view - thumb) / (content - view)
	}
	for i := range thumb {
		f.c.put(f.c.w-1, f.bodyY+at+i, line{{text: "┃", style: fg(b.t.Brand).bold()}})
	}
}

// ---- waiting / unavailable ---------------------------------------------------------

func (b *builder) waiting(w int) []line {
	t, m := b.t, b.m
	var out []line
	out = append(out, nil, nil)
	frames := []string{"◐", "◓", "◑", "◒"}
	switch m.conn.state {
	case connConnecting, connLive:
		out = append(out, line{txt("   "+frames[m.pulse%len(frames)]+" ", t.Brand), txt("connecting to space ", t.Text), txt(m.opts.Space, t.Bright).bold()})
		out = append(out, nil, line{txt("   space view opens one live read stream to the space controller.", t.Dim)})
	default:
		out = append(out, line{txt("   ◌ ", t.Bad), txt("space ", t.Text), txt(m.opts.Space, t.Bright).bold(), txt(" is not available", t.Text)})
		out = append(out, nil)
		for _, l := range wrapText(m.conn.err, max(w-6, 20)) {
			out = append(out, line{txt("   "+l, t.Dim)})
		}
		out = append(out, nil, line{txt("   space view never starts a VM. It keeps checking and connects once the", t.Dim)},
			line{txt("   space is running: ", t.Dim), txt("yoloctl space start "+m.opts.Space, t.Brand)})
	}
	return out
}

func (s seg) bold() seg {
	s.style.Bold = true
	return s
}

func wrapText(s string, n int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			if cur != "" && textWidth(cur)+1+textWidth(word) > n {
				out = append(out, cur)
				cur = ""
			}
			if cur != "" {
				cur += " "
			}
			cur += word
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

// ---- main body ------------------------------------------------------------------------

func (b *builder) body(w int, f *frame) []line {
	t, m := b.t, b.m
	inv := m.d.inv
	inner := w - 4
	var out []line
	if !b.summaryInBar {
		out = append(out, flowTight(b.summaryChips(b.t, false), w-1, 1)...)
	}
	out = append(out, nil)
	host, pipes := b.hostBand(w)
	out = append(out, host...)
	if len(pipes) > 0 {
		out = append(out, b.pipeRow(w, pipes))
	}
	out = append(out, b.frameTop(w, pipes))
	vm := func(l line) line {
		return append(append(line{{text: "║", style: fg(t.Frame)}, {text: " "}}, l.fit(inner)...), seg{text: " "}, seg{text: "║", style: fg(t.Frame)})
	}
	for _, l := range b.gauges(inner) {
		out = append(out, vm(l))
	}
	out = append(out, vm(nil))
	if inv.BoxesError != "" {
		out = append(out, vm(line{txt("◬ box inventory unavailable: ", t.Bad).bold(), txt(inv.BoxesError, t.Dim)}))
		out = append(out, vm(nil))
	}
	if rooms := b.chatRows(inner); len(rooms) > 0 {
		for _, l := range rooms {
			out = append(out, vm(l))
		}
		out = append(out, vm(nil))
	}
	boxes := m.visibleBoxes()
	f.order = names(boxes)
	cols := columnsFor(w)
	cw := (inner - (cols - 1)) / cols
	for cw < 34 && cols > 1 {
		cols--
		cw = (inner - (cols - 1)) / cols
	}
	f.cols = cols
	if len(boxes) == 0 && inv.BoxesError == "" {
		msg := "no boxes in this space yet · yoloctl box create NAME --agent AGENT --account ACCOUNT"
		if m.filter != "" {
			msg = "no boxes match the filter " + m.filter
		}
		out = append(out, vm(line{txt(msg, t.Dim)}), vm(nil))
	}
	for i := 0; i < len(boxes); i += cols {
		row := boxes[i:min(i+cols, len(boxes))]
		cards := make([][]line, len(row))
		for j, box := range row {
			cards[j] = b.card(box, cw)
			f.boxRows[box.Name] = [2]int{len(out), len(out) + len(cards[j])}
		}
		for r := range cards[0] {
			var l line
			for j := range cards {
				if j > 0 {
					l = append(l, seg{text: " "})
				}
				l = append(l, cards[j][r]...)
			}
			out = append(out, vm(l))
		}
		out = append(out, vm(nil))
	}
	for _, l := range b.diskBand(inner) {
		out = append(out, vm(l))
	}
	out = append(out, line{{text: "╚" + strings.Repeat("═", w-2) + "╝", style: fg(t.Frame)}})
	return out
}

func names(boxes []spacetelemetry.Box) []string {
	out := make([]string, len(boxes))
	for i, b := range boxes {
		out[i] = b.Name
	}
	return out
}

// summaryChips counts the space's accounts, ports, host workdirs, grants and
// chat rooms, then gives the VM's uptime. Short chips drop the words. The VM
// itself is named on its frame, so its size is not repeated here. t colors
// the chips: the body's theme, or the first row's in the title bar.
func (b *builder) summaryChips(t Theme, short bool) []line {
	m := b.m
	inv := m.d.inv
	ports, workdirs := 0, map[string]bool{}
	for _, box := range inv.Boxes {
		ports += len(box.Ports)
		if box.Workdir != "" {
			workdirs[box.Workdir] = true
		}
	}
	count := func(glyph string, n int, one, many string) string {
		if short {
			return fmt.Sprintf("%s%d", glyph, n)
		}
		return glyph + " " + plural(n, one, many)
	}
	chips := []line{
		{b.chip(count("@", len(inv.Accounts), "account", "accounts"), t.Account, tDisk, "")},
		{txt(count("⇡", ports, "port", "ports"), t.Port)},
		{txt(count("⌂", len(workdirs), "host workdir", "host workdirs"), t.Workdir)},
		{txt(count("⚑", len(inv.Grants), "grant", "grants"), t.Capability)},
	}
	if n := len(inv.Rooms); n > 0 {
		chips = append(chips, line{txt(count(ChatIcon, n, "chat room", "chat rooms"), t.Room)})
	}
	if s := m.d.last; s != nil && s.Uptime > 0 {
		chips = append(chips, line{txt("up ", t.Dim), txt(humanDuration(time.Duration(s.Uptime)*time.Second), t.Text)})
	}
	for i := range chips[:len(chips)-1] {
		chips[i] = append(chips[i], txt(" · ", t.Dim))
	}
	return chips
}

// flowTight is flow without the two-column gap: chips carry their own
// separators.
func flowTight(chips []line, n, indent int) []line {
	var rows []line
	cur := line{{text: strings.Repeat(" ", indent)}}
	for _, c := range chips {
		if cur.width() > indent && cur.width()+c.width() > n {
			rows = append(rows, cur)
			cur = line{{text: strings.Repeat(" ", indent)}}
		}
		cur = append(cur, c...)
	}
	return append(rows, cur)
}

// hostBand lists host workdirs, published ports and grants. When it fits on
// one row, it also returns pipe columns that drop into the VM frame.
func (b *builder) hostBand(w int) ([]line, []pipe) {
	t, m := b.t, b.m
	inv := m.d.inv
	type wd struct {
		path  string
		count int
	}
	var wds []wd
	seenWD := map[string]int{}
	var ports []line
	var portBoxes []string
	for _, box := range m.sortedBoxes() {
		if box.Workdir != "" {
			if i, ok := seenWD[box.Workdir]; ok {
				wds[i].count++
			} else {
				seenWD[box.Workdir] = len(wds)
				wds = append(wds, wd{box.Workdir, 1})
			}
		}
		for _, p := range box.Ports {
			id := box.Name + "/" + fmt.Sprint(p.Published)
			ports = append(ports, line{b.chip("⇡ :"+fmt.Sprint(p.Published), t.Port, tPort, id)})
			portBoxes = append(portBoxes, box.Name)
		}
	}
	label := line{{text: " HOST ", style: fg(t.Frame).bold()}, {text: " "}}
	var wdChips []line
	for _, d := range wds {
		wdChips = append(wdChips, line{b.chip("⌂ "+homeShort(d.path, m.opts.Home), t.Workdir, tWorkdir, d.path), txt(fmt.Sprintf(" ×%d", d.count), t.Dim)})
	}
	var grants line
	if len(inv.Grants) > 0 {
		grants = line{txt("grants ", t.Dim)}
		for i, g := range inv.Grants {
			if i > 0 {
				grants = append(grants, txt(" · ", t.Dim))
			}
			mode := " ro"
			if g.Writable {
				mode = " rw"
			}
			grants = append(grants, b.chip(homeShort(g.Path, m.opts.Home)+mode, t.Capability, tGrant, g.Path))
		}
	}
	// One row: workdirs, ports, grants right-aligned.
	one := slices.Clone(label)
	var pipes []pipe
	for i, c := range wdChips {
		if i > 0 {
			one = append(one, seg{text: "  "})
		}
		pipes = append(pipes, pipe{x: one.width() + 2, port: false, key: wds[i].path})
		one = append(one, c...)
	}
	if len(ports) > 0 {
		one = append(one, seg{text: "   "})
	}
	for i, c := range ports {
		if i > 0 {
			one = append(one, seg{text: "  "})
		}
		pipes = append(pipes, pipe{x: one.width(), port: true, key: portBoxes[i]})
		one = append(one, c...)
	}
	if w >= 100 && one.width()+grants.width()+3 <= w {
		return []line{spread(one, append(grants, seg{text: " "}), w, Style{})}, pipes
	}
	// Wrapped: chips flow below the label; no pipes.
	var chips []line
	chips = append(chips, wdChips...)
	chips = append(chips, ports...)
	if len(grants) > 0 {
		chips = append(chips, grants)
	}
	if len(chips) == 0 {
		return []line{append(label, txt("no host workdirs, ports or grants", t.Dim))}, nil
	}
	rows := flow(chips, w-1, label.width())
	rows[0] = append(label, rows[0][1:]...)
	return rows, nil
}

type pipe struct {
	x    int
	port bool
	key  string // workdir path or box name
}

// flow wraps chips into rows of at most n columns; continuation rows are
// indented. The first row starts with an indent placeholder.
func flow(chips []line, n, indent int) []line {
	var rows []line
	cur := line{{text: strings.Repeat(" ", indent)}}
	for _, c := range chips {
		if cur.width() > indent && cur.width()+2+c.width() > n {
			rows = append(rows, cur)
			cur = line{{text: strings.Repeat(" ", indent)}}
		}
		if cur.width() > indent {
			cur = append(cur, seg{text: "  "})
		}
		cur = append(cur, c...)
	}
	return append(rows, cur)
}

func (b *builder) pipeActive(p pipe) bool {
	m := b.m
	if !m.motion || m.pulse%4 >= 2 {
		return false
	}
	for _, box := range m.d.inv.Boxes {
		live := m.d.boxes[box.Name]
		if live == nil || !live.Rated {
			continue
		}
		if p.port && box.Name == p.key && live.NetRx+live.NetTx > 4096 {
			return true
		}
		if !p.port && box.Workdir == p.key && live.IOWrite+live.IORead > 4096 {
			return true
		}
	}
	return false
}

func (b *builder) pipeColor(p pipe) Color {
	t := b.t
	switch {
	case p.port && b.pipeActive(p):
		return t.PortHi
	case p.port:
		return t.Port
	case b.pipeActive(p):
		return t.WorkdirHi
	default:
		return t.Workdir
	}
}

func (b *builder) pipeRow(w int, pipes []pipe) line {
	cells := make(line, w)
	for i := range cells {
		cells[i] = seg{text: " "}
	}
	for _, p := range pipes {
		if p.x < w {
			cells[p.x] = seg{text: "┃", style: fg(b.pipeColor(p))}
		}
	}
	return cells
}

// frameTop is the VM frame's top edge: pipe arrows where host items drop in,
// and the VM's name right-aligned. Arrows win; the name shortens to fit.
func (b *builder) frameTop(w int, pipes []pipe) line {
	t := b.t
	cells := make(line, w)
	for i := range cells {
		cells[i] = seg{text: "═", style: fg(t.Frame)}
	}
	cells[0].text, cells[w-1].text = "╔", "╗"
	last := 0
	for _, p := range pipes {
		if p.x > 0 && p.x < w-1 {
			glyph := "▼"
			if p.port {
				glyph = "▲"
			}
			cells[p.x] = seg{text: glyph, style: fg(b.pipeColor(p))}
			last = max(last, p.x)
		}
	}
	// One ═ before ╗, and two cells clear of the last arrow (or after ╔).
	label := b.vmLabel(w - 2 - (last + 2))
	x := w - 2 - label.width()
	for _, part := range label {
		for _, r := range part.text {
			cells[x] = seg{text: string(r), style: part.style, hit: b.hit(tVMCPU, "")}
			x++
		}
	}
	return cells
}

// vmLabel names the VM as " <space> · <backend> VM " within n columns. The
// space name is shortened first, then dropped; nothing is shown when even the
// backend does not fit. Space and backend names are single-column ASCII.
func (b *builder) vmLabel(n int) line {
	plain := fg(b.t.Label)
	named := func(name, tail string) line {
		return line{{text: " ", style: plain}, {text: name, style: plain.bold()}, {text: tail, style: plain}}
	}
	kind := "VM"
	if backend := b.m.opts.VM.Backend; backend != "" {
		kind = backend + " VM"
	}
	tail := " · " + kind + " "
	switch room := n - 1 - textWidth(tail); {
	case room >= textWidth(b.m.opts.Space):
		return named(b.m.opts.Space, tail)
	case room >= 4:
		return named(truncate(b.m.opts.Space, room), tail)
	case textWidth(kind)+2 <= n:
		return line{{text: " " + kind + " ", style: plain}}
	default:
		return nil
	}
}

// ---- VM gauges ----------------------------------------------------------------------

func (b *builder) gauges(inner int) []line {
	t, m := b.t, b.m
	d := m.d
	cpus := float64(d.cpus())
	cpuHit, memHit := b.hit(tVMCPU, ""), b.hit(tVMMem, "")
	boxes := m.sortedBoxes()
	var cpuParts []part
	var boxCores float64
	var legend []line
	for _, box := range boxes {
		live := d.boxes[box.Name]
		if live == nil || box.Runtime != "running" {
			continue
		}
		cores := m.shownCPU(box.Name)
		c := m.accent(box.Name)
		h := b.hit(tBox, box.Name)
		cpuParts = append(cpuParts, part{value: cores, color: c, hit: h})
		boxCores += cores
		legend = append(legend, line{b.chip("■ "+box.Name+fmt.Sprintf(" %.2f", cores), c, tBox, box.Name)})
	}
	s := d.last
	builds, system := 0.0, 0.0
	if s != nil {
		if s.Pool != nil {
			builds = max(0, d.poolCPU-boxCores)
		}
		used := m.shownVM() * cpus
		system = max(0, used-max(d.poolCPU, boxCores))
	}
	cpuParts = append(cpuParts, part{value: builds, color: t.Builds}, part{value: system, color: t.System})
	legend = append(legend, line{txt(fmt.Sprintf("■ builds %.2f", builds), t.Builds)}, line{txt(fmt.Sprintf("■ system %.2f", system), t.System)})

	wide := inner >= 100
	barW := min(48, inner-5-2-14)
	pct := fmt.Sprintf(" %3.0f%%", m.shownVM()*100)
	cpuRow := append(line{{text: "CPU  ", style: fg(t.Label).bold(), hit: cpuHit}}, stacked(t, cpuParts, cpus, barW, cpuHit)...)
	cpuRow = append(cpuRow, seg{text: pct, style: fg(t.Bright).bold(), hit: cpuHit})
	// The legend names the bar's colors before the graphs use them.
	out := flow(legend, inner, 6) // CPU label (5) plus the bar's left edge (1)
	loadText := ""
	if s != nil {
		loadText = fmt.Sprintf("load %.2f %.2f %.2f", s.Load[0], s.Load[1], s.Load[2])
	}
	chartCells := min(inner-cpuRow.width()-2-textWidth(loadText)-2, 40)
	if wide && chartCells >= 12 {
		top, bottom := chart2(t, d.vmHist, chartCells, cpuHit)
		out = append(out, append(append(append(cpuRow, seg{text: "  "}), top...), seg{text: "  "}, txt(loadText, t.Text)))
		pad := line{{text: strings.Repeat(" ", cpuRow.width()+2)}}
		out = append(out, append(append(pad, bottom...), txt(fmt.Sprintf("  of %d cpu · last %s", int(cpus), historySpan(m.opts.Interval, chartCells)), t.Dim)))
	} else {
		if cpuRow.width()+2+textWidth(loadText) <= inner {
			cpuRow = append(cpuRow, txt("  "+loadText, t.Dim))
		}
		out = append(out, cpuRow)
		out = append(out, append(line{{text: "      "}}, spark(t, d.vmHist, barW, cpuHit)...))
	}

	// VM used and the pool's charge are different measures: box charges
	// include file cache that MemAvailable counts as available, so the fill
	// shows only the labeled used/total and the pool's charge stands apart.
	memRow := line{{text: "MEM  ", style: fg(t.Label).bold(), hit: memHit}}
	var poolRow line
	current, why := m.sampleCurrent()
	switch {
	case s == nil:
		memRow = append(memRow, txt("waiting for the first sample…", t.Dim))
	case !current:
		memRow = append(memRow, seg{text: "no current reading: " + why, style: fg(t.Dim), hit: memHit})
	case s.MemTotal == 0:
		memRow = append(memRow, seg{text: "VM memory unavailable", style: fg(t.Bad), hit: memHit})
	default:
		used := s.MemTotal - min(s.MemAvailable, s.MemTotal)
		memRow = append(memRow, stacked(t, []part{{value: float64(used), color: t.Label}}, float64(s.MemTotal), barW, memHit)...)
		memRow = append(memRow, seg{text: " " + gib(used), style: fg(t.Bright).bold(), hit: memHit}, seg{text: "/" + gib(s.MemTotal) + " GiB", style: fg(t.Dim), hit: memHit})
		if memRow.width()+5 <= inner {
			memRow = append(memRow, seg{text: " used", style: fg(t.Dim), hit: memHit})
		}
	}
	if s != nil && current {
		if p := s.Pool; p != nil {
			poolText := "pool " + gib(p.Current)
			if p.Max > 0 {
				poolText += "/" + gib(p.Max)
			}
			poolText += " GiB incl. cache"
			if p.Peak > 0 {
				poolText += " · peak " + gib(p.Peak)
			}
			oomColor := t.OK
			if p.OOMKills > 0 {
				oomColor = t.Bad
			}
			poolRow = line{{text: poolText + " · oom ", style: fg(t.Dim), hit: memHit}, {text: fmt.Sprint(p.OOMKills), style: fg(oomColor).bold(), hit: memHit}}
		} else if s.PoolError != "" {
			poolRow = line{{text: "pool unavailable", style: fg(t.Bad), hit: memHit}}
		}
	}
	if len(poolRow) > 0 && memRow.width()+3+poolRow.width() <= inner {
		memRow = append(append(memRow, seg{text: "   "}), poolRow...)
		poolRow = nil
	}
	out = append(out, memRow)
	if len(poolRow) > 0 {
		out = append(out, append(line{{text: "      "}}, poolRow...))
	}
	return out
}

func historySpan(interval time.Duration, cells int) string {
	return humanDuration(interval * time.Duration(2*cells))
}

// ---- chat rooms ------------------------------------------------------------------------

func (b *builder) chatRows(inner int) []line {
	t, m := b.t, b.m
	var out []line
	for i, room := range m.d.inv.Rooms {
		c, hi := m.roomColor(i)
		label := line{{text: "CHAT ", style: fg(t.Label).bold()}}
		name := b.chip(ChatIcon+" "+room.Server, c, tRoom, room.Server)
		if m.spotlight() == room.Server {
			name.style.FG = hi
		}
		l := append(label, name)
		if room.Error != "" {
			l = append(l, txt("  connections unavailable: "+room.Error, t.Bad))
			out = append(out, l)
			continue
		}
		l = append(l, txt(" ⟷ ", t.Dim))
		if len(room.Members) == 0 {
			l = append(l, txt("no boxes connected", t.Dim))
		}
		for j, member := range room.Members {
			if j > 0 {
				l = append(l, txt(" · ", t.Dim))
			}
			if box, ok := m.box(member); ok {
				l = append(l, txt(m.icon(box)+" ", t.agent(box.Agent)))
			}
			l = append(l, b.chip(member, m.accent(member), tBox, member))
		}
		var right line
		if len(room.Ports) > 0 {
			ports := make([]string, len(room.Ports))
			for k, p := range room.Ports {
				ports[k] = fmt.Sprint(p)
			}
			right = append(right, txt(":"+strings.Join(ports, ",")+" · ", t.Dim))
		}
		right = append(right, txt(plural(len(room.Members), "box", "boxes")+" connected", t.OK))
		if room.External > 0 {
			right = append(right, txt(fmt.Sprintf(" · %d external", room.External), t.Dim))
		}
		if room.Local > 0 {
			right = append(right, txt(fmt.Sprintf(" · %d local", room.Local), t.Dim))
		}
		if l.width()+2+right.width() <= inner {
			out = append(out, spread(l, right, inner, Style{}))
			continue
		}
		out = append(out, l, append(line{{text: "     "}}, right...))
	}
	return out
}

// ---- cards ---------------------------------------------------------------------------------

const operatorCapability = "yolo.space.operator"

func isOperator(name string) bool { return name == operatorCapability }

func isYolo(name string) bool { return strings.HasPrefix(name, "yolo.") }

func (b *builder) card(box spacetelemetry.Box, cw int) []line {
	t, m := b.t, b.m
	inner := cw - 4
	h := b.hit(tBox, box.Name)
	live := m.d.boxes[box.Name]
	roomIdx, inRoom := m.roomOf(box.Name)
	spot := m.spotlight()
	border := fg(t.Frame)
	switch {
	case m.focus == box.Name:
		border = fg(t.Brand).bold()
	case b.hovered(tBox, box.Name):
		border = fg(t.Brand)
	case spot != "" && inRoom && m.d.inv.Rooms[roomIdx].Server == spot:
		_, hi := m.roomColor(roomIdx)
		border = fg(hi)
	case spot != "":
		border = fg(t.Faint)
	}
	bs := func(text string) seg { return seg{text: text, style: border, hit: h} }
	withHit := func(l line) line {
		for i := range l {
			if l[i].hit == 0 {
				l[i].hit = h
			}
		}
		return l
	}

	// Title: corner, icon, name, state pill.
	corner := bs("─")
	if inRoom {
		c, hi := m.roomColor(roomIdx)
		if spot == m.d.inv.Rooms[roomIdx].Server {
			c = hi
		}
		corner = seg{text: ChatIcon, style: fg(c).bold(), hit: b.hit(tRoom, m.d.inv.Rooms[roomIdx].Server)}
	}
	nameStyle := fg(m.accent(box.Name)).bold()
	title := line{bs("╭"), corner, bs(" "), {text: m.icon(box), style: fg(t.agent(box.Agent)), hit: h}, {text: " ", hit: h},
		{text: truncate(box.Name, max(cw-24, 6)), style: nameStyle, hit: h}, {text: " ", hit: h}}
	pill := b.statePill(box, h)
	right := append(line{{text: " ", hit: h}}, pill...)
	right = append(right, bs(" ─╮"))
	fill := cw - title.width() - right.width()
	title = append(title, bs(strings.Repeat("─", max(fill, 0))))
	title = append(title, right...)

	rows := []line{title}
	body := []line{
		b.identRow(box, inner),
		b.workdirRow(box),
		b.portsRow(box, inner),
		b.featuresRow(box, inner),
		b.yoloRow(box, inner),
	}
	body = append(body, b.metricRows(box, live, inner)...)
	for _, l := range body {
		rows = append(rows, append(append(line{bs("│"), {text: " ", hit: h}}, withHit(l.fit(inner))...), seg{text: " ", hit: h}, bs("│")))
	}
	var foot line
	switch verb := attachVerb(box); verb {
	case "attach":
		foot = line{txt("⏎", t.Brand), txt(" attach · ", t.Dim), txt("i", t.Brand), txt(" inspect", t.Dim)}
	case "start a new session":
		foot = line{txt("⏎", t.Brand), txt(" new session · ", t.Dim), txt("i", t.Brand), txt(" inspect", t.Dim)}
	case "start and attach":
		foot = line{txt("⏎", t.Brand), txt(" start + attach · ", t.Dim), txt("i", t.Brand), txt(" inspect", t.Dim)}
	default:
		foot = line{txt("i", t.Brand), txt(" inspect", t.Dim)}
	}
	foot = withHit(foot)
	bottom := line{bs("╰" + strings.Repeat("─", max(cw-foot.width()-5, 0)) + " ")}
	bottom = append(append(bottom, foot...), bs(" ─╯"))
	return append(rows, bottom)
}

func (b *builder) statePill(box spacetelemetry.Box, h int) line {
	t, m := b.t, b.m
	var glyph, label string
	var c Color
	switch box.State {
	case "running":
		glyph, label, c = "●", "RUNNING", t.OK
	case "exited":
		glyph, label, c = "◇", "EXITED", t.Bad
	case "lost":
		glyph, label, c = "✖", "LOST", t.Bad
	case "stopped":
		glyph, label, c = "◌", "STOPPED", t.Dim
	case "configured":
		glyph, label, c = "○", "NOT CREATED", t.Dim
	case "drifted":
		glyph, label, c = "◐", "DRIFTED", t.Brand
	case "orphaned":
		glyph, label, c = "◬", "ORPHANED", t.Bad
	case "invalid":
		glyph, label, c = "✖", "INVALID", t.Bad
	default:
		glyph, label, c = "?", strings.ToUpper(box.State), t.Dim
	}
	// The label is the box's state; only the dot reflects observed activity.
	// Unproven activity is dimmed so it never reads as idle.
	gc := c
	if box.State == "running" {
		switch a, _ := m.activity(box.Name); a {
		case activityActive:
			gc = t.ActiveTone(c, m.motion, m.now)
		case activityUnknown:
			gc = t.Dim
		}
	}
	return line{{text: glyph, style: fg(gc), hit: h}, {text: " " + label, style: fg(c).bold(), hit: h}}
}

func (b *builder) identRow(box spacetelemetry.Box, inner int) line {
	t, m := b.t, b.m
	var left line
	if box.Agent != "" && box.Agent != "none" {
		left = line{b.chip(box.Agent, t.agent(box.Agent), tAgent, box.Agent), txt(" ▸ ", t.Dim)}
	} else {
		cmd := "shell"
		if len(box.SessionCommand) > 0 {
			cmd = filepath.Base(box.SessionCommand[0])
		}
		left = line{txt(cmd, t.Text), txt(" ▸ ", t.Dim)}
	}
	if box.Account != "" && box.Account != "none" {
		left = append(left, b.chip("@"+box.Account, t.Account, tAccount, box.Account))
	} else {
		left = append(left, txt("no account", t.Dim))
	}
	var right line
	switch box.Session {
	case "running":
		right = line{txt("⌁ ", t.OK), txt("live", t.Text)}
	case "exited":
		code := "?"
		if box.ExitCode != nil {
			code = fmt.Sprint(*box.ExitCode)
		}
		text := "exited " + code
		if box.ExitedAt != nil {
			text += " · " + humanDuration(m.now.Sub(*box.ExitedAt))
		}
		right = line{txt(text, t.Bad)}
	case "lost":
		right = line{txt("session lost", t.Bad)}
	case "none":
		right = line{txt("no session", t.Dim)}
	default:
		right = line{txt("session ?", t.Dim)}
	}
	return spread(left, right, inner, Style{})
}

func (b *builder) workdirRow(box spacetelemetry.Box) line {
	t, m := b.t, b.m
	switch {
	case box.Workdir != "":
		return line{b.chip("⌂ "+homeShort(box.Workdir, m.opts.Home), t.Workdir, tWorkdir, box.Workdir)}
	case box.WorkdirMode == "ephemeral":
		return line{b.chip("⌂ ephemeral (VM-local)", t.Workdir, tWorkdir, "")}
	default:
		return line{txt("⌂ —", t.Dim)}
	}
}

func (b *builder) portsRow(box spacetelemetry.Box, inner int) line {
	t := b.t
	var left line
	for i, p := range box.Ports {
		if i > 0 {
			left = append(left, seg{text: "  "})
		}
		label := fmt.Sprintf("⇡ :%d", p.Published)
		if p.Target != p.Published {
			label += fmt.Sprintf("→%d", p.Target)
		}
		if p.Protocol != "" && p.Protocol != "tcp" {
			label += "/" + p.Protocol
		}
		left = append(left, b.chip(label, t.Port, tPort, box.Name+"/"+fmt.Sprint(p.Published)))
	}
	if len(left) == 0 {
		left = line{txt("⇡ —", t.Dim)}
	}
	if box.Unconfined {
		return spread(left, line{b.chip("▲ unconfined network", t.Bad, tUnconfined, box.Name)}, inner, Style{})
	}
	return left
}

func (b *builder) featuresRow(box spacetelemetry.Box, inner int) line {
	t := b.t
	left := line{txt("+ ", t.Feature)}
	n := 0
	for _, f := range box.Features {
		if f == box.Agent || isYolo(f) {
			continue
		}
		if n > 0 {
			left = append(left, seg{text: "  "})
		}
		left = append(left, b.chip(f, t.Feature, tFeature, f))
		n++
	}
	if n == 0 {
		left = line{txt("+ —", t.Dim)}
	}
	caps := b.chip(fmt.Sprintf("⚑ %d cap", len(box.Capabilities)), t.Capability, tCaps, box.Name)
	return spread(left, line{caps}, inner, Style{})
}

// yoloRow shows yolo.* features and capabilities as raised pills: they change
// what a box can do for or to the rest of the space.
func (b *builder) yoloRow(box spacetelemetry.Box, inner int) line {
	t, m := b.t, b.m
	type item struct {
		name string
		kind string
	}
	var items []item
	// A feature that grants its same-named capability (yolo.space.operator)
	// shows once, as the capability: that is where the authority comes from.
	for _, f := range box.Features {
		if isYolo(f) && !slices.Contains(box.Capabilities, f) {
			items = append(items, item{f, tFeature})
		}
	}
	for _, c := range box.Capabilities {
		if isYolo(c) {
			items = append(items, item{c, tCapability})
		}
	}
	var room line
	for i, r := range m.d.inv.Rooms {
		if r.Server == box.Name {
			c, _ := m.roomColor(i)
			room = line{seg{text: "  "}, b.chip(fmt.Sprintf(ChatIcon+" %s ⟷ %d", r.Server, len(r.Members)), c, tRoom, r.Server)}
		}
	}
	mark := privilegeMark(box)
	build := func(short bool) line {
		var out line
		if mark != "" {
			out = append(out, b.privilegedPill(box, mark, short))
		}
		for i, it := range items {
			if i > 0 || mark != "" {
				out = append(out, seg{text: " "})
			}
			label := it.name
			if short {
				label = strings.TrimPrefix(label, "yolo.")
			}
			st := Style{FG: t.Brand, BG: t.Raised, Bold: true}
			text := " ❯ " + label + " "
			if isOperator(it.name) {
				st = Style{FG: t.OperatorFG, BG: t.OperatorBG, Bold: true}
				text = " ▲ " + label + " "
			}
			if b.hovered(it.kind, it.name) {
				st.Underline = true
			}
			out = append(out, seg{text: text, style: st, hit: b.hit(it.kind, it.name)})
		}
		return out
	}
	pills := build(false)
	if pills.width()+room.width() > inner {
		pills = build(true)
	}
	return append(pills, room...)
}

// privilegeMark is a box's privileged pill text: the declaration first, then
// what the running container's launch evidence adds. It is empty when neither
// shows privilege; that silence is not a claim of confinement, which only box
// inspect states.
func privilegeMark(box spacetelemetry.Box) string {
	switch {
	case box.Privileged && (box.Runtime != "running" || box.RuntimeMode == "privileged"):
		return "privileged"
	case box.Privileged && box.RuntimeMode == "confined":
		return "privileged · running confined"
	case box.Privileged:
		return "privileged · runtime ?"
	case box.RuntimePrivileged:
		return "privileged runtime · declared confined"
	}
	return ""
}

// privilegedPill draws mark in the operator warning colors; the short form
// keeps only "▲ privileged".
func (b *builder) privilegedPill(box spacetelemetry.Box, mark string, short bool) seg {
	if short {
		mark = "privileged"
	}
	st := Style{FG: b.t.OperatorFG, BG: b.t.OperatorBG, Bold: true}
	if b.hovered(tPrivileged, box.Name) {
		st.Underline = true
	}
	return seg{text: " ▲ " + mark + " ", style: st, hit: b.hit(tPrivileged, box.Name)}
}

func (b *builder) metricRows(box spacetelemetry.Box, live *boxLive, inner int) []line {
	t, m := b.t, b.m
	if box.Runtime != "running" || live == nil {
		dots := max(inner-11, 1)
		status := strings.Join(box.Reasons, "; ")
		if status == "" {
			status = box.State
		}
		if box.ConfigError != "" {
			status = box.ConfigError
		}
		return []line{
			{txt("CPU ", t.Label), txt(strings.Repeat("·", dots), t.Faint), txt("      —", t.Dim)},
			{txt("MEM ", t.Label), txt(strings.Repeat("·", dots), t.Faint), txt("      —", t.Dim)},
			{txt(status, t.Dim)},
		}
	}
	if live.Err != "" {
		return []line{
			{txt("CPU ", t.Label), txt("metrics unavailable", t.Bad)},
			{txt("MEM ", t.Label), txt("—", t.Dim)},
			{txt(live.Err, t.Dim)},
		}
	}
	cores := m.shownCPU(box.Name)
	limit := live.CPULimit
	valColor := t.Text
	if limit > 0 && cores/limit >= 0.9 {
		valColor = t.Brand
		if m.motion && m.pulse%4 < 2 {
			valColor = t.Bad
		}
	}
	var value line
	if limit > 0 {
		value = line{txt(fmt.Sprintf("%.2f", cores), valColor).bold(), txt(fmt.Sprintf("/%g cpu", limit), t.Dim)}
	} else {
		value = line{txt(fmt.Sprintf("%.2f", cores), valColor).bold(), txt(" cpu", t.Dim)}
	}
	if !live.Rated {
		value = line{txt("…", t.Dim)}
	}
	cpu := line{txt("CPU ", t.Label)}
	cpu = append(cpu, spark(t, live.Hist, max(inner-4-1-value.width(), 1), 0)...)
	cpu = append(append(cpu, seg{text: " "}), value...)

	mem := b.memRow(box, inner)

	net := txt("↓"+humanRate(live.NetRx)+" ↑"+humanRate(live.NetTx), t.Text)
	io := txt("r"+humanRate(live.IORead)+" w"+humanRate(live.IOWrite), t.Text)
	pids := txt(fmt.Sprint(live.Pids), t.Text)
	up := line{}
	if box.StartedAt != nil {
		upLabel := "  up "
		if box.Session == "exited" {
			upLabel = "  session exited · container up "
		}
		up = line{txt(upLabel, t.Dim), txt(humanDuration(m.now.Sub(*box.StartedAt)), t.Text)}
	}
	variants := []line{
		append(line{txt("net ", t.Dim), net, txt("  io ", t.Dim), io, txt("  pids ", t.Dim), pids}, up...),
		{txt("net ", t.Dim), net, txt("  io ", t.Dim), io, txt("  pids ", t.Dim), pids},
		{txt("net ", t.Dim), net, txt("  io ", t.Dim), txt("w"+humanRate(live.IOWrite), t.Text), txt("  pids ", t.Dim), pids},
		{net, txt("  ", t.Dim), txt("w"+humanRate(live.IOWrite), t.Text), txt("  ", t.Dim), pids, txt(" pids", t.Dim)},
	}
	stats := variants[len(variants)-1]
	for _, v := range variants {
		if v.width() <= inner {
			stats = v
			break
		}
	}
	return []line{cpu, mem, stats}
}

// ---- compact layout (< 48 columns) ----------------------------------------------------------

func (b *builder) compactBody(w int, f *frame) []line {
	t, m := b.t, b.m
	var out []line
	inner := w - 2
	pad := func(l line) line { return append(line{{text: " "}}, l.fit(inner)...) }
	cpuHit, memHit := b.hit(tVMCPU, ""), b.hit(tVMMem, "")
	vmGauge := func(label string, frac float64, value string, hit int) line {
		l := line{{text: label, style: fg(t.Label).bold(), hit: hit}}
		l = append(l, gauge(t, frac, max(inner-12, 4), hit)...)
		return pad(append(l, seg{text: value, style: fg(t.Bright).bold(), hit: hit}))
	}
	out = append(out, vmGauge("cpu ", m.shownVM(), fmt.Sprintf(" %3.0f%%", m.shownVM()*100), cpuHit))
	if s := m.d.last; s != nil {
		switch current, why := m.sampleCurrent(); {
		case !current:
			out = append(out, pad(line{{text: "mem ", style: fg(t.Label).bold(), hit: memHit}, {text: why, style: fg(t.Dim), hit: memHit}}))
		case s.MemTotal == 0:
			out = append(out, pad(line{{text: "mem ", style: fg(t.Label).bold(), hit: memHit}, {text: "unavailable", style: fg(t.Bad), hit: memHit}}))
		default:
			used := s.MemTotal - min(s.MemAvailable, s.MemTotal)
			out = append(out, vmGauge("mem ", float64(used)/float64(s.MemTotal), " "+gib(used)+"G", memHit))
		}
	}
	out = append(out, nil)
	boxes := m.visibleBoxes()
	f.order = names(boxes)
	f.cols = 1
	for _, box := range boxes {
		h := b.hit(tBox, box.Name)
		live := m.d.boxes[box.Name]
		pill := b.statePill(box, h)[:1]
		nameStyle := fg(m.accent(box.Name)).bold()
		if m.focus == box.Name {
			nameStyle = nameStyle.underline()
		}
		head := line{{text: " "}, pill[0], {text: " ", hit: h}, {text: m.icon(box) + " ", style: fg(t.agent(box.Agent)), hit: h}, {text: box.Name, style: nameStyle, hit: h}}
		var right line
		if box.Runtime == "running" {
			if live != nil && live.Rated {
				right = line{{text: fmt.Sprintf("%.2fc ", m.shownCPU(box.Name)), style: fg(t.Text), hit: h}}
			}
			// Only a current raw total is shown; its breakdown is a click away.
			boxMemHit := b.hit(tBoxMem, box.Name)
			if bm, why := m.memory(box); why == "" {
				right = append(right, seg{text: humanBytes(bm.Current) + " total ", style: fg(t.Text), hit: boxMemHit})
			} else {
				right = append(right, seg{text: "mem — ", style: fg(t.Dim), hit: boxMemHit})
			}
		}
		f.boxRows[box.Name] = [2]int{len(out), len(out) + 2}
		out = append(out, spread(head, right, w, Style{}))
		detail := line{{text: "   "}}
		if mark := privilegeMark(box); mark != "" {
			detail = append(detail, b.privilegedPill(box, mark, true), seg{text: " "})
		}
		if box.Account != "" && box.Account != "none" {
			detail = append(detail, b.chip("@"+box.Account, t.Account, tAccount, box.Account), seg{text: " "})
		}
		if box.Workdir != "" {
			detail = append(detail, b.chip("⌂ "+homeShort(box.Workdir, m.opts.Home), t.Workdir, tWorkdir, box.Workdir))
		} else if box.WorkdirMode == "ephemeral" {
			detail = append(detail, b.chip("⌂ ephemeral", t.Workdir, tWorkdir, ""))
		}
		if box.Runtime != "running" {
			detail = append(detail, txt("  "+box.State, t.Dim))
		}
		out = append(out, detail.fit(w))
	}
	return out
}
