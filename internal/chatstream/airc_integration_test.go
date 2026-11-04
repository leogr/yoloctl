package chatstream

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

// The real-client contract test drives the packaged AIRC JSONL v2 client
// through Run exactly as the controller stream does (trusted header, then the
// client's stdout; requests through Filter). It needs AIRC binaries, so it runs
// only when YOLOCTL_TEST_AIRC_BIN_DIR names a directory containing aircd/airc.

type aircFixture struct {
	t                         *testing.T
	dir, bin                  string
	room                      *managedchat.Room
	header                    Header
	humanAccount, peerAccount string
	server                    *exec.Cmd
}

const aircTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newAIRCFixture(t *testing.T) *aircFixture {
	bin := os.Getenv("YOLOCTL_TEST_AIRC_BIN_DIR")
	if bin == "" {
		t.Skip("set YOLOCTL_TEST_AIRC_BIN_DIR to the directory holding aircd and airc")
	}
	// Unix socket paths must stay short.
	dir, err := os.MkdirTemp("", "yc-airc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	room := &managedchat.Room{Schema: 1, ID: managedchat.NewID(), DiskID: managedchat.NewID(), Generation: 1, Channel: managedchat.DefaultChannel,
		Human: managedchat.Member{ID: managedchat.NewID(), Name: "leogr"}, Members: []managedchat.Member{{ID: managedchat.NewID(), Name: "opus", BoxID: managedchat.NewID()}}}
	header, err := RoomHeader(room)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := managedchat.AuthAccount(room.Members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	f := &aircFixture{t: t, dir: dir, bin: bin, room: room, header: header, humanAccount: header.SelfAccount, peerAccount: peer}
	sum := sha256.Sum256([]byte(aircTestToken))
	hash := hex.EncodeToString(sum[:])
	// The peer is the only operator, so it can deny the human with +m.
	server := fmt.Sprintf(`server_name = "airc.local"
listen_unix = "endpoint/irc.sock"
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
`, f.humanAccount, hash, peer, hash, f.humanAccount, peer, peer)
	f.write("server.toml", server)
	f.write("token", aircTestToken)
	if err := os.Mkdir(filepath.Join(dir, "endpoint"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.server = exec.Command(filepath.Join(bin, "aircd"), "serve", "--config", filepath.Join(dir, "server.toml"))
	stderr, err := f.server.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.server.Process.Signal(syscall.SIGCONT); _ = f.server.Process.Kill(); _ = f.server.Wait() })
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(stderr)
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "aircd listening on unix:") {
				ready <- true
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("aircd exited before listening")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("aircd did not start")
	}
	f.write("human.toml", fmt.Sprintf("unix = \"endpoint/irc.sock\"\naccount = %q\nnick = %q\ntoken_file = \"token\"\nchannels = [\"#space\"]\n", f.humanAccount, header.Nick))
	return f
}

func (f *aircFixture) write(name, text string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// peer is a raw IRC participant speaking draft/multiline itself.
type peer struct {
	conn net.Conn
	read *bufio.Reader
}

func (f *aircFixture) peer() *peer {
	conn, err := net.Dial("unix", filepath.Join(f.dir, "endpoint", "irc.sock"))
	if err != nil {
		f.t.Fatal(err)
	}
	p := &peer{conn: conn, read: bufio.NewReader(conn)}
	p.send("CAP REQ :batch draft/multiline message-tags echo-message", "PASS "+f.peerAccount+":"+aircTestToken, "NICK opus", "USER opus 0 * :peer", "CAP END", "JOIN #space")
	p.until(f.t, " 366 ")
	return p
}

func (p *peer) send(lines ...string) {
	for _, line := range lines {
		_, _ = io.WriteString(p.conn, line+"\r\n")
	}
}

func (p *peer) until(t *testing.T, needle string) string {
	_ = p.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := p.read.ReadString('\n')
		if err != nil {
			t.Fatalf("peer did not observe %q: %v", needle, err)
		}
		// The last parameter may be encoded with a leading ':' (BATCH :-m1).
		if strings.Contains(strings.ReplaceAll(line, " :", " "), needle) {
			return line
		}
	}
}

func (p *peer) multiline(t *testing.T, ref string, lines ...string) {
	p.send("BATCH +" + ref + " draft/multiline #space")
	for _, line := range lines {
		p.send("@batch=" + ref + " PRIVMSG #space :" + line)
	}
	p.send("BATCH -" + ref)
	p.until(t, "BATCH -")
}

type humanStream struct {
	events   chan Event
	outgoing chan Send
	cancel   context.CancelFunc
	client   *exec.Cmd
	done     chan error
}

