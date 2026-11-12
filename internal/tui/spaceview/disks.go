package spaceview

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// The VM's two disks. The system disk is rebuilt with the VM and fills with
// box files, images and build caches; the data disk is kept across rebuilds.
const (
	systemDiskAbout = "boxes, images, build caches"
	dataDiskAbout   = "accounts & box settings (kept across VM rebuilds)"
)

// vmDisk is one disk as the view shows it.
type vmDisk struct {
	name  string // "system" or "data"
	about string
	kind  string // click target
	usage *spacetelemetry.Usage
}

func (m *Model) disks() [2]vmDisk {
	var sys, dat *spacetelemetry.Usage
	if s := m.d.last; s != nil {
		sys, dat = s.SystemDisk, s.Disk
	}
	return [2]vmDisk{
		{name: "system", about: systemDiskAbout, kind: tSystemDisk, usage: known(sys)},
		{name: "data", about: dataDiskAbout, kind: tDisk, usage: known(dat)},
	}
}

// known drops a usage without a size: it is unavailable, never an empty disk.
func known(u *spacetelemetry.Usage) *spacetelemetry.Usage {
	if u == nil || u.Total == 0 {
		return nil
	}
	return u
}

func usedPercent(u spacetelemetry.Usage) float64 {
	return 100 * float64(u.Used()) / float64(u.Total)
}

func (t Theme) level(l spacetelemetry.DiskLevel) Color {
	switch l {
	case spacetelemetry.DiskCritical:
		return t.Bad
	case spacetelemetry.DiskWarn:
		return t.Warn
	}
	return t.Dim
}

// levelGauge is the thermometer gauge until the disk needs attention; then
// the whole fill takes the warning color so it reads at a glance.
func levelGauge(t Theme, frac float64, cells, hit int, l spacetelemetry.DiskLevel) line {
	g := gauge(t, frac, cells, hit)
	if l == spacetelemetry.DiskOK {
		return g
	}
	for i := 1; i < len(g)-1; i++ {
		if g[i].text != "░" {
			g[i].style = fg(t.level(l))
		}
	}
	return g
}

// diskBlock is one disk: its gauge with used/total and free space, then
// what it holds, wrapped under the gauge.
func (b *builder) diskBlock(d vmDisk, width int) []line {
	t := b.t
	hit := b.hit(d.kind, "")
	label := fmt.Sprintf("%-7s", d.name)
	nameStyle := fg(t.Label).bold()
	if b.hovered(d.kind, "") {
		nameStyle = nameStyle.underline()
	}
	head := line{{text: label, style: nameStyle, hit: hit}}
	var about []line
	for _, l := range wrapText(d.about, max(width-len(label), 1)) {
		about = append(about, line{{text: strings.Repeat(" ", len(label)), hit: hit}, {text: l, style: fg(t.Dim), hit: hit}}.fit(width))
	}
	u := d.usage
	if u == nil {
		head = append(head, seg{text: "usage unavailable", style: fg(t.Dim), hit: hit})
		return append([]line{head.fit(width)}, about...)
	}
	l := u.Pressure()
	value := line{{text: " " + gib(u.Used()), style: fg(t.Bright).bold(), hit: hit}, {text: "/" + gib(u.Total) + " GiB", style: fg(t.Dim), hit: hit}}
	free := line{{text: "  " + gib(u.Free) + " GiB free", style: fg(t.Dim), hit: hit}}
	switch l {
	case spacetelemetry.DiskWarn:
		free = line{{text: "  ⚠ " + gib(u.Free) + " GiB free", style: fg(t.Warn).bold(), hit: hit}}
	case spacetelemetry.DiskCritical:
		free = line{{text: "  ✖ " + gib(u.Free) + " GiB free", style: fg(t.Bad).bold(), hit: hit}}
	}
	cells := min(max(width-len(label)-2-value.width()-free.width(), 6), 22)
	head = append(head, levelGauge(t, float64(u.Used())/float64(u.Total), cells, hit, l)...)
	head = append(append(head, value...), free...)
	return append([]line{head.fit(width)}, about...)
}

