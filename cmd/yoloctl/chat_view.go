package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func runChatView(cmd *cobra.Command, _ []string) error {
	mode, err := chatCatalogMode(cmd)
	if err != nil {
		return err
	}
	if !stdinIsTerminal(cmd.InOrStdin()) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("chat needs an interactive terminal; use chat ensure/status for scripts")
	}
	header, err := ensureChatHeader(cmd)
	if err != nil {
		return err
	}
	target, err := selectControllerTarget(cmd.Context(), cmd)
	if err != nil {
		return err
	}
	if target.Operator {
		return errors.New("human chat view requires the host controller endpoint")
	}
	prefs, err := config.LoadTUI()
	if err != nil {
		return err
	}
	// Claim inherited descriptors so they cannot accidentally enter a child;
	// this component owns its local terminal and needs no VT/session channel.
	view, err := termio.InheritedView()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(view)
	// Inside the TUI a chat tab may ask for a guarded attach tab.
	requests, err := termio.InheritedTabRequests()
	if err != nil {
		return err
	}
	if requests != nil {
		defer func() { _ = requests.Close() }()
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	clipboard := "off"
	if prefs.ClipboardEnabled() {
		clipboard = "on"
	}
	identities, members := chatIdentities(header)
	opts := chatui.Options{Space: target.Space, Channel: header.Channel, Nick: header.Nick, Account: header.SelfAccount, MaxMessageBytes: chatstream.MaxMessageBytes,
		Clipboard: clipboard, Browser: prefs.BrowserPolicy(), Identities: identities, Icons: prefs.GetAgentIcons()}
	s := &chatSession{catalogRead: mode, ctx: ctx, target: target, members: members, header: header, agentsWake: make(chan struct{}, 1)}
	configureChatImages(s, &opts, prefs.ClipboardEnabled(), hostintegration.ReadImage)
	opts.LoadOlder = s.loadOlder
	opts.Send = s.send
	opts.Copy = func(text string) tea.Cmd {
		sequence, notice := termio.EvaluateOSC52(termio.OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte(text))}, prefs.ClipboardEnabled())
		if sequence == "" {
			return func() tea.Msg { return chatui.ActionResultMsg{Err: errors.New(notice)} }
		}
		return tea.Raw(sequence)
	}
	configureChatBrowser(ctx, &opts, prefs.BrowserPolicy(), hostintegration.OpenURL)
	opts.ResolveAttach = func(account string) tea.Cmd {
		return func() tea.Msg { return s.resolveAttach(cmd, account) }
	}
	opts.Attach = chatAttacher(target.Space, requests)
	opts.SaveNick = func(nick string) tea.Cmd {
		return func() tea.Msg {
			status, err := callChatController(ctx, cmd, "chat.nickname", chatRuntimeRequest{Nickname: nick})
			if err != nil {
				return chatui.NickSavedMsg{Err: err}
			}
			if status.Room != nil && status.Room.Human.Nickname != "" {
				nick = status.Room.Human.Nickname
			}
			return chatui.NickSavedMsg{Nick: nick}
		}
	}
	opts.Reconnect = func() tea.Cmd {
		return func() tea.Msg { return s.reconnect(cmd) }
	}
	opts.TakeOver = func() tea.Cmd {
		return func() tea.Msg { return s.takeOver() }
	}
	opts.Control = func(a chatui.Action) tea.Cmd {
		return s.control(a, s.readCatalog)
	}
	opts.RefreshChannels = func() tea.Cmd {
		return func() tea.Msg { return s.refreshCatalog() }
	}
	s.program = tea.NewProgram(chatui.New(opts), tea.WithoutSignalHandler(), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	s.start(false)
	s.refreshAgents()
	go s.agentsLoop()
	go s.refreshLoop()
	go streamChatTelemetry(ctx, target.Space, s.program.Send)
	programDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.program.Quit()
		case <-programDone:
		}
	}()
	_, err = s.program.Run()
	close(programDone)
	cancel()
	s.stop()
	if cmd.Context().Err() != nil {
		return nil
	}
	return err
}

