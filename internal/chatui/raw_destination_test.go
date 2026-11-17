package chatui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func TestChatRejectsRawControlDestinationBeforeAdmission(t *testing.T) {
	m, _, _ := localLinkModel(t)
	var run func(tea.Cmd)
	run = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		switch result := cmd().(type) {
		case tea.BatchMsg:
			for _, child := range result {
				run(child)
			}
		case linkAdmissionMsg:
			m.receiveFileLinks(result)
		}
	}
	_, cmd := m.Update(IncomingMsg{Message: Message{ID: "raw-control", Text: "[label](a\x00.md)"}})
	run(cmd)
	m.View()
	if cmd := m.settleFileLinks(); cmd != nil {
		m.receiveFileLinks(cmd().(linkAdmissionMsg))
	}
	body := m.body(&m.messages[len(m.messages)-1], 60)
	for _, line := range body.lines {
		for _, piece := range line.Pieces {
			if piece.Act.Kind == markdownview.IntentLocalFile {
				t.Fatalf("raw control destination became clickable after sanitization: %+v", piece.Act)
			}
		}
	}
}
