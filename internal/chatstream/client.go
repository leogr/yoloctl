package chatstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

type Send struct {
	ID, Channel, Text, ReplyTo string
	// Attach publishes staged artifacts with this one message.
	Attach []Attach
}
type Message struct {
	Channel                                    string
	ID, ClientID, Account, Nick, Text, ReplyTo string
	Time                                       time.Time
	Replay                                     bool
	// Older marks a message from an explicit older-history page.
	Older    bool
	Artifact *Artifact
	// Attachments are the staged artifacts this message published.
	Attachments []Attachment
}

// Limits are the server's negotiated multiline bounds for one logical message.
type Limits struct {
	MaxBytes int `json:"max_bytes"`
	MaxLines int `json:"max_lines"`
}

// Member is one connected channel session. Account comes from the server's
// authenticated JOIN prefix or message tags and stays empty until observed.
type Member struct {
	Nick, Account string
	Op, Voice     bool
	// Away is explicit availability (AWAY), never inferred from silence.
	Away       bool
	AwayReason string
}

type Event struct {
	CatalogRead          CatalogRead `json:"-"`
	Channel              string
	Action               string
	Catalog              *Catalog
	Topic, TopicBy       string
	TopicLive            bool
	Kind                 string // connection, message, delivery, gap, members, history-page, history-latest
	Message              Message
	ID, MessageID, State string
	// Code is a machine-readable control refusal, independent of display text.
	Code string
	// Refusal details a definite refusal of a send with attachments.
	Refusal *AttachmentRefusal
	// Attachments reports, on connection, that this stream may send staged
	// attachments (the confined client has the AIRC feature).
	Attachments bool
	Connected   bool
	Limits      Limits
	Members     []Member
	// Count of messages in a finished older page; 0 = no older history,
	// -1 = the request was refused.
	Count int
	Err   error
}

// Bound on tracked sessions; a larger roster is reported as incomplete.
const maxRoster = 4096

type envelope struct {
	ID                   string          `json:"id"`
	OK                   bool            `json:"ok"`
	Result               json.RawMessage `json:"result"`
	Error                string          `json:"error"`
	Event                string          `json:"event"`
	Version              int             `json:"version"`
	Epoch, Nick, Account string
	Multiline            json.RawMessage `json:"multiline"`
	// ServerError is a closed event's AIRC ERROR reason, if any.
	ServerError string `json:"server_error"`
}
type record struct {
	Cursor  uint64            `json:"cursor"`
	Event   string            `json:"event"`
	Receipt string            `json:"receipt"`
	State   string            `json:"state"`
	MsgID   string            `json:"msgid"`
	Reason  string            `json:"reason"`
	Target  string            `json:"target"`
	Command string            `json:"command"`
	Params  []string          `json:"params"`
	Prefix  string            `json:"prefix"`
	Tags    map[string]string `json:"tags"`
}
type pendingSend struct {
	Send
	Receipt string
}
type historyBatch struct {
	older    bool
	messages int
	// Open across a read gap: an older page was already finished as
	// unavailable, so the rest of it is dropped.
	gapped bool
	// Opened after a read gap: it answers no known request.
	discard bool
}

type driver struct {
	header         Header
	epoch          string
	cursor, serial uint64
	pending        *pendingSend
	// Keep only the most recent send for the receipt-before-echo ordering.
	last    *pendingSend
	batches map[string]*historyBatch
	// Accepted history requests whose batch or failure has not arrived yet.
	// The initial LATEST request is always sent first, so it resolves first.
	initialHistory bool
	olderRequests  int
	// After a read gap, later history cannot be matched to requests: paging
	// stays off and late batches are discarded until an explicit reconnect.
	historyLost bool
	roster      map[string]Member
	names       []Member
	topic       string
	emit        func(Event)
	read        *bufio.Reader
	in          io.ReadCloser
	out         io.WriteCloser
	// A multi-channel owner retains one transport/cursor and delegates only
	// channel-local message/roster parsing to this existing adapter.
	exchangeFn     func(Request) (envelope, error)
	allowedChannel func(string) bool
	// closedFor records why this side closed the stream (a reply deadline or
	// local cancellation), so a failed read reports that cause.
	closedFor atomic.Pointer[string]
}