// human starts the confined client path: header + airc stdout to Run, Run's
// requests through Filter into airc stdin.
func (f *aircFixture) human() *humanStream {
	client := exec.Command(filepath.Join(f.bin, "airc"), "--config", filepath.Join(f.dir, "human.toml"), "--jsonl", "2")
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
	headerLine, _ := json.Marshal(f.header)
	responses, responseWriter := io.Pipe()
	go func() {
		_, _ = responseWriter.Write(append(headerLine, '\n'))
		_, err := io.Copy(responseWriter, stdout)
		_ = responseWriter.CloseWithError(err)
	}()
	requests, requestWriter := io.Pipe()
	go func() {
		err := Filter(requests, stdin, f.header.Channel)
		_ = stdin.Close()
		_ = requests.CloseWithError(err)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	h := &humanStream{events: make(chan Event, 256), outgoing: make(chan Send, 1), cancel: cancel, client: client, done: make(chan error, 1)}
	go func() {
		h.done <- Run(ctx, responses, requestWriter, h.outgoing, nil, func(e Event) { h.events <- e })
	}()
	f.t.Cleanup(func() { cancel(); _ = client.Process.Kill(); _ = client.Wait() })
	return h
}

func (h *humanStream) next(t *testing.T, match func(Event) bool) Event {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case e := <-h.events:
			if match(e) {
				return e
			}
		case <-timeout:
			t.Fatal("expected chat stream event not observed")
		}
	}
}

func delivery(id string) func(Event) bool {
	return func(e Event) bool { return e.Kind == "delivery" && e.ID == id }
}

func TestRealAIRCClientContract(t *testing.T) {
	f := newAIRCFixture(t)
	p := f.peer()
	// History seeded before the human connects arrives assembled and as replay.
	p.multiline(t, "seed", "seeded line one", "", "    indented code")
	h := f.human()
	h.next(t, func(e Event) bool { return e.Kind == "connection" && e.Connected })
	seed := h.next(t, func(e Event) bool { return e.Kind == "message" })
	if seed.Message.Text != "seeded line one\n\n    indented code" || !seed.Message.Replay || seed.Message.Account != f.peerAccount {
		t.Fatalf("history was not one assembled replayed message: %+v", seed.Message)
	}
	// A multiline send is confirmed by its complete echo and receipt.
	text := "Plan:\n```go\nfmt.Println(\"é漢🙂\")\n```"
	h.outgoing <- Send{ID: "s1", Text: text}
	confirmed := h.next(t, delivery("s1"))
	if confirmed.State != "confirmed" || confirmed.MessageID == "" {
		t.Fatalf("multiline send not confirmed: %+v", confirmed)
	}
	// A 64 KiB fenced message round-trips byte-exact within negotiated limits.
	var b strings.Builder
	b.WriteString("```text\n")
	for b.Len() < MaxMessageBytes-200 {
		fmt.Fprintf(&b, "%04d %s\n", b.Len()%10000, strings.Repeat("x", 90))
	}
	b.WriteString("```")
	big := b.String()
	h.outgoing <- Send{ID: "s2", Text: big}
	if e := h.next(t, delivery("s2")); e.State != "confirmed" {
		t.Fatalf("64 KiB message not confirmed: %+v", e)
	}
	got := p.until(t, "```text")
	if !strings.Contains(got, "@") {
		t.Fatalf("peer did not receive the batch opener tags: %q", got)
	}
	// Oversize and too many lines are refused before sending; the view stays.
	h.outgoing <- Send{ID: "s3", Text: strings.Repeat("y", MaxMessageBytes+1)}
	if e := h.next(t, delivery("s3")); e.State != "rejected" {
		t.Fatalf("oversized text was not rejected locally: %+v", e)
	}
	h.outgoing <- Send{ID: "s4", Text: strings.Repeat("z\n", 1100)}
	if e := h.next(t, delivery("s4")); e.State != "rejected" || !strings.Contains(e.Err.Error(), "lines") {
		t.Fatalf("too many lines were not rejected: %+v", e)
	}
	// A server-side denial is a rejected receipt and the stream continues.
	p.send("MODE #space +m")
	p.until(t, "MODE #space +m")
	h.outgoing <- Send{ID: "s5", Text: "denied\nwhile moderated"}
	if e := h.next(t, delivery("s5")); e.State != "rejected" {
		t.Fatalf("moderated send was not a rejected receipt: %+v", e)
	}
	p.send("MODE #space -m")
	p.until(t, "MODE #space -m")
	h.outgoing <- Send{ID: "s6", Text: "after moderation"}
	if e := h.next(t, delivery("s6")); e.State != "confirmed" {
		t.Fatalf("stream did not continue after a rejection: %+v", e)
	}
	// A send left without its echo becomes uncertain when the client ends.
	if err := f.server.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	h.outgoing <- Send{ID: "s7", Text: "never echoed"}
	time.Sleep(500 * time.Millisecond)
	_ = h.client.Process.Kill()
	if e := h.next(t, delivery("s7")); e.State != "uncertain" {
		t.Fatalf("lost send was not uncertain: %+v", e)
	}
	if err := <-h.done; err == nil {
		t.Fatal("stream ended without reporting the lost connection")
	}
}
