package spaceview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// These regressions use only the fixture and the rendered screen, so they
// also run against the view before its memory rework.

// applyFixtureSample applies s as the 41st sample and keeps it current.
func applyFixtureSample(m *Model, s *spacetelemetry.Sample) {
	m.d.applySample(fixtureStart.Add(41*time.Second), s)
	m.sampleSeen = m.now
}

func screenLine(t *testing.T, m *Model, needle string) string {
	t.Helper()
	m.View()
	for _, row := range strings.Split(m.last.c.plain(), "\n") {
		if strings.Contains(row, needle) {
			return row
		}
	}
	t.Fatalf("no row contains %q:\n%s", needle, m.last.c.plain())
	return ""
}

// The VM fill uses only the numerator of its used/total label. Box charges
// include file cache that MemAvailable counts as available, so stacking them
// overfilled the bar past its own label.
func TestVMMemoryFillMatchesItsLabel(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	s.MemTotal, s.MemAvailable = 8<<30, 5632<<20 // 2.5 GiB used
	s.Pool.Current, s.Pool.Max = 3277<<20, 7<<30
	for name, mem := range map[string]uint64{"api-refactor": 2662 << 20, "conductor": 200 << 20, "docs-bot": 100 << 20, "room": 50 << 20, "web-preview": 60 << 20} {
		b := s.Boxes[name]
		b.MemCurrent = mem
		s.Boxes[name] = b
	}
	applyFixtureSample(m, s)
	row := screenLine(t, m, "MEM  ▕")
	if !strings.Contains(row, "2.5/8 GiB") {
		t.Fatalf("VM label = %q", row)
	}
	_, bar, _ := strings.Cut(row, "MEM  ▕")
	bar, _, _ = strings.Cut(bar, "▏")
	// barW is 48 at this width: 2.5/8 of it.
	if filled := strings.Count(bar, "█"); filled != 15 {
		t.Fatalf("VM fill %d cells, want 15 for 2.5/8 GiB: %q", filled, row)
	}
}

// A box whose sample failed has no current memory: the last charge must not
// stay on a compact row or keep its place in the memory sort.
func TestErroredBoxMemoryIsNotCurrent(t *testing.T) {
	m := fixtureModel(t, 40, 40)
	s := fixtureSample(41)
	s.Boxes["api-refactor"] = spacetelemetry.BoxSample{Error: "box has no running init process"}
	applyFixtureSample(m, s)
	if row := screenLine(t, m, "api-refactor"); strings.Contains(row, "1.4G") {
		t.Fatalf("errored box shows its old charge: %q", row)
	}
	m.sortMode = sortMemory
	order := names(m.visibleBoxes())
	if slices.Index(order, "api-refactor") < slices.Index(order, "room") {
		t.Fatalf("errored box sorted by its old charge: %v", order)
	}
}

// A stopped container keeps no memory place in the sort.
func TestStoppedBoxDoesNotSortByOldMemory(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	inv := fixtureInventory()
	for i := range inv.Boxes {
		if inv.Boxes[i].Name == "web-preview" {
			inv.Boxes[i].State, inv.Boxes[i].Runtime, inv.Boxes[i].Session = "stopped", "stopped", "none"
		}
	}
	m.d.applyInventory(fixtureStart.Add(41*time.Second), inv)
	s := fixtureSample(41)
	delete(s.Boxes, "web-preview")
	applyFixtureSample(m, s)
	m.sortMode = sortMemory
	want := []string{"api-refactor", "conductor", "docs-bot", "room", "scratch", "web-preview"}
	if got := names(m.visibleBoxes()); !slices.Equal(got, want) {
		t.Fatalf("memory sort = %v, want %v", got, want)
	}
}

// A stalled, paused or lost stream shows no memory as current, in cards or
// on the VM row.
func TestStaleStreamMemoryIsNotCurrent(t *testing.T) {
	for name, stale := range map[string]func(m *Model){
		"stalled": func(m *Model) { m.now = m.sampleSeen.Add(10 * time.Second) },
		"paused":  func(m *Model) { m.paused = true },
		"lost":    func(m *Model) { m.conn.state = connLost },
	} {
		m := fixtureModel(t, 132, 50)
		stale(m)
		m.View()
		screen := m.last.c.plain()
		for _, old := range []string{"1.4G", "610M", "9/16 GiB"} {
			if strings.Contains(screen, old) {
				t.Errorf("%s stream still shows %q as current:\n%s", name, old, screen)
			}
		}
	}
}

// A full charge near a box's cap is not pressure: no heat colors.
func TestNearCapMemoryIsNotPaintedAsPressure(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	b := s.Boxes["docs-bot"]
	b.MemCurrent = 2007 << 20 // of a 2 GiB cap
	s.Boxes["docs-bot"] = b
	applyFixtureSample(m, s)
	m.View()
	for y, row := range m.last.c.cells {
		for x, c := range row {
			if c.text == "█" && c.style.FG == m.theme.Heat[3] {
				t.Fatalf("hottest heat color at %d,%d", x, y)
			}
		}
	}
}

// An unreadable /proc/meminfo is unavailable, not "waiting".
func TestUnreadVMMemoryIsUnavailable(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	s.MemTotal, s.MemAvailable, s.Error = 0, 0, "incomplete /proc/meminfo"
	applyFixtureSample(m, s)
	m.View()
	screen := m.last.c.plain()
	if strings.Contains(screen, "waiting for the first sample") || !strings.Contains(screen, "VM memory unavailable") {
		t.Fatalf("unread meminfo:\n%s", screen)
	}
}

// An unread box limit is unknown, never the shared pool.
func TestUnreadBoxLimitIsNotShared(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	b := s.Boxes["api-refactor"]
	b.Unavailable = append(b.Unavailable, "mem_max")
	s.Boxes["api-refactor"] = b
	applyFixtureSample(m, s)
	m.View()
	screen := m.last.c.plain()
	if strings.Contains(screen, "shared") || !strings.Contains(screen, "limit unknown") {
		t.Fatalf("unread limit:\n%s", screen)
	}
}
