package chatui

// CatalogRead identifies a local host read and its originating stream link.
// Sequence orders equal-generation reads too; neither field grants authority.
type CatalogRead struct{ Link, Sequence uint64 }

type RefreshLinkMsg struct{ Link uint64 }
type RefreshResultMsg struct {
	Read    CatalogRead
	Err     error
	Refused bool // driver validation has a separate once-per-run diagnostic
}

type noticeOwner uint8

const (
	noticeGeneric noticeOwner = iota
	noticeRefresh
	noticeJoin
)

type refreshState struct {
	link, outcome, accepted uint64
	failing, refused        bool
}

func (m *Model) clearNotice() {
	m.notice = ""
	m.noticeOwner, m.noticeRead = noticeGeneric, CatalogRead{}
}

func (m *Model) refreshLink(link uint64) bool {
	if link == 0 || link < m.refresh.link {
		return false
	}
	if link > m.refresh.link {
		m.refresh = refreshState{link: link}
	}
	return true
}

func (m *Model) refreshResult(msg RefreshResultMsg) {
	r := msg.Read
	if msg.Err == nil || !m.refreshLink(r.Link) || r.Sequence == 0 || r.Sequence < m.refresh.outcome || r.Sequence <= m.refresh.accepted {
		return
	}
	m.refresh.outcome = r.Sequence
	reported := &m.refresh.failing
	if msg.Refused {
		reported = &m.refresh.refused
	}
	if *reported {
		return
	}
	*reported = true
	m.say(levelError, msg.Err.Error())
	m.noticeOwner, m.noticeRead = noticeRefresh, r
}

// Called only after the view's catalog-generation check, not on RPC success
// or queue admission. An unrelated accepted catalog has no read identity.
func (m *Model) refreshAccepted(r CatalogRead) {
	if !m.refreshLink(r.Link) || r.Sequence == 0 || r.Sequence < m.refresh.outcome {
		return
	}
	m.refresh.outcome, m.refresh.accepted = r.Sequence, r.Sequence
	m.refresh.failing, m.refresh.refused = false, false
	if m.noticeOwner == noticeRefresh && (m.noticeRead.Link < r.Link || m.noticeRead.Link == r.Link && m.noticeRead.Sequence <= r.Sequence) {
		m.clearNotice()
	}
}