// diskBand shows both disks: side by side when there is room, stacked
// otherwise, then the accounts on the data disk.
func (b *builder) diskBand(inner int) []line {
	t, m := b.t, b.m
	hit := b.hit(tDisk, "")
	rule := line{{text: "╶── ", style: fg(t.Frame)}, {text: "disks", style: fg(t.Label).bold()},
		{text: " " + strings.Repeat("─", max(inner-11, 0)) + "╴", style: fg(t.Frame)}}
	out := []line{rule}
	disks := m.disks()
	if inner >= 90 {
		cw := (inner - 3) / 2
		left, right := b.diskBlock(disks[0], cw), b.diskBlock(disks[1], inner-3-cw)
		for r := range max(len(left), len(right)) {
			l, rt := line{{text: strings.Repeat(" ", cw)}}, line(nil)
			if r < len(left) {
				l = left[r]
			}
			if r < len(right) {
				rt = right[r]
			}
			out = append(out, append(append(l, seg{text: "   "}), rt...))
		}
	} else {
		for _, d := range disks {
			out = append(out, b.diskBlock(d, inner)...)
		}
	}
	inv := m.d.inv
	var chips []line
	consumers := map[string]int{}
	for _, box := range inv.Boxes {
		consumers[box.Account]++
	}
	for _, a := range inv.Accounts {
		l := line{b.chip("@"+a.Name, t.Account, tAccount, a.Name)}
		for _, agent := range a.Agents {
			l = append(l, txt(" "+m.agentIcon(agent), t.agent(agent)))
		}
		switch n := consumers[a.Name]; n {
		case 0:
			l = append(l, txt(" (no boxes)", t.Dim))
		default:
			l = append(l, txt(" ⟵ "+plural(n, "box", "boxes"), t.Dim))
		}
		if a.Name == inv.DefaultAccount {
			l = append(l, txt(" ★", t.Brand))
		}
		chips = append(chips, l)
	}
	if inv.AccountsError != "" {
		chips = append(chips, line{txt("accounts unavailable: "+inv.AccountsError, t.Bad)})
	}
	first := line{{text: "ACCOUNTS ", style: fg(t.Label).bold(), hit: hit}}
	rows := flow(chips, inner, first.width())
	rows[0] = append(first, rows[0][1:]...)
	return append(out, rows...)
}

// diskAlerts is the title row's warning slot: each disk that needs
// attention, or only the worst one as a bare percentage when the row is tight.
func (b *builder) diskAlerts(on func(Color) Style, bar Style, bare bool) line {
	t := b.t.FirstRow()
	var out line
	worst := spacetelemetry.DiskOK
	for _, d := range b.m.disks() {
		if d.usage == nil {
			continue
		}
		l := d.usage.Pressure()
		if l == spacetelemetry.DiskOK || (bare && l <= worst) {
			continue
		}
		worst = l
		var label string
		switch {
		case bare:
			glyph := "⚠"
			if l == spacetelemetry.DiskCritical {
				glyph = "✖"
			}
			label = fmt.Sprintf(" %s %s %.0f%% ", glyph, d.name, usedPercent(*d.usage))
			out = nil
		case l == spacetelemetry.DiskCritical:
			label = " ✖ " + d.name + " disk almost full "
		default:
			label = fmt.Sprintf(" ⚠ %s disk %.0f%% ", d.name, usedPercent(*d.usage))
		}
		out = append(out, seg{text: "   ", style: bar}, seg{text: label, style: on(t.level(l)).bold(), hit: b.hit(d.kind, "")})
	}
	return out
}

// diskEvents toasts each disk once when it crosses into a worse level. A
// disk that recovers is quiet and may toast again on its next crossing.
func (d *data) diskEvents(s *spacetelemetry.Sample) []event {
	var out []event
	for _, disk := range []struct {
		name  string
		usage *spacetelemetry.Usage
	}{{"system", s.SystemDisk}, {"data", s.Disk}} {
		u := known(disk.usage)
		if u == nil {
			continue // unknown keeps the last crossing; it is not a recovery
		}
		l := u.Pressure()
		prev := d.diskToasted[disk.name]
		d.diskToasted[disk.name] = l
		if l <= prev {
			if l < prev && l != spacetelemetry.DiskOK {
				d.diskToasted[disk.name] = prev // warn after critical stays toasted
			}
			continue
		}
		text := fmt.Sprintf("%s disk low: %s GiB free (%.0f%% used)", disk.name, gib(u.Free), usedPercent(*u))
		if l == spacetelemetry.DiskCritical {
			text = fmt.Sprintf("%s disk almost full: %s GiB free (%.0f%% used)", disk.name, gib(u.Free), usedPercent(*u))
		}
		out = append(out, event{text: text, bad: l == spacetelemetry.DiskCritical, warn: l == spacetelemetry.DiskWarn})
	}
	return out
}

// ---- popups ---------------------------------------------------------------------------------

func (m *Model) openSystemDisk() {
	p := &popup{icon: "▤", iconFG: m.theme.Label, title: "system disk", titleFG: m.theme.Label, kind: "space " + m.opts.Space,
		command: []string{"space", "storage", m.opts.Space, "--json"}}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.storageLines(raw) }
	p.local = func(m *Model) []line { return m.systemDiskLines(p) }
	m.show(p)
}

func (m *Model) usageLines(u *spacetelemetry.Usage) []line {
	t := m.theme
	if u == nil {
		return []line{kv(t, "Used", txt("unavailable", t.Dim))}
	}
	out := []line{kv(t, "Used", txt(gib(u.Used())+" of "+gib(u.Total)+" GiB", t.Text), txt(fmt.Sprintf("   %s GiB free · %.0f%% used", gib(u.Free), usedPercent(*u)), t.Dim))}
	switch u.Pressure() {
	case spacetelemetry.DiskWarn:
		out = append(out, line{txt("⚠ low: under "+gib(u.WarnFree())+" GiB free or 85% used", t.Warn).bold()})
	case spacetelemetry.DiskCritical:
		out = append(out, line{txt("✖ almost full: under "+gib(u.CriticalFree())+" GiB free or 95% used; writes will start to fail", t.Bad).bold()})
	}
	return out
}

