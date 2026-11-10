package chatui

import (
	"fmt"
	"slices"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// channelStrip reserves one row for navigation, regardless of channel count.
// A sliding window preserves channel order; overflow opens the picker.
func (m *Model) channelStrip(width, y int) string {
	current := max(slices.Index(m.order, m.name), 0)
	var totalUnread, totalMentions int
	for _, name := range m.order {
		totalUnread += m.views[name].unread
		totalMentions += m.views[name].unreadMentions
	}
	overflow := func(hidden, unread, mentions int) string {
		label := fmt.Sprintf("+%d", hidden)
		if mentions > 0 {
			label += fmt.Sprintf(" @%d", mentions)
		}
		if unread > 0 {
			label += fmt.Sprintf(" %d", unread)
		}
		return label
	}
	// Short channel sets need no overflow budget or abbreviation.
	var full []run
	var fullTargets []hit
	fullWidth := 0
	for i, name := range m.order {
		v := m.views[name]
		label := name
		if v.unreadMentions > 0 {
			label += fmt.Sprintf(" @%d", v.unreadMentions)
		}
		if v.unread > 0 {
			label += fmt.Sprintf(" %d", v.unread)
		}
		if i < len(m.order)-1 {
			label += "  "
		}
		style := spaceview.Style{FG: m.theme.Room, Bold: v.unread > 0}
		if v == m.cur {
			style.BG, style.Bold = m.theme.Raised, true
		}
		full = append(full, m.hoverRun(deco(label, style), m.hovered(hitChannel, i, "")))
		next := fullWidth + m.widthMethod.StringWidth(label)
		fullTargets = append(fullTargets, hit{x0: fullWidth, x1: next, y: y, area: hitChannel, item: i})
		fullWidth = next
	}
	if fullWidth <= width {
		m.hits = append(m.hits, fullTargets...)
		return paint(m.widthMethod, full, width, 0, nil, spaceview.Style{})
	}
	// Reserve enough room for the overflow count and all unread indicators.
	reserve := min(m.widthMethod.StringWidth(overflow(len(m.order), totalUnread, totalMentions))+2, width/2)
	var runs []run
	var targets []hit
	x, shown := 0, 0
	unread, mentions := totalUnread, totalMentions
	for start := 0; start <= current; start++ {
		runs, targets = nil, nil
		x, shown = 0, 0
		unread, mentions = totalUnread, totalMentions
		for i := start; i < len(m.order); i++ {
			name := m.order[i]
			v := m.views[name]
			badge := ""
			if v.unreadMentions > 0 {
				badge += fmt.Sprintf(" @%d", v.unreadMentions)
			}
			if v.unread > 0 {
				badge += fmt.Sprintf(" %d", v.unread)
			}
			nameMin := min(m.widthMethod.StringWidth(name), 4)
			budget := min(max(width/3, 9, m.widthMethod.StringWidth(badge)+nameMin+2), 24, width-reserve-x)
			if shown > 0 && budget-m.widthMethod.StringWidth(badge)-2 < nameMin {
				break // fold an unreadable stub into the overflow picker
			}
			if budget < m.widthMethod.StringWidth(badge)+3 {
				if shown > 0 || budget < 3 {
					break
				}
				badge = truncate(m.widthMethod, badge, budget-3, "…")
			}
			label := truncate(m.widthMethod, name, budget-m.widthMethod.StringWidth(badge)-2, "…") + badge + "  "
			style := spaceview.Style{FG: m.theme.Room, Bold: v.unread > 0}
			if v == m.cur {
				style.BG, style.Bold = m.theme.Raised, true
			}
			runs = append(runs, m.hoverRun(deco(label, style), m.hovered(hitChannel, i, "")))
			next := x + m.widthMethod.StringWidth(label)
			targets = append(targets, hit{x0: x, x1: next, y: y, area: hitChannel, item: i})
			x = next
			shown++
			unread -= v.unread
			mentions -= v.unreadMentions
		}
		if current < start+shown {
			break
		}
	}
	m.hits = append(m.hits, targets...)
	if shown < len(m.order) {
		label := truncate(m.widthMethod, overflow(len(m.order)-shown, unread, mentions), max(width-x, 0), "…")
		runs = append(runs, m.hoverRun(deco(label, spaceview.Style{FG: m.theme.Brand, Bold: true}), m.hovered(hitTop, 0, "channels")))
		m.hits = append(m.hits, hit{x0: x, x1: x + m.widthMethod.StringWidth(label), y: y, area: hitTop, id: "channels"})
	}
	return paint(m.widthMethod, runs, width, 0, nil, spaceview.Style{})
}
