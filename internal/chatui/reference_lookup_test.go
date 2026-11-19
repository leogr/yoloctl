package chatui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func referenceLookupModel(t *testing.T) (*Model, *[]HistoryRequest) {
	t.Helper()
	m, _ := readyModel()
	m.Update(CatalogMsg{Generation: 2, Default: "#space", Applied: true, Channels: []ChannelInfo{
		{Name: "#space"}, {Name: "#yolo-skill"}, {Name: "#third"}, {Name: "#left"},
	}})
	for _, name := range []string{"#space", "#yolo-skill", "#third"} {
		m.Update(MembershipMsg{Channel: name, State: "joined"})
	}
	m.switchTo("#yolo-skill")
	var requests []HistoryRequest
	m.opts.LookupReference = func(r HistoryRequest) tea.Cmd {
		requests = append(requests, r)
		return nil
	}
	return m, &requests
}

func TestUnloadedReferenceQuotesExactMatchAcrossJoinedChannels(t *testing.T) {
	m, calls := referenceLookupModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(IncomingMsg{Message: Message{ID: "referrer", Channel: m.name, Nick: "peer", Text: "selected text and " + sharedMissing}})
	paste(m, "draft to preserve")
	m.follow = false
	dragOver(t, m, "referrer", 0, "selected text")
	if m.sel == nil {
		t.Fatal("expected a text highlight")
	}
	highlight, ok := m.selectedText(*m.sel)
	if !ok || strings.TrimSpace(highlight) != "selected text" {
		t.Fatalf("fixture highlighted %q", highlight)
	}
	m.View()
	cells := targetCells(m, actRef, refMissing)
	if len(cells) == 0 {
		t.Fatal("reference is not clickable")
	}
	m.Update(tea.MouseMotionMsg{X: cells[0][0], Y: cells[0][1]})
	m.View()
	if len(*calls) != 0 {
		t.Fatal("hover made a request")
	}
	scroll, anchor := m.scroll, m.anchor
	clickCell(m, cells[0])
	if len(*calls) != 1 || (*calls)[0].Channel != "#yolo-skill" || (*calls)[0].Lookup != refMissing {
		t.Fatalf("first request: %+v", *calls)
	}
	id := (*calls)[0].ID
	m.Update(ReferenceLookupMsg{ID: "stale", Channel: "#yolo-skill"})
	m.Update(ReferenceLookupMsg{ID: id, Channel: "#space"})
	if len(*calls) != 1 {
		t.Fatal("an uncorrelated result advanced the search")
	}
	m.Update(ReferenceLookupMsg{ID: id, Channel: "#yolo-skill"})
	if len(*calls) != 2 || (*calls)[1].Channel != "#space" || (*calls)[1].Lookup != refMissing {
		t.Fatalf("second request: %+v", *calls)
	}
	m.Update(ReferenceLookupMsg{ID: id, Channel: "#space", Message: Message{
		ID: refMissing, Channel: "#space", Nick: "reader", Text: "older exact message\n\x1b]52;c;inert\a",
	}})
	if len(*calls) != 2 || m.overlay == nil || m.overlay.kind != overlayReference {
		t.Fatalf("a hit should open the quote and end the search: %+v", *calls)
	}
	rendered := view(m)
	if !strings.Contains(rendered, "Message from #space") || !strings.Contains(rendered, "older exact message") ||
		strings.Contains(m.View().Content, "\x1b]52;") {
		t.Fatalf("missing or unsafe rendered quote:\n%s", rendered)
	}
	press(m, tea.KeyEscape, 0)
	if m.overlay != nil || m.name != "#yolo-skill" || m.Draft() != "draft to preserve" ||
		m.follow || !m.picked || m.selected != 0 || m.scroll != scroll || m.anchor != anchor ||
		len(m.messages) != 1 || len(m.views["#space"].messages) != 0 || m.loadingOlder || m.historyEnd || m.unread != 0 {
		t.Fatal("lookup altered the source channel or its paging state")
	}
	if m.sel == nil {
		t.Fatal("lookup discarded the source highlight")
	}
	if got, ok := m.selectedText(*m.sel); !ok || got != highlight {
		t.Fatalf("highlight after lookup is %q, want %q", got, highlight)
	}
	m.quoteSelection()
	if !strings.Contains(m.Draft(), "> selected text") {
		t.Fatalf("highlight is no longer available to Quote: %q", m.Draft())
	}
}