// ensureChatHeader ensures the managed room and returns the client header,
// including the trusted identity map and the saved human nickname.
func ensureChatHeader(cmd *cobra.Command) (chatstream.Header, error) {
	return openChatHeader(cmd)
}

// chatIdentities maps authenticated accounts to trusted display identities
// and enrolled box members to their immutable member IDs. Display names come
// only from the controller's identity map; nicks never select a member.
func chatIdentities(header chatstream.Header) (map[string]chatui.Identity, map[string]string) {
	identities, members := map[string]chatui.Identity{}, map[string]string{}
	for _, identity := range header.Identities {
		if identity.Revoked {
			continue
		}
		identities[identity.Account] = chatui.Identity{Name: identity.Name, Nick: identity.Nick, Human: identity.Role == "human", Box: identity.BoxID != ""}
		if identity.BoxID != "" {
			members[identity.Account] = identity.ID
		}
	}
	return identities, members
}

// chatSession owns the current stream link. An explicit reconnect replaces
// the link: the old stream ends first, so this view holds one connection at
// once. The saved nickname admits one human session: another view of this
// account is replaced only after the user chooses Take over.
type chatSession struct {
	catalogRead chatCatalogReadMode // fixed for this view; zero also selects single

	ctx     context.Context
	target  controllerTarget
	program *tea.Program
	mu      sync.Mutex
	link    *chatLink
	members map[string]string
	// header is the latest trusted room header (under mu); image paste binds
	// its immutable room, disk, human and channel identities.
	header          chatstream.Header
	refreshSequence uint64 // guarded by mu; spans all links of this view
	// agentsWake asks agentsLoop for one more read; requests coalesce.
	agentsWake chan struct{}
}

var chatLinkSequence atomic.Uint64

type chatLink struct {
	id       uint64 // immutable stream-link identity
	outgoing chan chatstream.Send
	older    chan chatstream.HistoryRequest
	actions  chan chatstream.Action
	catalogs chan chatstream.Catalog
	cancel   context.CancelFunc
	done     chan struct{}
	stopping atomic.Bool
	// mu orders queue admission with closing: once the driver has stopped,
	// nothing more is queued, and what it never read is reported unsent.
	mu     sync.Mutex
	closed bool
}

// admit queues v unless the link has closed; it reports whether v was
// queued and whether the link was closed.
func admit[T any](l *chatLink, queue chan T, v T) (queued, closed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false, true
	}
	select {
	case queue <- v:
		return true, false
	default:
		return false, false
	}
}

// close stops admission and returns the requests the driver never read.
func (l *chatLink) close() []tea.Msg {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return l.unread()
}

// offerCatalog queues a refreshed catalog. A snapshot still waiting is
// coalesced only with one of the same room, disk and self identity: the
// higher generation wins, and at an equal generation the header must match
// (only the runtime observed/unobserved result may differ, and the newer
// read wins). Anything else is a conflict reported to the caller; the queued
// snapshot stays. The driver validates whatever reaches it; holding mu stops
// concurrent offers from restoring an older snapshot.
func (l *chatLink) offerCatalog(c chatstream.Catalog) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	select {
	case queued := <-l.catalogs:
		q, n := queued.Header, c.Header
		switch {
		case q.RoomID != n.RoomID || q.DiskID != n.DiskID || q.SelfAccount != n.SelfAccount:
			l.catalogs <- queued
			return errors.New("the room catalog changed identity; reconnect explicitly")
		case q.Generation > n.Generation:
			c = queued
		case q.Generation == n.Generation && !reflect.DeepEqual(q, n):
			l.catalogs <- queued
			return errors.New("two catalog reads of one generation disagree")
		}
	default:
	}
	l.catalogs <- c // only the driver consumes, so the slot is free
	return nil
}