// Run owns both ends of one controller stream. All commands and responses are
// serialized; cancellation closes the descriptors, and no operation is retried.
// The outgoing queue belongs to the view and should have capacity one. Each
// value on older is the oldest known message ID; it requests the page before it.
func Run(ctx context.Context, in io.ReadCloser, out io.WriteCloser, outgoing <-chan Send, older <-chan string, emit func(Event)) (err error) {
	d := &driver{in: in, out: out, read: bufio.NewReaderSize(in, ResponseLimit+1), emit: emit, batches: map[string]*historyBatch{}, roster: map[string]Member{}}
	closeStream := func() { _ = in.Close(); _ = out.Close() }
	stop := context.AfterFunc(ctx, func() { d.closeFor("was closed by this view", closeStream) })
	defer stop()
	defer closeStream()
	defer func() {
		if d.pending != nil {
			emit(Event{Kind: "delivery", ID: d.pending.ID, State: "uncertain", Err: errors.New("connection ended before confirmation; inspect history before resending")})
		}
		emit(Event{Kind: "connection", Err: err})
	}()
	timeout := streamReplyTimeout // read once, before any callback can run
	initial := time.AfterFunc(timeout, func() {
		d.closeFor(fmt.Sprintf("sent no room header and client handshake within %v", timeout), closeStream)
	})
	defer initial.Stop()
	if err = d.frame(&d.header); err != nil {
		return err
	}
	if err = validateHeader(d.header); err != nil {
		return err
	}
	var ready envelope
	if err = d.frame(&ready); err != nil {
		return err
	}
	var limits Limits
	if ready.Event != "ready" || ready.Version != 2 || !validReference(ready.Epoch, 80) || ready.Account != d.header.SelfAccount || ready.Nick != d.header.Nick || json.Unmarshal(ready.Multiline, &limits) != nil || limits.MaxBytes <= 0 || limits.MaxLines <= 0 {
		return errors.New("AIRC client did not establish the expected authenticated multiline JSONL v2 connection")
	}
	initial.Stop()
	d.epoch = ready.Epoch
	// The stream allowlist bounds a request below the server's own limit.
	limits.MaxBytes = min(limits.MaxBytes, MaxMessageBytes)
	emit(Event{Kind: "connection", Connected: true, Limits: limits, Attachments: d.header.Attachments})
	if reply, e := d.exchange(Request{Op: "history", Channel: d.header.Channel, Direction: "LATEST", Reference: "*", Limit: 20}); e != nil {
		return e
	} else if !reply.OK {
		emit(Event{Kind: "gap", Err: errors.New("initial history was rejected; continuity is unknown")})
	} else {
		d.initialHistory = true
	}
	if reply, e := d.exchange(Request{Op: "members", Channel: d.header.Channel}); e != nil {
		return e
	} else if !reply.OK {
		emit(Event{Kind: "gap", Err: errors.New("member list was rejected; the roster is unknown")})
	}
	// WHO supplies authenticated accounts and away state for NAMES members.
	if reply, e := d.exchange(Request{Op: "who", Channel: d.header.Channel}); e != nil {
		return e
	} else if !reply.OK {
		emit(Event{Kind: "gap", Err: errors.New("member identities were rejected; accounts stay unverified")})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case send, ok := <-outgoing:
			if !ok {
				return nil
			}
			if err := d.send(send); err != nil {
				return err
			}
		case before := <-older:
			if err := d.loadOlder(before); err != nil {
				return err
			}
		default:
		}
		reply, err := d.exchange(Request{Op: "read", After: d.cursor, Limit: 1, WaitMS: 250})
		if err != nil {
			return err
		}
		if !reply.OK {
			return errors.New("AIRC event read failed; reconnect explicitly")
		}
		var page struct {
			Epoch        string   `json:"epoch"`
			Events       []record `json:"events"`
			Next, Latest uint64
			Gap          bool `json:"gap"`
		}
		if json.Unmarshal(reply.Result, &page) != nil || page.Epoch != d.epoch || len(page.Events) > 1 || page.Next < d.cursor || page.Next > page.Latest {
			return errors.New("invalid AIRC read cursor or epoch")
		}
		if page.Gap {
			d.historyGap()
		}
		if len(page.Events) == 0 && page.Next != d.cursor {
			return errors.New("AIRC advanced an empty read")
		}
		for _, r := range page.Events {
			if r.Cursor <= d.cursor || r.Cursor != page.Next {
				return errors.New("AIRC read event cursor mismatch")
			}
			if err := d.record(r); err != nil {
				return err
			}
		}
		d.cursor = page.Next // never skip ahead to latest
	}
}

