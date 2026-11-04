package spaceview

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// These regressions name containers only through the frame encoding, as the
// stream does, so they also run against a view that ignores the identity.

// encodedInventory is the fixture inventory as the stream would send it,
// with each running box's container ID and api-refactor's set by apiID
// ("" leaves it out: an unknown identity).
func encodedInventory(t *testing.T, apiID string) *spacetelemetry.Inventory {
	t.Helper()
	raw, err := json.Marshal(fixtureInventory())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, b := range doc["boxes"].([]any) {
		box := b.(map[string]any)
		if box["runtime"] == "running" {
			box["container"] = "c-" + box["name"].(string)
		}
		if box["name"] == "api-refactor" {
			delete(box, "container")
			if apiID != "" {
				box["container"] = apiID
			}
		}
	}
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	var inv spacetelemetry.Inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	return &inv
}

// The stream sends a replaced box's inventory before its next sample: until
// then the last sample is the old container's, and its charge, limit,
// estimate and sort place must not pass for the new one's.
func TestReplacementInventoryHidesTheOldContainer(t *testing.T) {
	for _, apiID := range []string{"c-new", ""} {
		m := fixtureModel(t, 132, 50)
		s := fixtureSample(41)
		setBoxMemory(s, "api-refactor", 1946<<20, 3<<30, memStat(250<<20, 1690<<20, 10<<20, 1646<<20))
		applyFixtureSample(m, s)
		screenLine(t, m, "~300M") // the old container's reading, while current
		m.d.applyInventory(fixtureStart.Add(42*time.Second), encodedInventory(t, apiID))
		m.View()
		for _, old := range []string{"~300M", "1.9G", "3G cap"} {
			if strings.Contains(m.last.c.plain(), old) {
				t.Errorf("container %q: card shows the old container's %q:\n%s", apiID, old, m.last.c.plain())
			}
		}
		m.sortMode = sortMemory
		if order := names(m.visibleBoxes()); slices.Index(order, "api-refactor") < slices.Index(order, "room") {
			t.Errorf("container %q: sorted by the old container's charge: %v", apiID, order)
		}
		m.openBoxMemory("api-refactor")
		m.View()
		if screen := m.last.c.plain(); strings.Contains(screen, "1.9G") || strings.Contains(screen, "~300M") || !strings.Contains(screen, "no current reading") {
			t.Errorf("container %q: details:\n%s", apiID, screen)
		}
		m.popup = nil
		m.w, m.h = 40, 40
		if row := screenLine(t, m, "api-refactor"); strings.Contains(row, "1.9G") {
			t.Errorf("container %q: compact row = %q", apiID, row)
		}
	}
}

// The new container's first sample shows its memory at once, while its CPU
// waits for a second sample of the same container.
func TestReplacementShowsItsFirstSample(t *testing.T) {
	m := fixtureModel(t, 132, 50)
	m.d.applyInventory(fixtureStart.Add(41*time.Second), encodedInventory(t, "c-new"))
	s := fixtureSample(42)
	b := s.Boxes["api-refactor"]
	b.Container, b.MemCurrent = "c-new", 300<<20
	s.Boxes["api-refactor"] = b
	m.d.applySample(fixtureStart.Add(42*time.Second), s)
	m.sampleSeen = m.now
	if live := m.d.boxes["api-refactor"]; live.Rated {
		t.Fatal("rated across a replaced container")
	}
	if row := screenLine(t, m, "300M"); !strings.Contains(row, "MEM") {
		t.Fatalf("replacement memory = %q", row)
	}
}
