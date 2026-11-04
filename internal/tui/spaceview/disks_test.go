package spaceview

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

const gib30 = 30 << 30

// withDisks replaces the last sample's disks, as a new sample would.
func withDisks(m *Model, system, data *spacetelemetry.Usage) {
	s := *m.d.last
	s.SystemDisk, s.Disk = system, data
	m.d.last = &s
}

// rowsWith returns the plain screen rows containing every word.
func rowsWith(m *Model, words ...string) []string {
	var out []string
	for _, row := range strings.Split(m.last.c.plain(), "\n") {
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(row, w)
		}
		if ok {
			out = append(out, row)
		}
	}
	return out
}

// Both disks appear with fixed explanations: side by side from 90 inner
// columns, stacked below that.
func TestDiskBandShowsBothDisks(t *testing.T) {
	for _, w := range []int{80, 100, 140} {
		m := fixtureModel(t, w, 200)
		m.View()
		screen := m.last.c.plain()
		for _, want := range []string{"disks", "system", "20/30 GiB", "10 GiB free", "data", "18/64 GiB", "46 GiB free",
			systemDiskAbout, "ACCOUNTS"} {
			if !strings.Contains(screen, want) {
				t.Errorf("%d columns lack %q:\n%s", w, want, screen)
			}
		}
		if !strings.Contains(screen, "accounts & box settings") || !strings.Contains(screen, "rebuilds)") {
			t.Errorf("%d columns lack the data disk's explanation", w)
		}
		side := len(rowsWith(m, "system", "data")) > 0
		if wantSide := w-4 >= 90; side != wantSide {
			t.Errorf("%d columns: side by side %v, want %v", w, side, wantSide)
		}
	}
}

func TestDiskBandUnknownUsage(t *testing.T) {
	m := fixtureModel(t, 140, 200)
	withDisks(m, nil, &spacetelemetry.Usage{})
	m.View()
	if got := rowsWith(m, "system", "usage unavailable", "data", "usage unavailable"); len(got) != 1 {
		t.Fatalf("unknown disks:\n%s", m.last.c.plain())
	}
	if strings.Contains(m.last.c.plain(), "disk almost") || strings.Contains(m.last.c.plain(), "⚠") {
		t.Fatal("an unknown disk warned")
	}
}

// fillColor is the color of a disk gauge's first filled cell.
func fillColor(t *testing.T, m *Model, kind string) Color {
	t.Helper()
	x, y := findTargetFrom(t, m, target{kind, ""}, 1) // row 0 holds the title's warning
	for ; x < m.last.c.w; x++ {
		if c := m.last.c.cells[y][x]; c.text == "█" {
			return c.style.FG
		}
	}
	t.Fatalf("%s gauge has no filled cell", kind)
	return 0
}

// Warn below 4 GiB free or from 85% used, critical below 2 GiB or from 95%;
// the header slot names the disk and a new crossing toasts once.
func TestDiskThresholdsWarnInBandHeaderAndToast(t *testing.T) {
	sample := func(m *Model, step int, system, data *spacetelemetry.Usage) {
		s := fixtureSample(step)
		s.SystemDisk, s.Disk = system, data
		m.Update(frameMsg(spacetelemetry.Frame{Kind: spacetelemetry.KindSample, At: fixtureStart.Add(time.Duration(step) * time.Second), Sample: s}))
		m.View()
	}
	data := &spacetelemetry.Usage{Total: 64 << 30, Free: 46 << 30}
	m := fixtureModel(t, 140, 60)
	m.View()
	if m.toast != nil || strings.Contains(m.last.c.plain(), "system disk") {
		t.Fatal("a healthy disk warned")
	}

	sample(m, 41, &spacetelemetry.Usage{Total: gib30, Free: 3 << 30}, data)
	title := strings.Split(m.last.c.plain(), "\n")[0]
	if !strings.Contains(title, "⚠ system disk 90%") || m.toast == nil || m.toast.bad || !strings.Contains(m.toast.text, "system disk low: 3 GiB free") {
		t.Fatalf("warn: title %q toast %+v", title, m.toast)
	}
	if len(rowsWith(m, "⚠ 3 GiB free")) != 1 || fillColor(t, m, tSystemDisk) != m.theme.Warn {
		t.Fatalf("warn band:\n%s", m.last.c.plain())
	}
	m.toast = nil
	sample(m, 42, &spacetelemetry.Usage{Total: gib30, Free: 3 << 30}, data)
	if m.toast != nil {
		t.Fatalf("a level that did not change toasted again: %+v", m.toast)
	}

	sample(m, 43, &spacetelemetry.Usage{Total: gib30, Free: 3 << 29}, data)
	title = strings.Split(m.last.c.plain(), "\n")[0]
	if !strings.Contains(title, "✖ system disk almost full") || m.toast == nil || !m.toast.bad || !strings.Contains(m.toast.text, "almost full: 1.5 GiB free") {
		t.Fatalf("critical: title %q toast %+v", title, m.toast)
	}
	if fillColor(t, m, tSystemDisk) != m.theme.Bad {
		t.Fatal("critical gauge is not red")
	}

	// Recovery clears the header quietly.
	m.toast = nil
	sample(m, 44, &spacetelemetry.Usage{Total: gib30, Free: 10 << 30}, data)
	if m.toast != nil || strings.Contains(strings.Split(m.last.c.plain(), "\n")[0], "system disk") {
		t.Fatalf("recovery: toast %+v\n%s", m.toast, m.last.c.plain())
	}
	if fillColor(t, m, tSystemDisk) == m.theme.Warn {
		t.Fatal("recovered gauge kept the warning color")
	}

	// A small data disk with 3 GiB free is not flagged: its floor is 15%.
	m.toast = nil
	sample(m, 45, &spacetelemetry.Usage{Total: gib30, Free: 10 << 30}, &spacetelemetry.Usage{Total: 97 << 30 / 10, Free: 3 << 30})
	if m.toast != nil || strings.Contains(strings.Split(m.last.c.plain(), "\n")[0], "data disk") {
		t.Fatalf("small data disk false alarm: %+v", m.toast)
	}

	// The data disk follows the same rule.
	sample(m, 46, &spacetelemetry.Usage{Total: gib30, Free: 10 << 30}, &spacetelemetry.Usage{Total: 97 << 30 / 10, Free: 1 << 29})
	title = strings.Split(m.last.c.plain(), "\n")[0]
	if !strings.Contains(title, "✖ data disk almost full") || m.toast == nil || !strings.Contains(m.toast.text, "data disk almost full: 0.5 GiB free") {
		t.Fatalf("data: title %q toast %+v", title, m.toast)
	}
}