func validateHeader(h Header) error {
	if h.Event != "managed-room" || (h.Schema != 1 && h.Schema != 2) || h.Channel != managedchat.DefaultChannel || h.Generation == 0 || layout.ValidateIdentity(h.RoomID) != nil || layout.ValidateIdentity(h.DiskID) != nil || h.SelfAccount != h.HumanAccount || len(h.Identities) == 0 || len(h.Identities) > managedchat.MaxMembers+1 {
		return errors.New("invalid trusted managed room header")
	}
	human := false
	seen := map[string]bool{}
	for _, identity := range h.Identities {
		a, err := managedchat.AuthAccount(identity.ID)
		if err != nil || a != identity.Account || seen[a] {
			return errors.New("invalid managed room identity map")
		}
		seen[a] = true
		if identity.Role == "human" && !identity.Revoked && a == h.HumanAccount {
			human = true
		}
	}
	if !human {
		return errors.New("managed room human identity is missing")
	}
	return validateHeaderChannels(h)
}

// streamReplyTimeout bounds the wait for the room header and each reply.
var streamReplyTimeout = 30 * time.Second

// closeFor records why this side is closing the stream, then closes it.
func (d *driver) closeFor(reason string, close func()) {
	d.closedFor.CompareAndSwap(nil, &reason)
	close()
}

func (d *driver) frame(v any) error {
	line, err := d.read.ReadSlice('\n')
	if err != nil || len(line) > ResponseLimit || !utf8.Valid(line) {
		return d.frameError(line, err)
	}
	if json.Unmarshal(line, v) != nil {
		return errors.New("invalid managed chat stream JSON")
	}
	return nil
}

// frameError says why a frame could not be read, so that a stopped view can
// tell a backend failure, whose error the transport carries, from a clean
// end, a deadline this side enforced, or a malformed frame.
func (d *driver) frameError(line []byte, err error) error {
	switch reason := d.closedFor.Load(); {
	case reason != nil:
		return errors.New("managed chat stream " + *reason)
	case errors.Is(err, bufio.ErrBufferFull) || len(line) > ResponseLimit:
		return fmt.Errorf("managed chat stream sent a frame over %d bytes", ResponseLimit)
	case errors.Is(err, io.EOF) && len(line) == 0:
		return errors.New("managed chat stream ended")
	case errors.Is(err, io.EOF):
		return errors.New("managed chat stream ended inside a frame")
	case err != nil:
		return fmt.Errorf("managed chat stream failed: %w", err)
	}
	return errors.New("managed chat stream sent a frame that is not UTF-8")
}

// localRejection is a request this side refused before invoking the stream
// writer, so no byte of it reached the peer. Only exchange's own encoding and
// allowlist create it; a Write, reply or peer failure never does, whatever
// its text.
type localRejection struct{ err error }

func (e *localRejection) Error() string { return e.err.Error() }
func (e *localRejection) Unwrap() error { return e.err }

