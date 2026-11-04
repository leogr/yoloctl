package chatstream

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

// channelStream drives RunChannels exactly as the controller relay does:
// trusted header, then the real `airc --jsonl 2` stdout; requests pass through
// FilterChannels into its stdin. The authorizer is a static trusted catalog
// here; the relay's native-manifest authorization is covered elsewhere.
type channelStream struct {
	events   chan Event
	outgoing chan Send
	older    chan HistoryRequest
	actions  chan Action
	done     chan error
	serial   int
	// close ends this view as a closing chat view does, then reaps its client.
	close func()
}

// realChannelFixture is a schema-2 room with #space and #build, a control
// socket for reloads, and a raw operator peer ("opus") in both channels.
type realChannelFixture struct {
	t                         *testing.T
	dir, bin                  string
	room                      *managedchat.Room
	header                    Header
	humanAccount, peerAccount string
}

func newRealChannelFixture(t *testing.T) *realChannelFixture {
	bin := os.Getenv("YOLOCTL_TEST_AIRC_BIN_DIR")
	if bin == "" {
		t.Skip("set YOLOCTL_TEST_AIRC_BIN_DIR to the directory holding aircd and airc")
	}
	// Unix socket paths must stay short.
	dir, err := os.MkdirTemp("", "yc-chs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	room := &managedchat.Room{Schema: 2, ID: managedchat.NewID(), DiskID: managedchat.NewID(), Generation: 1, Channel: managedchat.DefaultChannel,
		Human: managedchat.Member{ID: managedchat.NewID(), Name: "leogr"}, Members: []managedchat.Member{{ID: managedchat.NewID(), Name: "opus", BoxID: managedchat.NewID()}}}
	members := []string{room.Human.ID, room.Members[0].ID}
	slices.Sort(members)
	room.Channels = []managedchat.Channel{
		{ID: managedchat.LegacyChannelID(room.ID), Name: managedchat.DefaultChannel, Members: members},
		{ID: managedchat.NewID(), Name: "#build", Members: members},
	}
	header, err := RoomHeader(room)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := managedchat.AuthAccount(room.Members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	f := &realChannelFixture{t: t, dir: dir, bin: bin, room: room, header: header, humanAccount: header.SelfAccount, peerAccount: peer}
	for _, name := range []string{"endpoint", "control"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.write("token", aircTestToken)
	f.serverConfig(1, []string{f.humanAccount, f.peerAccount}, []string{f.humanAccount, f.peerAccount})
	server := exec.Command(filepath.Join(bin, "aircd"), "serve", "--config", filepath.Join(dir, "server.toml"))
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = server.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = server.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("aircd fixture did not stop")
		}
	})
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := f.control().Call(t.Context(), "status"); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("aircd exited before its control socket was ready")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("aircd control socket did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.write("human.toml", fmt.Sprintf("unix = \"endpoint/irc.sock\"\naccount = %q\nnick = %q\ntoken_file = \"token\"\nchannels = [\"#space\"]\n", f.humanAccount, header.Nick))
	return f
}

