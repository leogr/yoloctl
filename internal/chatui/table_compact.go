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
	closed := false
	for _, row := range body.rows {
		for _, run := range row.Runs {
			if run.Act.Kind == markdownview.IntentToggleTableRow && run.Act.Value == act.Value {
				valid = true
				closed = row.Closed
			}
		}
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
	if !closed {
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