func (d *driver) exchange(request Request) (envelope, error) {
	if d.exchangeFn != nil {
		return d.exchangeFn(request)
	}
	var reply envelope
	d.serial++
	request.ID = fmt.Sprintf("q%d", d.serial)
	line, err := json.Marshal(request)
	if err != nil {
		return reply, &localRejection{err}
	}
	allowed := d.allowedChannel
	if allowed == nil {
		allowed = func(name string) bool { return name == d.header.Channel }
	}
	if _, err := decodeChannelRequest(line, allowed, d.header.Attachments); err != nil {
		return reply, &localRejection{err}
	}
	timeout := streamReplyTimeout // read once, before any callback can run
	timer := time.AfterFunc(timeout, func() {
		d.closeFor(fmt.Sprintf("sent no reply within %v", timeout), func() { _ = d.in.Close(); _ = d.out.Close() })
	})
	defer timer.Stop()
	if _, err := d.out.Write(append(line, '\n')); err != nil {
		return reply, err
	}
	if err := d.frame(&reply); err != nil {
		return reply, err
	}
	if reply.Event == "closed" {
		if reply.ServerError == TakenOverReason {
			return reply, ErrTakenOver
		}
		return reply, errors.New("AIRC connection closed; inspect pending sends before reconnecting")
	}
	if reply.Event != "" || reply.ID != request.ID {
		return reply, errors.New("uncorrelated AIRC response")
	}
	return reply, nil
}

func (d *driver) send(s Send) error {
	if s.Channel == "" {
		s.Channel = d.header.Channel
	}
	request := Request{ID: "check", Op: "send", Target: d.header.Channel, Text: s.Text, ReplyTo: s.ReplyTo, Attach: s.Attach}
	raw, _ := json.Marshal(request)
	_, invalid := decodeChannelRequest(raw, func(name string) bool { return name == d.header.Channel }, d.header.Attachments)
	if d.pending != nil || invalid != nil || !validReference(s.ID, 80) || s.Channel != d.header.Channel {
		d.emit(Event{Kind: "delivery", ID: s.ID, State: "rejected", Err: errors.New("message was not sent: invalid text or another send is pending")})
		return nil
	}
	d.pending = &pendingSend{Send: s}
	reply, err := d.exchange(request)
	var refused *localRejection
	if errors.As(err, &refused) {
		// Nothing was written, so the send definitely did not happen. The
		// stream stays up; only an explicit new send may try again.
		d.pending = nil
		d.emit(Event{Kind: "delivery", ID: s.ID, State: "rejected", Err: fmt.Errorf("message was not sent: this view refused the request before writing it (%w)", refused)})
		return nil
	}
	if err != nil {
		return err
	}
	if !reply.OK {
		if !strings.HasPrefix(reply.Error, "not sent:") {
			return errors.New("AIRC send outcome is uncertain; inspect history before resending")
		}
		d.pending = nil
		d.emit(Event{Kind: "delivery", ID: s.ID, State: "rejected", Err: errors.New(reply.Error)})
		return nil
	}
	var accepted struct{ Receipt, State string }
	if json.Unmarshal(reply.Result, &accepted) != nil || accepted.State != "pending" || !validReference(accepted.Receipt, 80) {
		return errors.New("invalid AIRC send receipt; delivery is uncertain")
	}
	d.pending.Receipt = accepted.Receipt
	return nil
}

// loadOlder requests the page before the oldest displayed message. Only one
// older page is outstanding at a time; its batch is matched by request order.
func (d *driver) loadOlder(before string) error {
	if d.historyLost {
		d.emit(Event{Kind: "history-page", Count: -1})
		d.emit(Event{Kind: "gap", Err: errors.New("older history is off after an event gap; rerun yoloctl chat to page further")})
		return nil
	}
	if d.olderPending() || !validReference(before, 128) {
		return nil
	}
	reply, err := d.exchange(Request{Op: "history", Channel: d.header.Channel, Direction: "BEFORE", Reference: "msgid=" + before, Limit: 50})
	if err != nil {
		return err
	}
	if !reply.OK {
		d.emit(Event{Kind: "gap", Err: errors.New("older history request was rejected")})
		d.emit(Event{Kind: "history-page", Count: -1})
		return nil
	}
	d.olderRequests++
	return nil
}

// historyGap forgets history requests and batches whose events may be lost.
// A pending older page completes as unavailable, so paging is not stuck.
func (d *driver) historyGap() {
	if d.olderPending() {
		d.emit(Event{Kind: "history-page", Count: -1})
	}
	for id, batch := range d.batches {
		if batch.gapped {
			delete(d.batches, id) // its close was lost in an earlier gap
		}
		batch.gapped = true
	}
	d.initialHistory, d.olderRequests, d.historyLost = false, 0, true
	d.emit(Event{Kind: "gap", Err: errors.New("AIRC event buffer has a gap; missing history is not proof of failed delivery")})
}