// catalogRefresh is how often an open view rereads the trusted catalog, so
// enrollments and revocations reach its identity map without reopening.
const catalogRefresh = time.Minute

func (s *chatSession) current() *chatLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.link
}

// start connects a new stream; its events reach the program in order.
// takeOver replaces this account's other session holding the nickname.
func (s *chatSession) start(takeOver bool) {
	ctx, cancel := context.WithCancel(s.ctx)
	l := &chatLink{id: chatLinkSequence.Add(1), outgoing: make(chan chatstream.Send, 1), older: make(chan chatstream.HistoryRequest, 1), actions: make(chan chatstream.Action, 1),
		catalogs: make(chan chatstream.Catalog, 1), cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.link = l
	s.mu.Unlock()
	requestRead, requestWrite := io.Pipe()
	responseRead, responseWrite := io.Pipe()
	transportDone := make(chan struct{})
	go func() {
		defer close(transportDone)
		err := callChatStream(ctx, s.target, takeOver, requestRead, responseWrite)
		_ = responseWrite.CloseWithError(err)
		_ = requestRead.CloseWithError(err)
	}()
	go func() {
		defer close(l.done)
		s.program.Send(chatui.RefreshLinkMsg{Link: l.id})
		_ = chatstream.RunChannels(ctx, responseRead, requestWrite, l.outgoing, l.older, l.actions, l.catalogs, func(e chatstream.Event) {
			if l.stopping.Load() && e.Kind == "connection" {
				// A requested reconnect ends this stream; it is not a failure.
				s.program.Send(chatui.ConnectionMsg{})
				return
			}
			if e.Kind == "connection" && chatNickHeld(e.Err) {
				// The user decides what happens; nothing retries on its own.
				s.program.Send(chatui.NickHeldMsg{})
				return
			}
			if e.Kind == "catalog" {
				if e.Catalog != nil {
					if !s.adoptCatalogFor(l, e.Catalog.Header) {
						return
					}
					// Even an uncorrelated startup/JOIN catalog keeps its link
					// identity; sequence zero cannot acknowledge a refresh.
					copy := *e.Catalog
					if copy.Read.Link == 0 {
						copy.Read.Link = l.id
					}
					e.Catalog = &copy
				} else if s.current() != l {
					return
				}
			}
			if msg := chatViewEvent(e); msg != nil {
				s.program.Send(msg)
			}
		})
		// Before l.done (and so before a reconnect's SessionMsg), report
		// what the stopped driver never took; driver-taken sends keep their
		// genuine uncertain outcome.
		for _, msg := range l.close() {
			s.program.Send(msg)
		}
		cancel()
		_ = responseRead.Close()
		_ = requestWrite.Close()
		<-transportDone
	}()
}

// chatNickHeld reports a stream that ended because another session of this
// account holds the saved nickname: the controller's typed refusal before any
// effect or, when that session connected meanwhile, the confined client's
// NICK_HELD registration failure in the stream's diagnostic.
func chatNickHeld(err error) bool {
	return errors.Is(err, chatstream.ErrNickHeld) || err != nil && strings.Contains(err.Error(), "IRC registration failed (NICK_HELD)")
}

// errChatNickTaken explains AIRC's 433 for the saved nickname: another
// account holds it, which no take-over can change.
var errChatNickTaken = errors.New("another account is connected with your nickname; save a different nickname in settings, then reconnect")

// unread drains requests the stopped driver never read, so the view learns
// they were not sent: a queued message is rejected (its draft stays), an
// action refused and a page request unavailable. Nothing is retried.
func (l *chatLink) unread() []tea.Msg {
	var out []tea.Msg
	for {
		select {
		case s := <-l.outgoing:
			out = append(out, chatui.DeliveryMsg{ID: s.ID, Channel: s.Channel, State: chatui.Rejected, Err: errors.New("the connection ended before this message was sent; draft retained")})
		case a := <-l.actions:
			out = append(out, chatui.ControlMsg{ID: a.ID, Channel: a.Channel, Kind: a.Kind, State: "refused", Err: errors.New("the connection ended before this action was sent")})
		case r := <-l.older:
			out = append(out, chatui.HistoryPageMsg{Channel: r.Channel, ID: r.ID, Count: -1})
		default:
			return out
		}
	}
}

// stop ends the current stream and waits for its transport.
func (s *chatSession) stop() {
	if l := s.current(); l != nil {
		l.stopping.Store(true)
		l.cancel()
		<-l.done
	}
}

// reconnect applies saved settings: stop the stream, ensure the room again
// for the new header, then connect. Nothing pending is resent.
func (s *chatSession) reconnect(cmd *cobra.Command) tea.Msg {
	s.stop()
	header, err := ensureChatHeader(cmd)
	if err != nil {
		return chatui.ConnectionMsg{Err: err}
	}
	identities, members := chatIdentities(header)
	s.mu.Lock()
	s.members, s.header = members, header
	s.mu.Unlock()
	s.program.Send(chatui.SessionMsg{Nick: header.Nick, Identities: identities})
	s.start(false)
	s.refreshAgents()
	return nil
}

// takeOver connects again, replacing this account's other session that holds
// the nickname. Only the user's explicit Take over reaches it; nothing
// pending is resent.
func (s *chatSession) takeOver() tea.Msg {
	s.stop()
	s.start(true)
	s.refreshAgents()
	return nil
}

// refreshAgents asks for one more agents read. A request made while one is
// pending coalesces with it; a nil channel (no loop) drops it.
func (s *chatSession) refreshAgents() {
	select {
	case s.agentsWake <- struct{}{}:
	default:
	}
}

// agentsLoop reads the members' agents and runtime one read at a time: when
// asked (connect, reconnect, membership change) and at the catalog cadence,
// until the view closes.
func (s *chatSession) agentsLoop() {
	ticker := time.NewTicker(catalogRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.agentsWake:
		case <-ticker.C:
		}
		if msg := s.loadAgents(func(ctx context.Context) ([]controllerBoxView, error) {
			boot, err := checkControllerSpace(ctx, s.target.Space)
			if err != nil {
				return nil, err
			}
			var view []controllerBoxView
			return view, controllerJSON(ctx, boot, "box.inventory", nil, nil, &view, io.Discard)
		}); msg != nil {
			s.program.Send(msg)
		}
	}
}

