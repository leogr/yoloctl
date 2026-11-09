package chatui

import "github.com/charmbracelet/x/ansi"

// setWidthMethod discards display coordinates from the old method. Body lines
// contain table padding and expanded tabs, so discarding wrapped rows alone
// would leave stale byte positions, including in channels that are not visible.
func (m *Model) setWidthMethod(method ansi.Method) {
	if m.widthMethod == method {
		return
	}
	m.widthMethod = method
	m.eachView(func() {
		m.captureAnchor()
		if m.anchor != nil {
			m.anchor.offset = 0 // retain the message/logical line, not old padding
		}
		m.sel = nil
		m.bodyCache = map[string]cachedBody{}
		m.built.rows, m.lastTranscript, m.draft.rows = nil, nil, nil
		m.editor.column = nil
		m.touch()
	})
	// Cancel every pending gesture/tick. Its eventual release sees no active
	// press, so it cannot copy a selection or activate a moved target.
	m.gesture++
	m.drag, m.hover = dragState{}, hoverKey{}
	m.marquee = marquee{gen: m.marquee.gen + 1}
	m.hits, m.layout = nil, viewLayout{}
	if m.linkPrompt != nil {
		m.linkPrompt.scroll = 0
	}
	// Update can receive another mouse event before the framework paints.
	// Publish fresh coordinates now, without waiting for a window resize.
	m.View()
}