// olderPending reports an older page that is queued or still open.
func (d *driver) olderPending() bool {
	if d.olderRequests > 0 {
		return true
	}
	for _, batch := range d.batches {
		if batch.older && !batch.gapped {
			return true
		}
	}
	return false
}

// nextHistory resolves the oldest accepted history request; the server
// answers them in order with either a chathistory batch or FAIL CHATHISTORY.
func (d *driver) nextHistory() (older bool) {
	if d.initialHistory {
		d.initialHistory = false
		return false
	}
	if d.olderRequests > 0 {
		d.olderRequests--
		return true
	}
	return false
}

func (d *driver) members() {
	list := make([]Member, 0, len(d.roster))
	for _, m := range d.roster {
		list = append(list, m)
	}
	slices.SortFunc(list, func(a, b Member) int { return strings.Compare(strings.ToLower(a.Nick), strings.ToLower(b.Nick)) })
	d.emit(Event{Kind: "members", Members: list})
}

// maxTopicBytes bounds a displayed topic; AIRC itself accepts at most 200.
const maxTopicBytes = 512

// topicEvent reports the legacy single-channel stream's topic: RPL_NOTOPIC,
// RPL_TOPIC and RPL_TOPICWHOTIME on joining, and live TOPIC changes. Other
// channels are ignored; RunChannels reports topics per channel instead.
func (d *driver) topicEvent(r record) {
	e := Event{Kind: "topic", Topic: d.topic}
	switch r.Command {
	case "331":
		if len(r.Params) < 2 || r.Params[1] != d.header.Channel {
			return
		}
		e.Topic = ""
	case "332":
		if len(r.Params) < 3 || r.Params[1] != d.header.Channel {
			return
		}
		e.Topic = r.Params[2]
	case "333":
		if len(r.Params) < 3 || r.Params[1] != d.header.Channel {
			return
		}
		e.TopicBy, _ = prefixIdentity(r.Params[2])
	case "TOPIC":
		if len(r.Params) < 2 || r.Params[0] != d.header.Channel {
			return
		}
		e.Topic, e.TopicLive = r.Params[1], true
		e.TopicBy, _ = prefixIdentity(r.Prefix)
	}
	if len(e.Topic) > maxTopicBytes || e.TopicBy != "" && !validReference(e.TopicBy, 64) {
		return
	}
	d.topic = e.Topic
	d.emit(e)
}

// prefixIdentity splits AIRC's authenticated `nick!account@airc` prefix.
func prefixIdentity(prefix string) (nick, account string) {
	nick, rest, _ := strings.Cut(prefix, "!")
	account, _, _ = strings.Cut(rest, "@")
	return nick, account
}