// agentsReadTimeout bounds one inventory read; the controller worker that
// serves it has its own live-observation budget.
var agentsReadTimeout = 5 * time.Second

// loadAgents reads the box inventory once for each member's agent (glyph and
// color) and its model and effort. A box counts only while its declaration
// ID still equals the member's immutable box ID. A read that a catalog
// change superseded is discarded and repeated; a failed read reports an
// error, so earlier runtime values show as stale.
func (s *chatSession) loadAgents(read func(context.Context) ([]controllerBoxView, error)) tea.Msg {
	s.mu.Lock()
	header := s.header
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, agentsReadTimeout)
	defer cancel()
	view, err := read(ctx)
	if err == nil {
		err = ctx.Err()
	}
	at := time.Now()
	s.mu.Lock()
	current := s.header
	s.mu.Unlock()
	if current.RoomID != header.RoomID || current.DiskID != header.DiskID || current.SelfAccount != header.SelfAccount || current.Generation != header.Generation {
		s.refreshAgents()
		return nil
	}
	if err != nil {
		if s.ctx.Err() != nil {
			return nil // the view is closing
		}
		return chatui.AgentsMsg{Err: errors.New("box inventory unavailable: " + err.Error())}
	}
	return chatui.AgentsMsg{Agents: chatAgents(header, view), Runtime: chatParticipantRuntime(header, view), At: at}
}

