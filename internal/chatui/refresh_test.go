package chatui

import (
	"errors"
	"reflect"
	"testing"
)

func refreshFailure(m *Model, link, sequence uint64, text string) {
	m.Update(RefreshResultMsg{Read: CatalogRead{link, sequence}, Err: errors.New(text)})
}
func refreshSuccess(m *Model, link, sequence uint64) {
	m.Update(CatalogMsg{Generation: 7, Read: CatalogRead{link, sequence}})
}

func TestRefreshRecoversOnlyItsOwnNotice(t *testing.T) {
	m, _, h := stagedModel(t)
	paste(m, "human draft ")
	img := pasteImg(t, m, h, "private-png")
	m.replyTo = "reply"
	m.scroll, m.follow, m.selected, m.focus = 3, false, 1, focusMessages
	before := m.editor
	reply := m.replyTo
	refreshFailure(m, 1, 1, "refresh failed")
	if m.noticeOwner != noticeRefresh || m.notice != "refresh failed" {
		t.Fatal(m.notice)
	}
	refreshSuccess(m, 1, 2)
	if m.notice != "" || !reflect.DeepEqual(before, m.editor) || m.replyTo != reply || m.editor.images()[0] != img || m.scroll != 3 || m.follow || m.selected != 1 || m.focus != focusMessages {
		t.Fatal("recovery changed draft/cursor/reply/images")
	}
	refreshFailure(m, 1, 3, "refresh failed")
	m.Update(ActionResultMsg{Err: errors.New("refresh failed")}) // identical text, another owner
	refreshSuccess(m, 1, 4)
	if m.notice != "refresh failed" || m.noticeOwner != noticeGeneric {
		t.Fatal("cleared unrelated identical text")
	}
	refreshFailure(m, 1, 5, "new failure")
	if m.notice != "new failure" {
		t.Fatal("suppression was not reset at accepted recovery")
	}
	m.say(levelError, "another action")
	refreshFailure(m, 1, 6, "repeated failure")
	if m.notice != "another action" {
		t.Fatal("same failure run overwrote another notice")
	}
}

func TestRefreshOrderingAndLinkIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events func(*Model)
		want   string
	}{
		{"old acceptance", func(m *Model) { refreshFailure(m, 1, 3, "new"); refreshSuccess(m, 1, 2) }, "new"},
		{"old failure", func(m *Model) { refreshSuccess(m, 1, 3); refreshFailure(m, 1, 2, "old") }, ""},
		{"equal-generation newer recovery", func(m *Model) { refreshFailure(m, 1, 3, "new"); refreshSuccess(m, 1, 4) }, ""},
		{"unrelated accepted catalog", func(m *Model) { refreshFailure(m, 1, 3, "new"); m.Update(CatalogMsg{Generation: 7}) }, "new"},
		{"old link acceptance", func(m *Model) { refreshFailure(m, 2, 1, "new"); refreshSuccess(m, 1, 9) }, "new"},
		{"old link failure", func(m *Model) { m.Update(RefreshLinkMsg{Link: 2}); refreshFailure(m, 1, 9, "old") }, ""},
		{"low generation", func(m *Model) {
			refreshSuccess(m, 1, 1)
			refreshFailure(m, 1, 2, "new")
			m.Update(CatalogMsg{Generation: 6, Read: CatalogRead{1, 3}})
		}, "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := readyModel()
			tc.events(m)
			if m.notice != tc.want {
				t.Fatalf("notice=%q want %q", m.notice, tc.want)
			}
		})
	}
}

func TestRefreshOwnershipSurvivesHiddenChannelsAndJoinRefusal(t *testing.T) {
	m, _ := readyModel()
	m.view("#build")
	refreshFailure(m, 1, 1, "refresh")
	m.within("#build", func() { m.say(levelInfo, "hidden information") })
	if m.noticeOwner != noticeRefresh || m.notice != "refresh" {
		t.Fatal("lost saved owner")
	}
	refreshSuccess(m, 1, 2)
	if m.notice != "" {
		t.Fatal("restored refresh owner did not recover")
	}
	refreshFailure(m, 1, 3, "refresh")
	m.within("#build", func() { m.sayEverywhere(levelError, "hidden action") })
	refreshSuccess(m, 1, 4)
	if m.noticeOwner != noticeGeneric || m.notice == "" {
		t.Fatal("lost carried action owner")
	}
	m.pending = &pendingAction{Action: Action{ID: "join", Kind: "join", Channel: "#build"}}
	m.Update(ControlMsg{ID: "join", Kind: "join", Channel: "#build", State: "refused", Err: errors.New("JOIN denied")})
	if m.noticeOwner != noticeJoin {
		t.Fatal("missing JOIN owner")
	}
	refreshSuccess(m, 1, 5)
	if m.noticeOwner != noticeJoin || m.notice == "" {
		t.Fatal("minute refresh erased JOIN refusal")
	}
}

// A preceding read error must not hide a separately returned authority or
// identity refusal. Each diagnostic suppresses only its own repeated run.
func TestRefreshDriverRefusalHasOwnFailureRun(t *testing.T) {
	m, _ := readyModel()
	refreshFailure(m, 1, 1, "read failed")
	refuse := func(sequence uint64) {
		m.Update(RefreshResultMsg{Read: CatalogRead{1, sequence}, Refused: true, Err: errors.New("catalog authority refused")})
	}
	refuse(2)
	if m.notice != "catalog authority refused" || m.noticeOwner != noticeRefresh {
		t.Fatal("read failure hid driver refusal", m.notice)
	}
	m.say(levelError, "unrelated")
	refuse(3)
	if m.notice != "unrelated" {
		t.Fatal("repeat refusal overwrote another notice")
	}
	refreshSuccess(m, 1, 4)
	if m.notice != "unrelated" {
		t.Fatal("recovery erased unrelated notice")
	}
	refuse(5)
	if m.notice != "catalog authority refused" {
		t.Fatal("accepted recovery did not reset refusal run")
	}
}