// roster applies one membership event; it reports whether the roster changed.
func (d *driver) rosterEvent(r record) bool {
	nick, account := prefixIdentity(r.Prefix)
	key := strings.ToLower(nick)
	switch r.Command {
	case "353":
		// RPL_NAMREPLY: me, symbol, channel, space-separated [@+]nicks.
		if len(r.Params) == 4 && r.Params[2] == d.header.Channel {
			for _, name := range strings.Fields(r.Params[3]) {
				nick := strings.TrimLeft(name, "@+")
				roles := name[:len(name)-len(nick)]
				m := Member{Nick: nick, Op: strings.Contains(roles, "@"), Voice: strings.Contains(roles, "+")}
				if len(d.names) < maxRoster && validReference(m.Nick, 64) {
					d.names = append(d.names, m)
				}
			}
		}
		return false
	case "366":
		if len(r.Params) < 2 || r.Params[1] != d.header.Channel {
			return false
		}
		previous := d.roster
		d.roster = map[string]Member{}
		for _, m := range d.names {
			if old, ok := previous[strings.ToLower(m.Nick)]; ok {
				m.Account, m.Away, m.AwayReason = old.Account, old.Away, old.AwayReason
			}
			d.roster[strings.ToLower(m.Nick)] = m
		}
		d.names = nil
		return true
	case "352":
		// RPL_WHOREPLY: me, channel, account, host, server, nick, H|G[@+], …
		if len(r.Params) < 7 || r.Params[1] != d.header.Channel || !validReference(r.Params[5], 64) {
			return false
		}
		key := strings.ToLower(r.Params[5])
		m, ok := d.roster[key]
		if !ok && len(d.roster) >= maxRoster {
			return false
		}
		flags := r.Params[6]
		m.Nick, m.Account = r.Params[5], r.Params[2]
		m.Away = strings.HasPrefix(flags, "G")
		m.Op, m.Voice = strings.Contains(flags, "@"), strings.Contains(flags, "+")
		d.roster[key] = m
		return false // reported once at 315
	case "315":
		return len(r.Params) >= 2 && r.Params[1] == d.header.Channel
	case "AWAY":
		m, ok := d.roster[key]
		if !ok {
			return false
		}
		m.Away = len(r.Params) > 0
		m.AwayReason = ""
		if m.Away {
			m.AwayReason = r.Params[0]
			if len([]rune(m.AwayReason)) > 120 {
				m.AwayReason = string([]rune(m.AwayReason)[:120]) + "…"
			}
		}
		d.roster[key] = m
		return true
	case "JOIN":
		if len(r.Params) == 0 || r.Params[0] != d.header.Channel || nick == "" || len(d.roster) >= maxRoster {
			return false
		}
		d.roster[key] = Member{Nick: nick, Account: account}
		return true
	case "PART":
		if len(r.Params) == 0 || r.Params[0] != d.header.Channel {
			return false
		}
		delete(d.roster, key)
		return true
	case "QUIT":
		_, ok := d.roster[key]
		delete(d.roster, key)
		return ok
	case "KICK":
		if len(r.Params) < 2 || r.Params[0] != d.header.Channel {
			return false
		}
		delete(d.roster, strings.ToLower(r.Params[1]))
		return true
	case "NICK":
		m, ok := d.roster[key]
		if !ok || len(r.Params) == 0 {
			return false
		}
		delete(d.roster, key)
		m.Nick = r.Params[0]
		d.roster[strings.ToLower(m.Nick)] = m
		return true
	case "MODE":
		// Only channel role changes for one nickname: MODE #space +o nick.
		if len(r.Params) != 3 || r.Params[0] != d.header.Channel || len(r.Params[1]) != 2 {
			return false
		}
		target := strings.ToLower(r.Params[2])
		m, ok := d.roster[target]
		if !ok {
			return false
		}
		on := r.Params[1][0] == '+'
		switch r.Params[1][1] {
		case 'o':
			m.Op = on
		case 'v':
			m.Voice = on
		default:
			return false
		}
		d.roster[target] = m
		return true
	}
	return false
}