func chatAgents(header chatstream.Header, view []controllerBoxView) map[string]string {
	byID := map[string]string{}
	for _, b := range view {
		if b.Config != nil && b.Config.ID != "" {
			byID[b.Config.ID] = b.Config.Agent
		}
	}
	agents := map[string]string{}
	for _, id := range header.Identities {
		if agent := byID[id.BoxID]; id.BoxID != "" && agent != "" && !id.Revoked {
			agents[id.Account] = agent
		}
	}
	return agents
}

// adoptCatalogFor replaces the member map used for attachment with a trusted
// catalog's, and reloads agent glyphs when membership changed.
func (s *chatSession) adoptCatalogFor(link *chatLink, header chatstream.Header) bool {
	_, members := chatIdentities(header)
	s.mu.Lock()
	if link != nil && s.link != link {
		s.mu.Unlock()
		return false
	}
	changed := !maps.Equal(s.members, members)
	s.members, s.header = members, header
	s.mu.Unlock()
	if changed {
		s.refreshAgents()
	}
	return true
}

// refreshCatalog reads the trusted catalog through the controller and hands
// it to the driver, which checks room identity and generation before the
// view sees it. Nothing is joined or reconnected.
func (s *chatSession) readCatalog(ctx context.Context) (chatstream.Catalog, error) {
	if s.catalogRead == chatCatalogObserved {
		return readChatChannelCatalogTarget(ctx, s.target)
	}
	return readDesiredChatCatalogTarget(ctx, s.target)
}

func (s *chatSession) refreshCatalog() tea.Msg {
	return s.refreshCatalogRead(s.readCatalog)
}

