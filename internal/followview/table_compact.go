package followview

import (
	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/markdownview"
)

type followTableKey struct {
	Table, Columns, Occurrence int
	First                      string
}

func followKey(r markdownview.TableRecord) followTableKey {
	return followTableKey{r.ID.Table, r.Columns, r.Occurrence, r.First}
}
func (m *Model) toggleTableRows(value string, all bool) tea.Cmd {
	if m.view == nil || !m.view.markdown || m.source || len(m.view.controls) == 0 {
		return nil
	}
	if m.openTables == nil {
		m.openTables = map[followTableKey]bool{}
	}
	if all {
		expand := true
		for _, record := range m.view.records {
			if !m.view.controls[record.ID] {
				continue
			}
			if m.openTables[followKey(record)] {
				expand = false
				break
			}
		}
		for _, record := range m.view.records {
			if !m.view.controls[record.ID] {
				continue
			}
			key := followKey(record)
			if expand {
				m.openTables[key] = true
			} else {
				delete(m.openTables, key)
			}
		}
	} else {
		id, ok := markdownview.ParseTableRowID(value)
		if !ok || !m.view.controls[id] {
			return nil
		}
		found := false
		for _, record := range m.view.records {
			if record.ID == id {
				key := followKey(record)
				if m.openTables[key] {
					delete(m.openTables, key)
				} else {
					m.openTables[key] = true
				}
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	m.pause()
	m.captureAnchor()
	m.clearSelection()
	m.notice = ""
	m.gen++
	return m.work(false, true)
}
