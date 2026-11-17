package chatui

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/linkview"
	"github.com/leogr/yoloctl/internal/markdownview"
)

type linkCacheKey struct{ channel, key, text string }
type linkAdmissionMsg struct {
	key          linkCacheKey
	view         *channelView
	admissions   filelink.Admissions
	epoch        uint64
	destinations []string
}
type fileOpenedMsg struct {
	key    linkCacheKey
	view   *channelView
	target filelink.Target
	err    error
	epoch  uint64
}
type linkJobs struct {
	mu     sync.Mutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (j *linkJobs) run(fn func(context.Context) tea.Msg) tea.Msg {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.wg.Add(1)
	j.mu.Unlock()
	defer j.wg.Done()
	return fn(j.ctx)
}
func (m *Model) CloseLinks() {
	if m.linkJobs == nil {
		return
	}
	j := m.linkJobs
	j.mu.Lock()
	j.closed = true
	j.cancel()
	j.mu.Unlock()
	j.wg.Wait()
}
func (m *Model) linkPolicy(key, text string) markdownview.LinkPolicy {
	if m.opts.Navigation == nil && !m.opts.LinkActions {
		return nil
	}
	a := m.fileLinks[linkCacheKey{m.name, key, text}]
	return linkview.Policy(a, text)
}
func (m *Model) settleFileLinks() tea.Cmd {
	if m.opts.Navigation == nil || m.linksBusy || m.source || m.layout.transcriptWidth == 0 || m.lastBodyHeight == 0 {
		return nil
	}
	rows := m.buildTranscript(m.layout.transcriptWidth)
	m.place(rows, m.lastBodyHeight)
	visible := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, r := range rows[min(m.scroll, len(rows)):min(m.scroll+m.lastBodyHeight, len(rows))] {
		for _, run := range r.runs {
			a := run.Act
			if a.Kind != markdownview.IntentNone || a.Value == "" {
				continue
			}
			if seen[r.key] == nil {
				seen[r.key] = map[string]bool{}
			}
			if !seen[r.key][a.Value] {
				seen[r.key][a.Value] = true
				visible[r.key] = append(visible[r.key], a.Value)
			}
		}
	}
	for i := range m.messages {
		msg := &m.messages[i]
		key := linkCacheKey{m.name, messageKey(*msg), msg.Text}
		if msg.Artifact != nil {
			continue
		}
		var destinations []string
		for _, s := range visible[key.key] {
			if !m.linksObserved[key][s] {
				destinations = append(destinations, s)
				if len(destinations) >= filelink.MaxTargets {
					break
				}
			}
		}
		if len(destinations) == 0 {
			continue
		}
		if len(m.fileLinks) >= maxMessages*2 {
			m.fileLinks = map[linkCacheKey]filelink.Admissions{}
			m.linksObserved = map[linkCacheKey]map[string]bool{}
		}
		m.linksBusy = true
		view, epoch := m.channelView, m.linkEpoch
		return func() tea.Msg {
			return m.linkJobs.run(func(ctx context.Context) tea.Msg {
				return linkAdmissionMsg{key: key, view: view, epoch: epoch, destinations: destinations, admissions: m.opts.Navigation.AdmitDestinations(ctx, destinations)}
			})
		}
	}
	return nil
}
func (m *Model) receiveFileLinks(msg linkAdmissionMsg) {
	m.linksBusy = false
	if msg.view != m.channelView || msg.epoch != m.linkEpoch {
		return
	}
	for _, v := range m.messages {
		if messageKey(v) == msg.key.key && v.Text == msg.key.text {
			m.captureAnchor()
			if m.fileLinks[msg.key] == nil {
				m.fileLinks[msg.key] = filelink.Admissions{}
			}
			if m.linksObserved[msg.key] == nil {
				m.linksObserved[msg.key] = map[string]bool{}
			}
			for _, s := range msg.destinations {
				m.linksObserved[msg.key][s] = true
			}
			for s, t := range msg.admissions {
				m.fileLinks[msg.key][s] = t
			}
			delete(m.bodyCache, msg.key.key)
			m.sel = nil
			m.touch()
			return
		}
	}
}
func (m *Model) openFileLink(a action) tea.Cmd {
	// The source message identity is part of the press/action, independent of
	// whatever later occupies that cell. Only current admitted targets route.
	if m.linksOpening {
		return nil
	}
	key := linkCacheKey{m.name, a.Extra, ""}
	var target filelink.Target
	found := false
	for _, msg := range m.messages {
		if messageKey(msg) == key.key {
			key.text = msg.Text
			target, found = m.fileLinks[key][a.Value]
			break
		}
	}
	if !found {
		return nil
	}
	if m.opts.OpenFile == nil {
		m.say(levelWarn, "Open local files from the yoloctl TUI")
		return nil
	}
	m.linksOpening = true
	view, epoch := m.channelView, m.linkEpoch
	return func() tea.Msg {
		return m.linkJobs.run(func(ctx context.Context) tea.Msg {
			return fileOpenedMsg{key: key, view: view, target: target, epoch: epoch, err: m.opts.Navigation.Check(ctx, target)}
		})
	}
}
func (m *Model) checkedFile(msg fileOpenedMsg) tea.Cmd {
	m.linksOpening = false
	if msg.view != m.channelView || msg.epoch != m.linkEpoch {
		return nil
	}
	exists := false
	for _, v := range m.messages {
		if messageKey(v) == msg.key.key && v.Text == msg.key.text {
			exists = true
			break
		}
	}
	if !exists {
		return nil
	}
	if msg.err != nil {
		m.say(levelWarn, msg.err.Error())
		return nil
	}
	return m.opts.OpenFile(msg.target)
}