func (f *realChannelFixture) write(name, text string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// serverConfig writes the AIRC server document with the managed server name
// the driver requires. #build's ACL and operators vary; #space never does.
func (f *realChannelFixture) serverConfig(generation uint64, build, operators []string) {
	sum := sha256.Sum256([]byte(aircTestToken))
	hash := hex.EncodeToString(sum[:])
	list := func(accounts []string) string {
		quoted := make([]string, len(accounts))
		for i, account := range accounts {
			quoted[i] = fmt.Sprintf("%q", account)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}
	f.write("server.toml", fmt.Sprintf(`server_name = %q
listen_unix = "endpoint/irc.sock"
control_unix = "control/control.sock"
membership_generation = %d
database = "airc.sqlite3"

[[accounts]]
name = %q
token_sha256 = %q

[[accounts]]
name = %q
token_sha256 = %q

[[channels]]
name = "#space"
members = [%q, %q]
operators = [%q]

[[channels]]
name = "#build"
members = %s
operators = %s
`, "yoloctl-"+f.room.ID[:12], generation, f.humanAccount, hash, f.peerAccount, hash, f.humanAccount, f.peerAccount, f.peerAccount, list(build), list(operators)))
}

func (f *realChannelFixture) control() managedchat.Control {
	return managedchat.Control{Socket: filepath.Join(f.dir, "control", "control.sock"), VerifyPeer: func(*net.UnixConn) error {
		return nil // Local test stub only; this is not live-container proof.
	}}
}

func (f *realChannelFixture) peer(channels ...string) *peer {
	conn, err := net.Dial("unix", filepath.Join(f.dir, "endpoint", "irc.sock"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	p := &peer{conn: conn, read: bufio.NewReader(conn)}
	p.send("PASS "+f.peerAccount+":"+aircTestToken, "NICK opus", "USER opus 0 * :peer")
	for _, channel := range channels {
		p.send("JOIN " + channel)
		p.until(f.t, " 366 opus "+channel+" ")
	}
	return p
}

func (f *realChannelFixture) human() *channelStream {
	return f.stream(f.header, exec.Command(filepath.Join(f.bin, "airc"), "--config", filepath.Join(f.dir, "human.toml"), "--jsonl", "2"))
}

// stream drives RunChannels over one client process with its trusted header.
func (f *realChannelFixture) stream(header Header, client *exec.Cmd) *channelStream {
	stdin, err := client.StdinPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	stdout, err := client.StdoutPipe()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		f.t.Fatal(err)
	}
	headerLine, _ := json.Marshal(header)
	responses, responseWriter := io.Pipe()
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_, _ = responseWriter.Write(append(headerLine, '\n'))
		_, err := io.Copy(responseWriter, stdout)
		_ = responseWriter.CloseWithError(err)
		_ = client.Wait() // only after its stdout reads, as exec.Cmd requires
	}()
	trusted := map[string]bool{}
	for _, channel := range header.Channels {
		trusted[channel.Name] = true
	}
	requests, requestWriter := io.Pipe()
	go func() {
		err := FilterChannels(requests, stdin, func(channel string) error {
			if !trusted[channel] {
				return errors.New("channel outside the trusted catalog")
			}
			return nil
		}, false)
		_ = stdin.Close()
		_ = requests.CloseWithError(err)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	h := &channelStream{events: make(chan Event, 1024), outgoing: make(chan Send, 1), older: make(chan HistoryRequest, 1), actions: make(chan Action, 1), done: make(chan error, 1)}
	go func() {
		h.done <- RunChannels(ctx, responses, requestWriter, h.outgoing, h.older, h.actions, nil, func(e Event) { h.events <- e })
	}()
	var once sync.Once
	h.close = func() {
		once.Do(func() {
			// The closed stream ends the client's input; kill only a stuck one.
			cancel()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				_ = client.Process.Kill()
				<-exited
			}
		})
	}
	f.t.Cleanup(h.close)
	return h
}

func (h *channelStream) next(t *testing.T, what string, match func(Event) bool) Event {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case e := <-h.events:
			if e.Kind == "gap" && e.Err != nil && strings.Contains(e.Err.Error(), "(422)") {
				t.Fatalf("real AIRC No MOTD was misclassified as a history gap: %v", e.Err)
			}
			if match(e) {
				return e
			}
		case err := <-h.done:
			t.Fatalf("channel stream ended before %s: %v", what, err)
		case <-timeout:
			t.Fatalf("channel stream did not report %s", what)
		}
	}
}

