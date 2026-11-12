package chatui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func (m *Model) toggleTableRow(act action) tea.Cmd {
	id, ok := markdownview.ParseTableRowID(act.Value)
	index := m.indexOf(act.Extra)
	if !ok || index < 0 || m.source {
		return nil
	}
	body := m.body(&m.messages[index], max(m.lastTextWidth, 1))
	valid := false
	for _, r := range markdownview.TableRecords(markdownview.Document{Lines: body.lines}) {
		valid = valid || r.ID == id
	}
	if !valid {
		return nil
	}
	m.captureAnchor()
	if m.tableRows == nil {
		m.tableRows = map[string]map[markdownview.TableRowID]bool{}
	}
	if m.tableRows[act.Extra] == nil {
		m.tableRows[act.Extra] = map[markdownview.TableRowID]bool{}
	}
	state := m.tableRows[act.Extra]
	if state[id] {
		delete(state, id)
	} else {
		state[id] = true
	}
	m.sel = nil
	m.drag = dragState{}
	body.width = -1
	m.bodyCache[act.Extra] = body
	m.touch()
	m.View()

	return nil
}