func TestReferenceSearchCancelsWhenChannelPickerOpens(t *testing.T) {
	m, calls := referenceLookupModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.showMessage(refMissing)
	m.View()
	for _, h := range m.hits {
		if h.area == hitTop && h.id == "channels" {
			clickCell(m, [2]int{h.x0, h.y})
			break
		}
	}
	if len(*calls) != 1 || m.overlay == nil || m.overlay.kind != overlayChannels || m.reference != nil {
		t.Fatalf("picker did not cancel the request: %+v", *calls)
	}
	r := (*calls)[0]
	m.Update(ReferenceLookupMsg{ID: r.ID, Channel: r.Channel})
	m.Update(ReferenceLookupMsg{ID: r.ID, Channel: r.Channel, Message: Message{ID: r.Lookup, Channel: r.Channel}})
	if len(*calls) != 1 || m.overlay == nil || m.overlay.kind != overlayChannels {
		t.Fatal("a canceled result advanced the lookup or displaced the picker")
	}
	press(m, tea.KeyEscape, 0)
	if m.overlay != nil || strings.Contains(m.notice, "Looking up") {
		t.Fatalf("picker left stale progress: %q", m.notice)
	}
}

func TestReferenceSearchMissesOnlyAcrossCapturedChannels(t *testing.T) {
	m, calls := referenceLookupModel(t)
	m.showMessage(refMissing)
	m.Update(MembershipMsg{Channel: "#left", State: "joined"})
	for i, name := range []string{"#yolo-skill", "#space", "#third"} {
		if len(*calls) != i+1 || (*calls)[i].Channel != name {
			t.Fatalf("query %d: %+v", i, *calls)
		}
		if strings.Contains(m.notice, "no longer retained") {
			t.Fatal("claimed absence before all misses")
		}
		m.Update(ReferenceLookupMsg{ID: (*calls)[i].ID, Channel: name})
	}
	if len(*calls) != 3 || m.overlay != nil || !strings.Contains(m.notice, "no longer retained in the joined channels") {
		t.Fatalf("all misses: %+v, %q", *calls, m.notice)
	}
}

func TestReferenceSearchStopsOnFailureOrInvalidatedSource(t *testing.T) {
	for _, test := range []struct {
		name string
		end  func(*Model, HistoryRequest)
	}{
		{"refused", func(m *Model, r HistoryRequest) {
			m.Update(ReferenceLookupMsg{ID: r.ID, Channel: r.Channel, Err: errors.New("request refused")})
		}},
		{"wrong ID", func(m *Model, r HistoryRequest) {
			m.Update(ReferenceLookupMsg{ID: r.ID, Channel: r.Channel, Message: Message{ID: refTarget, Channel: r.Channel}})
		}},
		{"switch and return", func(m *Model, _ HistoryRequest) {
			m.switchTo("#space")
			m.switchTo("#yolo-skill")
		}},
		{"source leaves", func(m *Model, r HistoryRequest) {
			m.Update(MembershipMsg{Channel: r.Channel, State: "left"})
		}},
		{"disconnect", func(m *Model, _ HistoryRequest) {
			m.Update(ConnectionMsg{})
		}},
		{"next channel left", func(m *Model, r HistoryRequest) {
			m.Update(MembershipMsg{Channel: "#space", State: "left"})
			m.Update(ReferenceLookupMsg{ID: r.ID, Channel: r.Channel})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, calls := referenceLookupModel(t)
			m.showMessage(refMissing)
			if len(*calls) != 1 {
				t.Fatalf("first request: %+v", *calls)
			}
			r := (*calls)[0]
			test.end(m, r)
			m.Update(ReferenceLookupMsg{ID: r.ID, Channel: r.Channel, Message: Message{ID: refMissing, Channel: r.Channel}})
			if len(*calls) != 1 || m.reference != nil || m.overlay != nil || strings.Contains(m.notice, "no longer retained") {
				t.Fatalf("failed lookup leaked or claimed absence: %+v, %q", *calls, m.notice)
			}
		})
	}
}
