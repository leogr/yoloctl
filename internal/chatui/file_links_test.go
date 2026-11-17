package chatui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/markdownview"
)

func localLinkModel(t *testing.T) (*Model, *int, *int) {
	t.Helper()
	m, _ := readyModel()
	t.Cleanup(m.CloseLinks)
	probes, opens := 0, 0
	m.opts.Navigation = &filelink.Navigator{Context: filelink.Context{Mode: "vm", Space: "trial", Base: "/p", Root: "/p"}, Probe: func(_ context.Context, ts []filelink.Target) map[filelink.Target]error {
		probes++
		out := map[filelink.Target]error{}
		for _, t := range ts {
			out[t] = nil
		}
		return out
	}}
	m.opts.OpenFile = func(filelink.Target) tea.Cmd { return func() tea.Msg { opens++; return nil } }
	m.Update(IncomingMsg{Message: Message{ID: "a", Text: "[file](a.md)"}})
	m.View()
	if cmd := m.settleFileLinks(); cmd != nil {
		m.receiveFileLinks(cmd().(linkAdmissionMsg))
	}
	return m, &probes, &opens
}
func TestChatLocalLinkKeepsDraftQuoteAndBrowserIsolation(t *testing.T) {
	m, probes, opened := localLinkModel(t)
	if *probes != 1 {
		t.Fatal("no background admission", *probes)
	}
	if err := m.SetDraft("draft"); err != nil {
		t.Fatal(err)
	}
	body := m.body(&m.messages[0], 60)
	if body.lines[0].Text != "file" {
		t.Fatal(body.lines[0].Text)
	}
	var srcs []lineSrc
	displayLinesMap(m.widthMethod, m.messages[0].Text, false, m.theme, m.self, &srcs, nil, body.links)
	q := markdownview.SourceSelection{Lines: body.lines, Sources: srcs, Text: m.messages[0].Text, Render: func(s string) string { return rendered(s, false, m, body) }}
	if text, ok := q.Exact(0, 0, 0, 4, true, true); !ok || text != "[file](a.md)" {
		t.Fatal(text, ok)
	}
	act := action{Kind: markdownview.IntentLocalFile, Value: "a.md", Extra: messageKey(m.messages[0])}
	cmd := m.openFileLink(act)
	if cmd == nil {
		t.Fatal("no activation")
	}
	if m.openFileLink(act) != nil {
		t.Fatal("concurrent open checks")
	}
	next := m.checkedFile(cmd().(fileOpenedMsg))
	if next == nil {
		t.Fatal("no typed file callback")
	}
	next()
	if *opened != 1 || *probes != 2 || m.Draft() != "draft" || m.opts.OpenLink != nil {
		t.Fatal(*opened, *probes, m.Draft())
	}
}
func TestChatStaleAdmissionAndFileChecksDoNotOpen(t *testing.T) {
	m, _, opened := localLinkModel(t)
	key := linkCacheKey{m.name, messageKey(m.messages[0]), m.messages[0].Text}
	stale := linkAdmissionMsg{key: key, view: m.channelView, epoch: m.linkEpoch}
	m.messages[0].Text = "changed"
	m.receiveFileLinks(stale)
	cmd := m.checkedFile(fileOpenedMsg{key: key, view: m.channelView, epoch: m.linkEpoch, target: filelink.Target{}})
	if cmd != nil || *opened != 0 {
		t.Fatal("changed document opened")
	}
	m.opts.Navigation.Probe = func(context.Context, []filelink.Target) map[filelink.Target]error { return nil }
	key.text = "changed"
	m.checkedFile(fileOpenedMsg{key: key, view: m.channelView, epoch: m.linkEpoch, err: errors.New("revoked")})
	if !strings.Contains(m.notice, "revoked") {
		t.Fatal(m.notice)
	}
	m.linkEpoch++
	m.receiveFileLinks(stale)
	if cmd := m.checkedFile(fileOpenedMsg{key: key, view: m.channelView, epoch: stale.epoch}); cmd != nil {
		t.Fatal("stale channel opened")
	}
}