func (m *Model) systemDiskLines(p *popup) []line {
	t := m.theme
	out := m.usageLines(m.disks()[0].usage)
	out = append(out, nil,
		line{txt("Holds the guest OS, image layers, BuildKit's cache and every box's own", t.Dim)},
		line{txt("files: its home, /tmp, /var/tmp and anything written outside its mounts.", t.Dim)},
		line{txt("Free space by deleting files in a box, recreating a box (its files go),", t.Dim)},
		line{txt("or yoloctl image prune. A larger disk needs a new VM size.", t.Dim)}, nil)
	switch {
	case p.loading:
		frames := []string{"◐", "◓", "◑", "◒"}
		out = append(out, line{txt(frames[m.pulse%4]+" measuring what fills it…", t.Dim)})
	case p.err != "":
		out = append(out, line{txt("What fills it", t.Label).bold()})
		for _, l := range wrapText("unavailable: "+p.err, 80) {
			out = append(out, line{txt(l, t.Bad)})
		}
	default:
		out = append(out, p.lines...)
	}
	return out
}

// storageReport mirrors `yoloctl space storage --json`.
type storageReport struct {
	MeasuredAt time.Time `json:"measured_at"`
	System     *struct {
		Used uint64 `json:"used"`
	} `json:"system"`
	Boxes []struct {
		Name     string `json:"name"`
		Running  bool   `json:"running"`
		Declared bool   `json:"declared"`
		Bytes    uint64 `json:"bytes"`
	} `json:"boxes"`
	Images *struct {
		Bytes  uint64 `json:"bytes"`
		Layers uint64 `json:"layers"`
		Blobs  uint64 `json:"blobs"`
	} `json:"images"`
	BuildKit *struct {
		Bytes uint64 `json:"bytes"`
	} `json:"buildkit"`
	Other  *uint64 `json:"other"`
	Errors []struct {
		Item   string `json:"item"`
		Reason string `json:"reason"`
	} `json:"errors"`
}

// storageLines lists what fills the system disk, largest first. A part that
// was not measured says so with its reason.
func (m *Model) storageLines(raw []byte) ([]line, error) {
	var r storageReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	t := m.theme
	type row struct {
		name, note string
		bytes      uint64
		color      Color
	}
	var rows []row
	for _, b := range r.Boxes {
		note := "box, stopped"
		if b.Running {
			note = "box, running"
		}
		if !b.Declared {
			note = strings.Replace(note, "box", "container", 1)
		}
		c := t.Text
		if b.Declared {
			c = m.accent(b.Name)
		}
		rows = append(rows, row{b.Name, note, b.Bytes, c})
	}
	if r.Images != nil {
		rows = append(rows, row{"images", "layers " + humanBytes(r.Images.Layers) + ", downloads " + humanBytes(r.Images.Blobs), r.Images.Bytes, t.Label})
	}
	if r.BuildKit != nil {
		rows = append(rows, row{"build cache", "BuildKit", r.BuildKit.Bytes, t.Label})
	}
	if r.Other != nil {
		rows = append(rows, row{"guest OS and other", "includes the filesystem reserve", *r.Other, t.Label})
	}
	// Largest first: what to look at is at the top.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].bytes > rows[j-1].bytes; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	var used uint64
	if r.System != nil {
		used = r.System.Used
	}
	out := []line{{txt("What fills it", t.Label).bold(), txt("  measured "+r.MeasuredAt.Local().Format("15:04:05"), t.Dim)}}
	for _, rw := range rows {
		bar := ""
		if used > 0 {
			bar = strings.Repeat("▪", min(int(float64(rw.bytes)/float64(used)*20+0.5), 20))
		}
		out = append(out, line{txt("  "+fmt.Sprintf("%-20s", truncate(rw.name, 20)), rw.color).bold(),
			txt(fmt.Sprintf("%8s ", humanBytes(rw.bytes)), t.Text), txt(fmt.Sprintf("%-20s ", bar), t.Brand), txt(rw.note, t.Dim)})
	}
	if r.Images == nil {
		out = append(out, line{txt("  images", t.Label).bold(), txt("  unavailable", t.Dim)})
	}
	if r.BuildKit == nil {
		out = append(out, line{txt("  build cache", t.Label).bold(), txt("  unavailable", t.Dim)})
	}
	if r.Other == nil {
		out = append(out, line{txt("  guest OS and other", t.Label).bold(), txt("  unavailable: not every part was measured", t.Dim)})
	}
	for _, e := range r.Errors {
		out = append(out, line{txt("  unavailable: "+e.Item+": "+e.Reason, t.Bad)})
	}
	return out, nil
}
