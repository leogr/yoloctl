package spaceview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

func memStat(anon, file, shmem, inactive uint64) *spacetelemetry.MemStat {
	return &spacetelemetry.MemStat{Anon: ptr(anon), File: ptr(file), Shmem: ptr(shmem), InactiveFile: ptr(inactive)}
}

// setBoxMemory replaces one box's memory counters in s.
func setBoxMemory(s *spacetelemetry.Sample, name string, current, max uint64, stat *spacetelemetry.MemStat) {
	b := s.Boxes[name]
	b.MemCurrent, b.MemMax, b.MemStat = current, max, stat
	s.Boxes[name] = b
}

// memCells counts the bar cells of one box's memory row by color.
func memCells(m *Model, box string) map[Color]int {
	m.View()
	out := map[Color]int{}
	for y, row := range m.last.c.cells {
		for x, c := range row {
			if tg, ok := m.last.targetAt(x, y); ok && tg == (target{tBoxMem, box}) && c.text == "█" {
				out[c.style.FG]++
			}
		}
	}
	return out
}

func TestWorkingEstimate(t *testing.T) {
	for _, tc := range []struct {
		name string
		bm   boxMemory
		want uint64
		ok   bool
	}{
		{"estimate", boxMemory{Current: 1900, Stat: memStat(100, 1700, 0, 1600)}, 300, true},
		{"valid zero inactive", boxMemory{Current: 1900, Stat: memStat(1900, 0, 0, 0)}, 1900, true},
		{"separate reads raced", boxMemory{Current: 100, Stat: memStat(0, 200, 0, 150)}, 0, true},
		{"no breakdown", boxMemory{Current: 1900}, 0, false},
		{"inactive_file absent", boxMemory{Current: 1900, Stat: &spacetelemetry.MemStat{Anon: ptr(uint64(5))}}, 0, false},
	} {
		if got, ok := tc.bm.working(); got != tc.want || ok != tc.ok {
			t.Errorf("%s: working = %d %v, want %d %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// The bar's extent is the raw charge against the box's cap; the estimate is
// in the box's accent and the inactive file pages it leaves out are muted.
func TestCardShowsEstimateBesideTotal(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	setBoxMemory(s, "api-refactor", 1946<<20, 2<<30, memStat(250<<20, 1690<<20, 10<<20, 1646<<20))
	applyFixtureSample(m, s)
	if row := screenLine(t, m, "~300M"); !strings.Contains(row, "~300M work · 1.9G/2G cap") {
		t.Fatalf("card label = %q", row)
	}
	// 7 cells of a 2 GiB cap: 300 MiB rounds to 1, the 1.9 GiB total to 7.
	cells := memCells(m, "api-refactor")
	if cells[m.accent("api-refactor")] != 1 || cells[m.theme.Dim] != 6 || len(cells) != 2 {
		t.Fatalf("bar cells = %v", cells)
	}
	// The raw charge and the cap stay in the details.
	m.openBoxMemory("api-refactor")
	m.View()
	screen := m.last.c.plain()
	for _, want := range []string{"Total", "1.9G", fmt.Sprintf("%d bytes", uint64(1946<<20)), "Limit", "2G", "caps the total, not the estimate", "~300M", "total − inactive_file"} {
		if !strings.Contains(screen, want) {
			t.Errorf("details lack %q:\n%s", want, screen)
		}
	}
}

// Without a breakdown the bar is neutral and shows the total alone.
func TestCardWithoutBreakdownShowsTotalOnly(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	cells := memCells(m, "api-refactor")
	if len(cells) != 1 || cells[m.theme.Label] == 0 {
		t.Fatalf("bar cells = %v", cells)
	}
	if row := screenLine(t, m, "1.4G"); !strings.Contains(row, "1.4G of 14G pool") || strings.Contains(row, "~") {
		t.Fatalf("card label = %q", row)
	}
}

// Overlapping categories are listed, never stacked or summed, and file pages
// are not all called reclaimable.
func TestDetailsKeepOverlappingCategoriesApart(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	// 1 GiB of shmem is part of 1.5 GiB of file; only 100 MiB is inactive.
	setBoxMemory(s, "api-refactor", 2560<<20, 0, memStat(1<<30, 1536<<20, 1<<30, 100<<20))
	applyFixtureSample(m, s)
	m.openBoxMemory("api-refactor")
	m.View()
	screen := m.last.c.plain()
	for _, want := range []string{"~2.4G", "inactive_file", "100M", "anon", "file", "1.5G", "shmem", "part of file", "categories overlap", "shares the workload pool's 14G limit"} {
		if !strings.Contains(screen, want) {
			t.Errorf("details lack %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, " reclaimable") && !strings.Contains(screen, "may not be reclaimable") {
		t.Errorf("details call memory reclaimable:\n%s", screen)
	}
	// The card bar still spans only the raw total over the pool.
	m.popup = nil
	cells := memCells(m, "api-refactor")
	if cells[m.accent("api-refactor")]+cells[m.theme.Dim] == 0 {
		t.Fatalf("bar cells = %v", cells)
	}
}

// An unread breakdown leaves the raw charge, with no estimate.
func TestUnreadBreakdownKeepsTheTotal(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	b := s.Boxes["api-refactor"]
	b.Unavailable = append(b.Unavailable, spacetelemetry.SignalMemStat)
	s.Boxes["api-refactor"] = b
	applyFixtureSample(m, s)
	if row := screenLine(t, m, "1.4G"); strings.Contains(row, "~") {
		t.Fatalf("estimate without a breakdown: %q", row)
	}
	m.openBoxMemory("api-refactor")
	m.View()
	if screen := m.last.c.plain(); !strings.Contains(screen, "memory.stat could not be read") || !strings.Contains(screen, "no estimate") {
		t.Fatalf("details:\n%s", screen)
	}
}

// An exited session can leave a running container with real processes: its
// memory and CPU stay current readings.
func TestExitedSessionKeepsContainerReadings(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	setBoxMemory(s, "web-preview", 2<<30, 0, memStat(1<<30, 1<<30, 0, 512<<20))
	applyFixtureSample(m, s)
	if row := screenLine(t, m, "~1.5G"); !strings.Contains(row, "2G/14G pool") {
		t.Fatalf("exited card = %q", row)
	}
	screenLine(t, m, "0.69/2 cpu") // a current CPU reading, not "…"
	m.openBoxMemory("web-preview")
	m.View()
	if screen := m.last.c.plain(); !strings.Contains(screen, "session exited · container running") || !strings.Contains(screen, "~1.5G") {
		t.Fatalf("details:\n%s", screen)
	}
}

// Memory is instantaneous: a matching container's first sample shows it while
// CPU still waits for a second one.
func TestMemoryNeedsNoRatedSample(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	first := New(t.Context(), m.opts)
	first.w, first.h, first.now = 132, 50, fixtureStart
	first.conn.state = connLive
	first.d.applyInventory(fixtureStart, fixtureInventory())
	first.d.applySample(fixtureStart, fixtureSample(1))
	first.sampleSeen = first.now
	if live := first.d.boxes["conductor"]; live.Rated {
		t.Fatal("rated from one sample")
	}
	if row := screenLine(t, first, "610M"); !strings.Contains(row, "MEM") {
		t.Fatalf("first sample memory = %q", row)
	}
}

// Valid readings sort by raw total, unavailable ones follow, and ties keep
// name order. The estimate never changes the order.
func TestMemorySortUsesCurrentTotals(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	setBoxMemory(s, "conductor", 500<<20, 0, nil)
	setBoxMemory(s, "docs-bot", 500<<20, 2<<30, nil)
	// The largest total with the smallest estimate still sorts first.
	setBoxMemory(s, "web-preview", 3<<30, 0, memStat(0, 3<<30, 0, 3<<30))
	s.Boxes["room"] = spacetelemetry.BoxSample{Error: "box has no running init process"}
	applyFixtureSample(m, s)
	m.sortMode = sortMemory
	want := []string{"web-preview", "api-refactor", "conductor", "docs-bot", "room", "scratch"}
	if got := names(m.visibleBoxes()); !slices.Equal(got, want) {
		t.Fatalf("memory sort = %v, want %v", got, want)
	}
}

// The pool's charge is labeled as including cache and stands apart from the
// VM fill; without the pool a shared box names the VM scale it is drawn on.
func TestPoolChargeStandsApart(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	if row := screenLine(t, m, "MEM  ▕"); !strings.Contains(row, "9/16 GiB used") || !strings.Contains(row, "pool 7/14 GiB incl. cache") {
		t.Fatalf("VM row = %q", row)
	}
	s := fixtureSample(41)
	s.Pool, s.PoolError = nil, "missing workload OOM counter"
	applyFixtureSample(m, s)
	if row := screenLine(t, m, "MEM  ▕"); !strings.Contains(row, "9/16 GiB used") || !strings.Contains(row, "pool unavailable") {
		t.Fatalf("VM row without pool = %q", row)
	}
	if row := screenLine(t, m, "1.4G"); !strings.Contains(row, "1.4G of 16G VM") {
		t.Fatalf("shared card without pool = %q", row)
	}
}

// An OOM kill is reported for the pool's boxes and builds, never as the
// pool's own cap or one box's.
func TestOOMToastStaysAggregate(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	s := fixtureSample(41)
	s.Pool.OOMKills = 1
	events := m.d.applySample(fixtureStart.Add(41*time.Second), s)
	if len(events) != 1 || events[0].box != "" || !strings.Contains(events[0].text, "OOM kill in a box or build") {
		t.Fatalf("events = %+v", events)
	}
}

// Stale reasons replace the readings on the card and in the details.
func TestStaleMemoryNamesTheReason(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.paused = true
	if row := screenLine(t, m, "MEM  "); !strings.Contains(row, "no current reading: updates paused") {
		t.Fatalf("paused VM row = %q", row)
	}
	m.View()
	if !strings.Contains(m.last.c.plain(), "MEM ") || strings.Count(m.last.c.plain(), "updates paused") < 2 {
		t.Fatalf("paused cards:\n%s", m.last.c.plain())
	}
	m.paused = false
	m.openBoxMemory("api-refactor")
	m.paused = true
	m.View()
	if screen := m.last.c.plain(); !strings.Contains(screen, "no current reading") || strings.Contains(screen, "1.4G") {
		t.Fatalf("paused details:\n%s", screen)
	}
}

// Clicking a memory row opens its live details; the rest of the card still
// attaches.
func TestClickMemoryRowOpensDetails(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.View()
	x, y := findTarget(t, m, target{tBoxMem, "conductor"})
	click(m, x, y, tea.MouseLeft)
	if m.popup == nil || m.popup.kind != "box memory" || m.popup.title != "conductor" {
		t.Fatalf("popup = %+v", m.popup)
	}
}

// Breakdown labels fit every layout width.
func TestMemoryLabelsFitEveryWidth(t *testing.T) {
	for _, size := range sizes {
		m := fixtureModel(t, size[0], size[1])
		s := fixtureSample(41)
		for name, b := range s.Boxes {
			setBoxMemory(s, name, b.MemCurrent, b.MemMax, memStat(b.MemCurrent/4, b.MemCurrent/2, 1<<20, b.MemCurrent/3))
		}
		applyFixtureSample(m, s)
		m.View()
		for i, row := range strings.Split(m.last.c.render(), "\n") {
			if w := ansi.StringWidthWc(row); w != size[0] {
				t.Errorf("%dx%d row %d is %d columns: %q", size[0], size[1], i, w, ansi.Strip(row))
			}
		}
	}
}