func (s *chatSession) refreshCatalogRead(read func(context.Context) (chatstream.Catalog, error)) tea.Msg {
	s.mu.Lock()
	l := s.link
	s.refreshSequence++
	sequence := s.refreshSequence
	s.mu.Unlock()
	if l == nil {
		return nil
	}
	origin := chatstream.CatalogRead{Link: l.id, Sequence: sequence}
	failed := func(prefix string, err error) tea.Msg {
		return chatui.RefreshResultMsg{Read: uiCatalogRead(origin), Err: fmt.Errorf("%s: %w", prefix, err)}
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	c, err := read(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if s.ctx.Err() != nil || s.current() != l {
		return nil
	}
	if err != nil {
		return failed("channel and identity refresh failed", err)
	}
	c.Read = origin
	if err := l.offerCatalog(c); err != nil {
		return failed("channel and identity refresh conflict", err)
	}
	// Enqueue/coalescing is not acceptance. Only a correlated driver event
	// can recover the UI's refresh-owned notice.
	return nil
}

func uiCatalogRead(r chatstream.CatalogRead) chatui.CatalogRead {
	return chatui.CatalogRead{Link: r.Link, Sequence: r.Sequence}
}

// refreshLoop rereads the catalog at stream start and then periodically,
// until the view closes.
func (s *chatSession) refreshLoop() {
	ticker := time.NewTicker(catalogRefresh)
	defer ticker.Stop()
	for {
		if msg := s.refreshCatalog(); msg != nil {
			s.program.Send(msg)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *chatSession) loadOlder(r chatui.HistoryRequest) tea.Cmd {
	l := s.current()
	return func() tea.Msg {
		if queued, _ := admit(l, l.older, chatstream.HistoryRequest{ID: r.ID, Channel: r.Channel, Before: r.Before}); queued {
			return nil
		}
		return chatui.HistoryPageMsg{Channel: r.Channel, ID: r.ID, Count: -1}
	}
}

func (s *chatSession) send(out chatui.Outgoing) tea.Cmd {
	l := s.current()
	return func() tea.Msg {
		send := chatstream.Send{ID: out.ID, Channel: out.Channel, Text: out.Text, ReplyTo: out.ReplyTo}
		for _, a := range out.Attach {
			send.Attach = append(send.Attach, chatstream.Attach{ID: a.ID, At: a.At})
		}
		queued, closed := admit(l, l.outgoing, send)
		switch {
		case queued:
			return nil
		case closed:
			return chatui.DeliveryMsg{ID: out.ID, Channel: out.Channel, State: chatui.Rejected, Err: errors.New("the connection ended before this message was queued; draft retained")}
		}
		return chatui.DeliveryMsg{ID: out.ID, Channel: out.Channel, State: chatui.Rejected, Err: errors.New("another send is queued")}
	}
}

// control captures one link and queues one channel action. An explicit JOIN
// carries a fresh trusted catalog in that same driver envelope, so catalog
// acceptance precedes admission even when background refreshes are queued.
// The driver answers with the original action ID; nothing is retried.
func (s *chatSession) control(a chatui.Action, readCatalog func(context.Context) (chatstream.Catalog, error)) tea.Cmd {
	l := s.current()
	return func() tea.Msg {
		refuse := func(err error) tea.Msg {
			return chatui.ControlMsg{ID: a.ID, Channel: a.Channel, Kind: a.Kind, State: "refused", Err: err}
		}
		if l == nil {
			return refuse(errors.New("no chat connection is available"))
		}
		action := chatstream.Action{ID: a.ID, Kind: a.Kind, Channel: a.Channel, Topic: a.Topic}
		if a.Kind == "join" {
			if readCatalog == nil {
				return refuse(errors.New("JOIN needs a trusted channel catalog refresh"))
			}
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			defer cancel()
			catalog, err := readCatalog(ctx)
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				return refuse(fmt.Errorf("channel catalog refresh failed before JOIN: %w", err))
			}
			action.Catalog = &catalog
		}
		queued, closed := admit(l, l.actions, action)
		switch {
		case queued:
			return nil
		case closed:
			return chatui.ControlMsg{ID: a.ID, Channel: a.Channel, Kind: a.Kind, State: "refused", Err: errors.New("the connection ended before this action was queued")}
		}
		return chatui.ControlMsg{ID: a.ID, Channel: a.Channel, Kind: a.Kind, State: "refused", Err: errors.New("another channel action is queued")}
	}
}

// attachLookupTimeout bounds one participant lookup end to end, so a click
// never waits silently on a busy or unreachable controller.
var attachLookupTimeout = 10 * time.Second

// resolveAttach asks the controller, at click time, which declaration the
// account's membership currently names. Only the immutable member ID from
// the trusted identity map selects it; the guarded attach checks again.
func (s *chatSession) resolveAttach(cmd *cobra.Command, account string) tea.Msg {
	return s.resolveAttachWith(account, func(ctx context.Context, member string) (managedchat.AttachTarget, error) {
		return callChatAttachTarget(ctx, cmd, member)
	})
}

func (s *chatSession) resolveAttachWith(account string, lookup func(context.Context, string) (managedchat.AttachTarget, error)) tea.Msg {
	s.mu.Lock()
	member, ok := s.members[account]
	room := s.header.RoomID
	s.mu.Unlock()
	if !ok {
		return chatui.AttachTargetMsg{Err: errors.New("not an enrolled box member of this room")}
	}
	ctx, cancel := context.WithTimeout(s.ctx, attachLookupTimeout)
	defer cancel()
	t, err := lookup(ctx, member)
	if err == nil {
		err = ctx.Err() // an answer at the deadline is too late
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return chatui.AttachTargetMsg{Err: fmt.Errorf("the controller did not answer within %s (busy with a lifecycle operation, or unavailable); try again", attachLookupTimeout)}
	case err != nil:
		return chatui.AttachTargetMsg{Err: err}
	}
	// The trusted catalog may have changed while the controller answered.
	s.mu.Lock()
	current, still := s.members[account]
	currentRoom := s.header.RoomID
	s.mu.Unlock()
	if !still || current != member || currentRoom != room || t.MemberID != member || t.RoomID != room {
		return chatui.AttachTargetMsg{Err: errors.New("the room membership changed during the check; try again")}
	}
	return chatui.AttachTargetMsg{Target: attachTargetView(account, t)}
}

// attachTargetView presents a resolved target to the view. Only a current
// declaration that still matches the member (Box set) is attachable; one
// without a running container needs the human's consent to start it, which
// the guarded attach then accepts with --chat-allow-start.
func attachTargetView(account string, t managedchat.AttachTarget) chatui.AttachTarget {
	out := chatui.AttachTarget{Account: account, Box: t.Box, BoxID: t.BoxID, State: t.State, RoomID: t.RoomID, MemberID: t.MemberID}
	switch {
	case t.State == "revoked":
		out.Box, out.Reason = "", "its room membership was revoked"
	case t.Box == "":
		out.State, out.Reason = "missing", "its box was deleted or replaced since it was enrolled"
	case t.State != "running":
		out.State = "stopped" // matching declaration, no running container
	}
	return out
}

// chatAttacher opens a resolved participant's session through the same
// guarded `box attach`: in a TUI tab via the typed request channel,
// otherwise by handing this terminal over until the session detaches.
func chatAttacher(space string, requests *os.File) func(chatui.AttachTarget) tea.Cmd {
	var mu sync.Mutex
	return func(t chatui.AttachTarget) tea.Cmd {
		req := termio.TabRequest{Op: "attach", Space: space, Box: t.Box, ChatRoomID: t.RoomID, ChatMemberID: t.MemberID, ChatBoxID: t.BoxID, ChatAllowStart: t.State == "stopped"}
		if err := req.ValidateChat(); err != nil {
			return func() tea.Msg { return chatui.ActionResultMsg{Err: err} }
		}
		if requests != nil {
			return func() tea.Msg {
				mu.Lock()
				defer mu.Unlock()
				if err := termio.WriteTabRequest(requests, req); err != nil {
					return chatui.ActionResultMsg{Err: errors.New("could not open a tab for " + t.Box + ": " + err.Error())}
				}
				return chatui.ActionResultMsg{Text: "Opened " + t.Box + " in a new tab"}
			}
		}
		self, err := os.Executable()
		if err != nil {
			return func() tea.Msg { return chatui.ActionResultMsg{Err: err} }
		}
		c := exec.Command(self, req.ChatAttachArgs()...)
		c.Env = viewChildEnv()
		return tea.ExecProcess(c, func(err error) tea.Msg {
			if err != nil {
				return chatui.ActionResultMsg{Err: errors.New("attach " + t.Box + ": " + err.Error())}
			}
			return chatui.ActionResultMsg{Text: "Detached from " + t.Box}
		})
	}
}

func configureChatBrowser(ctx context.Context, opts *chatui.Options, policy string, openURL func(context.Context, string) error) {
	opts.OpenLink = nil
	opts.ConfirmLinks = policy != "on"
	if policy == "off" {
		return
	}
	opts.OpenLink = func(url string) tea.Cmd {
		return func() tea.Msg {
			err := ctx.Err()
			if err == nil {
				err = hostintegration.ValidateURL(url)
			}
			if err == nil {
				err = openURL(ctx, url)
			}
			return chatui.ActionResultMsg{Text: "Opened link", Err: err}
		}
	}
}

func chatViewEvent(e chatstream.Event) tea.Msg {
	switch e.Kind {
	case "connection":
		err := e.Err
		if err != nil && strings.Contains(err.Error(), "IRC registration failed (433)") {
			err = errChatNickTaken
		}
		return chatui.ConnectionMsg{Connected: e.Connected, MaxBytes: e.Limits.MaxBytes, MaxLines: e.Limits.MaxLines, Attachments: e.Attachments, Err: err}
	case "catalog":
		if e.Catalog == nil {
			if e.Err != nil {
				if e.CatalogRead.Link != 0 {
					return chatui.RefreshResultMsg{Read: uiCatalogRead(e.CatalogRead), Refused: true, Err: fmt.Errorf("channel catalog refused: %w", e.Err)}
				}
				return chatui.ActionResultMsg{Err: fmt.Errorf("channel catalog refused: %w", e.Err)}
			}
			return nil
		}
		return chatCatalog(*e.Catalog)
	case "members":
		members := make([]chatui.Member, 0, len(e.Members))
		for _, m := range e.Members {
			members = append(members, chatui.Member{Nick: m.Nick, Account: m.Account, Op: m.Op, Voice: m.Voice, Away: m.Away, AwayReason: m.AwayReason})
		}
		return chatui.MembersMsg{Channel: e.Channel, Members: members}
	case "membership":
		return chatui.MembershipMsg{Channel: e.Channel, State: e.State, ID: e.ID}
	case "control":
		return chatui.ControlMsg{ID: e.ID, Channel: e.Channel, Kind: e.Action, State: e.State, Code: e.Code, Err: e.Err}
	case "history-page":
		return chatui.HistoryPageMsg{Channel: e.Channel, ID: e.ID, Count: e.Count}
	case "delivery":
		d := chatui.DeliveryMsg{ID: e.ID, MessageID: e.MessageID, Channel: e.Channel, State: chatui.Delivery(e.State), Err: e.Err}
		if r := e.Refusal; r != nil {
			d.Refusal = &chatui.AttachmentRefusal{Reason: r.Reason, Index: r.Index, MessageID: r.MessageID}
		}
		return d
	case "message":
		m := e.Message
		channel := e.Channel
		if channel == "" {
			channel = m.Channel
		}
		var artifact *chatui.ArtifactRef
		if a := m.Artifact; a != nil { // only from validated reserved tags
			artifact = &chatui.ArtifactRef{ID: a.ID, Name: a.Name, SHA256: a.SHA256, Bytes: a.Bytes}
		}
		var attachments []chatui.AttachmentRef
		for _, a := range m.Attachments { // only from validated reserved tags
			attachments = append(attachments, chatui.AttachmentRef{ArtifactRef: chatui.ArtifactRef{ID: a.ID, Name: a.Name, SHA256: a.SHA256, Bytes: a.Bytes}, At: a.At})
		}
		return chatui.IncomingMsg{Message: chatui.Message{ID: m.ID, ClientID: m.ClientID, Channel: channel, Account: m.Account, Nick: m.Nick, Text: m.Text, ReplyTo: m.ReplyTo, Time: m.Time, Replay: m.Replay, Older: m.Older, Delivery: chatui.Confirmed, Artifact: artifact, Attachments: attachments}}
	case "topic":
		return chatui.TopicMsg{Channel: e.Channel, Text: e.Topic, By: e.TopicBy, Live: e.TopicLive, Unknown: e.State == "unknown"}
	case "gap":
		text := "Chat continuity is unknown"
		if e.Err != nil {
			text = e.Err.Error()
		}
		return chatui.GapMsg{Channel: e.Channel, Text: text}
	}
	return nil
}

// chatCatalog presents a driver-validated catalog: the channel list and the
// complete trusted identity map, which replaces the view's map so newly
// enrolled members become trusted and revoked ones stop being trusted.
func chatCatalog(c chatstream.Catalog) chatui.CatalogMsg {
	h := c.Header
	identities, _ := chatIdentities(h)
	out := chatui.CatalogMsg{Read: uiCatalogRead(c.Read), Generation: h.Generation, Default: h.DefaultChannel, Applied: c.RuntimeApplied, RuntimeUnobserved: c.RuntimeUnobserved, Identities: identities}
	if out.Default == "" {
		out.Default = h.Channel
	}
	for _, ch := range h.Channels {
		out.Channels = append(out.Channels, chatui.ChannelInfo{Name: ch.Name, ID: ch.ID, Members: len(ch.Members)})
	}
	for _, deleted := range h.DeletedChannels {
		out.Deleted = append(out.Deleted, deleted.ID)
	}
	if len(out.Channels) == 0 && out.Default != "" {
		out.Channels = []chatui.ChannelInfo{{Name: out.Default}}
	}
	return out
}
