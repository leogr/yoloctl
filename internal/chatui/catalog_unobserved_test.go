package chatui

import (
	"strings"
	"testing"
)

func TestCatalogUnobservedNeverClaimsPendingOrApplied(t *testing.T) {
	m, _ := readyModel()
	m.openOverlay(overlayState{kind: overlayChannels})
	for _, generation := range []uint64{1, 1, 2} {
		m.Update(CatalogMsg{Generation: generation, Applied: true})
		if !m.catalogApplied {
			t.Fatal("lost observed proof")
		}
		// Even an inconsistent incoming applied bit cannot retain stale proof.
		m.Update(CatalogMsg{Generation: generation, Applied: true, RuntimeUnobserved: true})
		text := strings.Join(m.channelPickerRows(100), "\n")
		if !m.catalogUnobserved || m.catalogApplied || !strings.Contains(text, "Server application not checked; use chat status") || strings.Contains(text, "has not loaded") {
			t.Fatal("unobserved became pending/applied", text)
		}
		m.Update(CatalogMsg{Generation: generation})
		text = strings.Join(m.channelPickerRows(100), "\n")
		if m.catalogUnobserved || !strings.Contains(text, "has not loaded") {
			t.Fatal("observed pending behavior changed", text)
		}
	}
}

func TestUnobservedCatalogPreservesRefreshOwnershipAndDraft(t *testing.T) {
	m, _ := readyModel()
	paste(m, "keep this draft")
	before := m.editor
	refreshFailure(m, 2, 3, "catalog unavailable")
	m.Update(CatalogMsg{Generation: 7, RuntimeUnobserved: true, Read: CatalogRead{1, 4}})
	if m.notice == "" || m.catalogUnobserved {
		t.Fatal("old link changed the current view")
	}
	m.Update(CatalogMsg{Generation: 7, RuntimeUnobserved: true, Read: CatalogRead{2, 4}})
	if m.notice != "" || !m.catalogUnobserved || m.editor.text != before.text || m.editor.cursor != before.cursor {
		t.Fatal("accepted recovery changed draft/cursor or lost unknown state")
	}
}