// act submits a channel action. A local refusal while setup is pending was
// never sent, so a fresh ID may retry it; any other outcome is returned.
func (h *channelStream) act(t *testing.T, kind, channel string, topic *string) Event {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; {
		h.serial++
		id := fmt.Sprintf("%s-%d", kind, h.serial)
		h.actions <- Action{ID: id, Kind: kind, Channel: channel, Topic: topic}
		e := h.next(t, "control "+id, func(e Event) bool { return e.Kind == "control" && e.ID == id })
		if e.State != "refused" || e.Err == nil || !strings.Contains(e.Err.Error(), "pending") || time.Now().After(deadline) {
			return e
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deliver sends text and retries only local not-sent rejections during setup.
func (h *channelStream) deliver(t *testing.T, channel, text string) Event {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; {
		h.serial++
		id := fmt.Sprintf("send-%d", h.serial)
		h.outgoing <- Send{ID: id, Channel: channel, Text: text}
		e := h.next(t, "delivery "+id, func(e Event) bool { return e.Kind == "delivery" && e.ID == id })
		if e.State != "rejected" || e.Err == nil || !strings.Contains(e.Err.Error(), "was not sent") || time.Now().After(deadline) {
			return e
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRealAIRCRunChannelsTopicHistoryAndACLRemoval(t *testing.T) {
	f := newRealChannelFixture(t)
	p := f.peer("#space", "#build")
	// Context that exists before the human joins #build: topic and history.
	p.send("TOPIC #build :T0 before join", "PRIVMSG #build :build one", "PRIVMSG #build :build two")
	p.until(t, "TOPIC #build T0 before join")
	h := f.human()
	h.next(t, "connection", func(e Event) bool { return e.Kind == "connection" && e.Connected })
	h.next(t, "startup #space membership", func(e Event) bool {
		return e.Kind == "membership" && e.Channel == "#space" && e.State == "joined"
	})

	// An explicit JOIN is confirmed by the authentic self JOIN; bootstrap
	// topic and the initial history page follow for that channel only.
	if e := h.act(t, "join", "#build", nil); e.State != "confirmed" {
		t.Fatalf("join was not confirmed: %+v", e)
	}
	h.next(t, "actual human operator mode in #build roster", func(e Event) bool {
		if e.Kind == "members" && e.Channel == "#build" {
			for _, member := range e.Members {
				if member.Account == f.humanAccount && member.Op {
					return true
				}
			}
		}
		return false
	})
	h.next(t, "bootstrap topic", func(e Event) bool {
		return e.Kind == "topic" && e.Channel == "#build" && e.State == "known" && e.Topic == "T0 before join" && !e.TopicLive
	})
	h.next(t, "initial #build history", func(e Event) bool {
		return e.Kind == "message" && e.Channel == "#build" && e.Message.Text == "build two" && e.Message.Replay
	})
	if e := h.act(t, "join", "#build", nil); e.State != "confirmed" {
		t.Fatalf("repeated join of a joined channel was not idempotent: %+v", e)
	}

	// A newer operator topic is live; a later query is correlated to its reply.
	p.send("TOPIC #build :T1 newer")
	h.next(t, "live topic", func(e Event) bool {
		return e.Kind == "topic" && e.Channel == "#build" && e.Topic == "T1 newer" && e.TopicLive
	})
	read := h.act(t, "topic-read", "#build", nil)
	if read.State != "confirmed" {
		t.Fatalf("topic query was not confirmed: %+v", read)
	}
	text := "T2 from human"
	if e := h.act(t, "topic-set", "#build", &text); e.State != "confirmed" {
		t.Fatalf("topic set was not confirmed: %+v", e)
	}
	p.until(t, "TOPIC #build T2 from human")
	if e := h.act(t, "topic-clear", "#build", nil); e.State != "confirmed" {
		t.Fatalf("topic clear was not confirmed: %+v", e)
	}
	if e := h.deliver(t, "#build", "from human"); e.State != "confirmed" {
		t.Fatalf("#build send was not confirmed: %+v", e)
	}
	p.until(t, "PRIVMSG #build from human")

	// The peer reloads a narrower #build ACL for the human: a KICK there,
	// no reconnect, #space stays usable and a rejoin is refused.
	f.serverConfig(2, []string{f.peerAccount}, []string{f.peerAccount})
	if _, err := f.control().Call(t.Context(), "reload"); err != nil {
		t.Fatalf("real AIRC refused the ACL reload: %v", err)
	}
	h.next(t, "kicked membership", func(e Event) bool {
		return e.Kind == "membership" && e.Channel == "#build" && e.State == "kicked"
	})
	if e := h.deliver(t, "#space", "space still works"); e.State != "confirmed" {
		t.Fatalf("#space send after the KICK was not confirmed: %+v", e)
	}
	p.until(t, "PRIVMSG #space space still works")
	h.serial++
	h.outgoing <- Send{ID: "after-kick", Channel: "#build", Text: "must not send"}
	if e := h.next(t, "local #build rejection", func(e Event) bool { return e.Kind == "delivery" && e.ID == "after-kick" }); e.State != "rejected" {
		t.Fatalf("send to a kicked channel was not refused locally: %+v", e)
	}
	if e := h.act(t, "join", "#build", nil); e.State != "refused" || e.Err == nil || !strings.Contains(e.Err.Error(), "403") {
		t.Fatalf("rejoin after ACL removal was not refused by AIRC: %+v", e)
	}
}