// A narrow title keeps the warning as a bare percentage instead of cutting it.
func TestDiskAlertShortensOnNarrowTitle(t *testing.T) {
	m := fixtureModel(t, 80, 30)
	withDisks(m, &spacetelemetry.Usage{Total: gib30, Free: 3 << 30}, nil)
	m.View()
	title := strings.Split(m.last.c.plain(), "\n")[0]
	if !strings.Contains(title, "⚠ system 90%") {
		t.Fatalf("title %q", title)
	}
}

// Hovering a gauge describes it in the footer; clicking opens its popup. The
// system disk's popup measures what fills it through space storage.
func TestDiskGaugesHoverAndClick(t *testing.T) {
	m := fixtureModel(t, 140, 60)
	var ran [][]string
	m.opts.Run = func(_ context.Context, args ...string) ([]byte, error) {
		ran = append(ran, args)
		return []byte(`{"measured_at":"2026-10-27T20:00:00Z","system":{"total":32212254720,"free":10737418240,"used":21474836480},
"boxes":[{"name":"opus","running":true,"declared":true,"bytes":2147483648},{"name":"api-refactor","running":true,"declared":true,"bytes":10200547328},
{"name":"old","running":false,"declared":false,"bytes":0}],
"images":{"bytes":5368709120,"layers":3221225472,"blobs":2147483648},"buildkit":null,"other":null,
"errors":[{"item":"buildkit","reason":"walk failed"}]}`), nil
	}
	m.View()
	x, y := findTargetFrom(t, m, target{tSystemDisk, ""}, 1)
	m.Update(tea.MouseMotionMsg{X: x, Y: y})
	m.View()
	footer := strings.Split(m.last.c.plain(), "\n")[m.h-1]
	if !strings.Contains(footer, "system disk") || !strings.Contains(footer, systemDiskAbout) || !strings.Contains(footer, "10 GiB free") {
		t.Fatalf("hover footer %q", footer)
	}
	cmd := click(m, x, y, tea.MouseLeft)
	if m.popup == nil || cmd == nil || !strings.Contains(m.last.c.plain(), "measuring") {
		t.Fatalf("system disk click:\n%s", m.last.c.plain())
	}
	m.Update(cmd())
	m.View()
	if want := []string{"space", "storage", "dev", "--json"}; len(ran) != 1 || !slices.Equal(ran[0], want) {
		t.Fatalf("ran %v", ran)
	}
	screen := m.last.c.plain()
	for _, want := range []string{"20 of 30 GiB", "What fills it", "api-refactor", "images", "container, stopped",
		"build cache  unavailable", "unavailable: not every part", "unavailable: buildkit: walk failed", "yoloctl space storage dev --json"} {
		if !strings.Contains(screen, want) {
			t.Errorf("popup lacks %q:\n%s", want, screen)
		}
	}
	// Largest first.
	if a, o := strings.Index(screen, "api-refactor"), strings.Index(screen, "  opus"); a < 0 || o < 0 || a > strings.Index(screen, "  images") || strings.Index(screen, "  images") > o {
		t.Errorf("rows are not sorted by size:\n%s", screen)
	}
	click(m, 0, m.h-1, tea.MouseLeft)

	x, y = findTarget(t, m, target{tDisk, ""})
	click(m, x, y, tea.MouseLeft)
	if m.popup == nil || m.popup.title != "data disk" || !strings.Contains(m.last.c.plain(), "kept across VM rebuilds") || !strings.Contains(m.last.c.plain(), "@personal") {
		t.Fatalf("data disk popup:\n%s", m.last.c.plain())
	}
}