func (d *driver) record(r record) error {
	if r.Event == "receipt" {
		p := d.pending
		if p == nil || r.Receipt != p.Receipt {
			return nil
		}
		if r.Target != d.header.Channel {
			return errors.New("AIRC receipt target mismatch")
		}
		switch r.State {
		case "confirmed":
			if !validReference(r.MsgID, 128) {
				return errors.New("AIRC confirmation lacks a message ID")
			}
		case "rejected", "uncertain":
		default:
			return errors.New("invalid AIRC receipt state")
		}
		e := Event{Kind: "delivery", ID: p.ID, MessageID: r.MsgID, State: r.State}
		if r.State != "confirmed" {
			e.Err = errors.New(r.Reason)
		}
		if r.State == "confirmed" && len(p.Attach) > 0 {
			// A matching echo, which precedes its receipt, would already have
			// resolved this send: images are confirmed only by that evidence.
			e.State, e.Err = "uncertain", errors.New("the room confirmed the message without a matching echo of its images; inspect history before sending again")
		}
		if r.State == "rejected" && len(p.Attach) > 0 {
			e.Refusal = ParseAttachmentRefusal(r.Reason)
		}
		d.last, d.pending = p, nil
		d.emit(e)
		return nil
	}
	if r.Event != "" {
		return errors.New("unsupported AIRC logical event")
	}
	if d.rosterEvent(r) {
		d.members()
	}
	switch r.Command {
	case "331", "332", "333", "TOPIC":
		d.topicEvent(r)
	case "BATCH":
		if len(r.Params) == 0 || len(r.Params[0]) < 2 {
			return errors.New("invalid AIRC history batch")
		}
		id := r.Params[0][1:]
		if r.Params[0][0] == '-' {
			if batch := d.batches[id]; batch != nil && batch.older && !batch.gapped {
				d.emit(Event{Kind: "history-page", Count: batch.messages})
			}
			delete(d.batches, id)
		} else if r.Params[0][0] == '+' && len(r.Params) >= 2 && r.Params[1] == "chathistory" {
			if len(d.batches) >= 16 {
				return errors.New("too many AIRC history batches")
			}
			d.batches[id] = &historyBatch{older: d.nextHistory(), discard: d.historyLost}
		}
	case "PRIVMSG", "NOTICE":
		if len(r.Params) != 2 || r.Params[0] != d.header.Channel {
			return nil
		}
		if len(r.Params[1]) > 256<<10 {
			return errors.New("AIRC logical message exceeds its bound")
		}
		id := r.Tags["msgid"]
		if !validReference(id, 128) {
			d.emit(Event{Kind: "gap", Err: errors.New("room message has no stable message ID")})
			return nil
		}
		batchID := r.Tags["batch"]
		batch := d.batches[batchID]
		if d.historyLost && batchID != "" && batch == nil {
			// The gap may have consumed this history batch's opener. Its tail
			// cannot be classified as live traffic or a newly requested page.
			return nil
		}
		if batch != nil && (batch.discard || batch.older && batch.gapped) {
			return nil
		}
		m := Message{ID: id, Account: r.Tags["account"], Nick: strings.SplitN(r.Prefix, "!", 2)[0], Text: r.Params[1], ReplyTo: r.Tags["+reply"], Replay: batch != nil, Older: batch != nil && batch.older}
		if r.Command == "PRIVMSG" && m.Account != "" {
			m.Artifact = artifactFromTags(r.Tags)
			m.Attachments = attachmentsFromTags(r.Tags, m.Text)
		}
		if batch != nil {
			batch.messages++
		}
		// An authenticated live message also reveals a roster member's account.
		if member, ok := d.roster[strings.ToLower(m.Nick)]; ok && !m.Replay && member.Account == "" && m.Account != "" {
			member.Account = m.Account
			d.roster[strings.ToLower(m.Nick)] = member
			d.members()
		}
		m.Time, _ = time.Parse(time.RFC3339Nano, r.Tags["time"])
		// The client tag alone is not authentication. It only links a verified
		// human echo whose complete target/text/reply match the submitted item.
		for _, p := range []*pendingSend{d.pending, d.last} {
			if p != nil && p.Receipt != "" && r.Tags["+airc-send"] == p.Receipt && m.Account == d.header.SelfAccount && m.Nick == d.header.Nick && m.Text == p.Text && m.ReplyTo == p.ReplyTo && SameAttachments(m.Attachments, p.Attach) {
				m.ClientID = p.ID
				if p == d.pending {
					// A full authenticated echo can survive a gap that dropped
					// its preceding receipt. Resolve both adapter and UI state;
					// otherwise the UI confirms while the adapter blocks forever.
					d.last, d.pending = p, nil
					d.emit(Event{Kind: "delivery", ID: p.ID, MessageID: m.ID, State: "confirmed"})
				}
				break
			}
		}
		d.emit(Event{Kind: "message", Message: m})
	case "ERROR":
		return errors.New("AIRC server closed this membership; reconnect only after inspecting state")
	case "FAIL":
		if len(r.Params) > 0 && r.Params[0] == "CHATHISTORY" {
			// An asynchronous refusal (e.g. HISTORY_LOST) completes its request.
			older := d.nextHistory()
			d.emit(Event{Kind: "gap", Err: errors.New("AIRC refused a history request; that history is unavailable")})
			if older {
				d.emit(Event{Kind: "history-page", Count: -1})
			}
			return nil
		}
		d.emit(Event{Kind: "gap", Err: errors.New("AIRC reported an operation failure; some history may be unavailable")})
	}
	return nil
}
