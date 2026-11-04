package managedchat

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/pelletier/go-toml/v2"
)

// ircPeer is a raw IRC session authenticated with a member's private token.
type ircPeer struct {
	t    *testing.T
	nick string
	conn net.Conn
	read *bufio.Reader
}

func dialIRC(t *testing.T, socket, account string, token []byte, nick string, channels ...string) *ircPeer {
	t.Helper()
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &ircPeer{t: t, nick: nick, conn: conn, read: bufio.NewReader(conn)}
	p.send("PASS "+account+":"+string(token), "NICK "+nick, "USER "+nick+" 0 * :channel test")
	for _, channel := range channels {
		p.send("JOIN " + channel)
		p.until(" 366 " + nick + " " + channel + " ")
	}
	return p
}

func (p *ircPeer) send(lines ...string) {
	p.t.Helper()
	for _, line := range lines {
		if _, err := io.WriteString(p.conn, line+"\r\n"); err != nil {
			p.t.Fatal(err)
		}
	}
}

// until reads lines (bounded by a deadline) until one contains needle.
func (p *ircPeer) until(needle string) string {
	p.t.Helper()
	if err := p.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		p.t.Fatal(err)
	}
	for {
		line, err := p.read.ReadString('\n')
		if err != nil {
			p.t.Fatalf("IRC peer did not observe %q: %v", needle, err)
		}
		if strings.Contains(strings.ReplaceAll(line, " :", " "), needle) {
			return line
		}
	}
}

// role checks independent wire evidence: NAMES and WHO agree about actual +o.
// Fixtures below have at most three peers, so their NAMES fits one reply.
func (p *ircPeer) role(channel, nick string, want bool) {
	p.t.Helper()
	p.send("NAMES " + channel)
	line := p.until(" 353 " + p.nick + " = " + channel + " ")
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[1] != "353" || fields[4] != channel {
		p.t.Fatalf("invalid NAMES reply: %q", line)
	}
	found := false
	for _, field := range fields[5:] {
		field = strings.TrimPrefix(field, ":")
		if strings.TrimLeft(field, "@+") == nick {
			found = true
			if strings.HasPrefix(field, "@") != want {
				p.t.Fatalf("NAMES role for %s in %s: %q (want op=%v)", nick, channel, field, want)
			}
		}
	}
	if !found {
		p.t.Fatalf("NAMES omitted %s in %s", nick, channel)
	}
	p.until(" 366 " + p.nick + " " + channel + " ")
	p.send("WHO " + channel)
	line = p.until(" " + nick + " H")
	fields = strings.Fields(line)
	if len(fields) < 9 || fields[1] != "352" || fields[3] != channel || fields[7] != nick || strings.Contains(fields[8], "@") != want {
		p.t.Fatalf("WHO role for %s in %s: %q (want op=%v)", nick, channel, line, want)
	}
	p.until(" 315 " + p.nick + " " + channel + " ")
}

// This opt-in check drives production room, channel-catalog and ACL rendering
// against a real aircd. Only the container mount paths in the rendered server
// document are relocated to a private temporary directory, so the expected
// digest covers those relocated bytes. Peer verification is a local stub; it
// is not container/UID proof, which remains a host acceptance item.
func TestRealAIRCChannelAdditionAndACLRemovalWithoutRevocation(t *testing.T) {
	bin := os.Getenv("YOLOCTL_TEST_AIRC_BIN_DIR")
	if bin == "" {
		t.Skip("set YOLOCTL_TEST_AIRC_BIN_DIR to the directory holding aircd")
	}
	s, inventory, r, _ := renderedFixture(t)
	coderID := r.Members[0].ID
	inventory.Declarations["plain"] = NewID()
	r, err := s.Enroll(Box{Name: "plain", ID: inventory.Declarations["plain"]}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SetOperators(map[string]bool{coderID: true}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	// Unix socket paths must stay short even under a long worktree path.
	dir, err := os.MkdirTemp("", "yc-chan-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{"endpoint", "control"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "server.toml")
	write := func(doc serverDocument) string {
		t.Helper()
		raw, err := toml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	base, err := layout.ChatRoomRelative(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var published serverDocument
	publish := func(r *Room) Rendered {
		t.Helper()
		proof, err := s.Render(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(s.Disk, base, "server/config", ServerConfigFile))
		if err != nil {
			t.Fatal(err)
		}
		published = serverDocument{}
		if err := toml.Unmarshal(raw, &published); err != nil {
			t.Fatal(err)
		}
		published.ListenUnix = filepath.Join(dir, "endpoint", "irc.sock")
		published.ControlUnix = filepath.Join(dir, "control", "control.sock")
		published.Database = filepath.Join(dir, "airc.sqlite3")
		published.Artifacts.ListenUnix = filepath.Join(dir, "endpoint", "artifacts.sock")
		proof.ConfigSHA256 = write(published)
		return proof
	}
	first := publish(r)
	stderr, err := os.OpenFile(filepath.Join(dir, "aircd.stderr"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	server := exec.Command(filepath.Join(bin, "aircd"), "serve", "--config", configPath)
	server.Stderr = stderr
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
	control := Control{Socket: published.ControlUnix, VerifyPeer: func(*net.UnixConn) error {
		return nil // Local test stub only; this is not live-container proof.
	}}
	var initial Applied
	for deadline := time.Now().Add(10 * time.Second); ; {
		if initial, err = control.Call(t.Context(), "status"); err == nil {
			break
		}
		select {
		case <-done:
			t.Fatal("aircd exited before its control socket was ready")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("aircd control socket did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := initial.Matches(first); err != nil {
		t.Fatalf("real AIRC status did not prove the rendered room: %v", err)
	}
	if err := initial.Require("channel-add-reload"); err != nil {
		t.Fatal(err)
	}

	nicks, err := RoomNicknames(r)
	if err != nil {
		t.Fatal(err)
	}
	coder := r.Members[0]
	plain := r.Members[1]
	coderAccount, _ := AuthAccount(coder.ID)
	humanAccount, _ := AuthAccount(r.Human.ID)
	_, coderToken := credential(t, s, r, coder.ID)
	_, humanToken := credential(t, s, r, r.Human.ID)
	plainAccount, _ := AuthAccount(plain.ID)
	_, plainToken := credential(t, s, r, plain.ID)
	coderNick, humanNick := nicks[coder.ID], nicks[r.Human.ID]
	member := dialIRC(t, published.ListenUnix, coderAccount, coderToken, coderNick, DefaultChannel)
	human := dialIRC(t, published.ListenUnix, humanAccount, humanToken, humanNick, DefaultChannel)
	ordinary := dialIRC(t, published.ListenUnix, plainAccount, plainToken, nicks[plain.ID], DefaultChannel)
	human.role(DefaultChannel, humanNick, true)
	human.role(DefaultChannel, coderNick, true)
	human.role(DefaultChannel, ordinary.nick, false)

	// Addition: the production catalog gains #build for every active member.
	r, err = s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	added := publish(r)
	if len(published.Channels) != 2 || published.Channels[1].Name != "#build" {
		t.Fatalf("rendered catalog did not add #build: %+v", published.Channels)
	}
	reload := func(want Rendered) Applied {
		t.Helper()
		ack, err := control.Call(t.Context(), "reload")
		if err != nil {
			t.Fatalf("real AIRC refused the rendered reload: %v", err)
		}
		if err := ack.MatchesReload(want, initial); err != nil {
			t.Fatal(err)
		}
		status, err := control.Call(t.Context(), "status")
		if err != nil {
			t.Fatal(err)
		}
		if err := status.MatchesIncarnation(want, initial); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if status := reload(added); status.Accounts[coderAccount].Connections != 1 || status.Accounts[humanAccount].Connections != 1 {
		t.Fatal("channel addition disturbed connected sessions")
	}
	// An ordinary first join does not receive +o. Configured human/operator
	// grants apply to every successful JOIN, including a channel added live.
	ordinary.send("JOIN #build")
	ordinary.until(" 366 " + ordinary.nick + " #build ")
	ordinary.role("#build", ordinary.nick, false)
	member.send("JOIN #build")
	member.until(" MODE #build +o " + coderNick)
	member.until(" 366 " + coderNick + " #build ")
	human.send("JOIN #build")
	human.until(" MODE #build +o " + humanNick)
	human.until(" 366 " + humanNick + " #build ")
	human.role("#build", coderNick, true)
	human.role("#build", humanNick, true)
	for _, peer := range []*ircPeer{human, member} {
		peer.send("PART #build")
		peer.until(" PART #build")
		peer.send("JOIN #build")
		peer.until(" MODE #build +o " + peer.nick)
		peer.until(" 366 " + peer.nick + " #build ")
		peer.role("#build", peer.nick, true)
	}
	for _, grant := range []bool{false, true} {
		operators := map[string]bool{}
		if grant {
			operators[coderID] = true
		}
		r, err = s.SetOperators(operators, r.Generation, inventory)
		if err != nil {
			t.Fatal(err)
		}
		reload(publish(r))
		mode := "-o"
		if grant {
			mode = "+o"
		}
		for _, channel := range []string{DefaultChannel, "#build"} {
			member.until(" MODE " + channel + " " + mode + " " + coderNick)
		}
		for _, channel := range []string{DefaultChannel, "#build"} {
			member.role(channel, coderNick, grant)
			human.role(channel, humanNick, true)
			ordinary.role(channel, ordinary.nick, false)
		}
	}

	// Removal of one channel ACL: KICK there, no account revocation.
	build := r.ChannelCatalog()[1]
	r, err = s.SetChannelMember(r.ID, build.ID, coder.ID, coder.BoxID, false, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	removed := publish(r)
	if status := reload(removed); status.Accounts[coderAccount].Connections != 1 {
		t.Fatal("removing one channel ACL revoked the member's connection")
	}
	member.until("KICK #build " + coderNick)
	member.send("PRIVMSG #space :still in space")
	human.until("PRIVMSG #space still in space")
	member.send("JOIN #build")
	member.until(" 403 " + coderNick + " #build ")

	// A rendered-shape document that removes an applied channel (A -> A+B ->
	// A+C) is refused, and the applied proof stays unchanged.
	replaced := published
	replaced.Channels = append([]serverChannel(nil), published.Channels...)
	replaced.Channels[1].Name = "#other"
	replaced.Generation++
	write(replaced)
	if _, err := control.Call(t.Context(), "reload"); err == nil || !strings.Contains(err.Error(), "RESTART_REQUIRED") {
		t.Fatalf("real AIRC accepted removal of an applied channel: %v", err)
	}
	after, err := control.Call(t.Context(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if err := after.Matches(removed); err != nil || after.Incarnation != initial.Incarnation {
		t.Fatalf("refused reload changed the applied proof: %v", err)
	}

	// A channel-delete-reload server also deletes an explicitly listed
	// channel; an older one keeps only the refusal above.
	if initial.Require("channel-delete-reload") != nil {
		t.Log("aircd lacks channel-delete-reload; the deletion steps are skipped")
		return
	}
	human.send("TOPIC #build :build topic", "TOPIC #space :space topic")
	human.until(" TOPIC #build build topic")
	human.until(" TOPIC #space space topic")
	r, err = s.DeleteChannel(r.ID, build.ID, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	deleted := publish(r)
	if len(published.Channels) != 1 || !slices.Equal(published.RemovedChannels, []string{"#build"}) {
		t.Fatalf("rendered deletion lacks explicit intent: %+v %v", published.Channels, published.RemovedChannels)
	}
	if status := reload(deleted); status.Accounts[humanAccount].Connections != 1 || status.Accounts[plainAccount].Connections != 1 {
		t.Fatal("channel deletion disconnected sessions")
	}
	human.until("KICK #build " + humanNick + " Channel deleted")
	ordinary.until("KICK #build " + ordinary.nick + " Channel deleted")
	member.send("PRIVMSG #space :after deletion")
	human.until("PRIVMSG #space after deletion")
	human.send("JOIN #build")
	human.until(" 403 " + humanNick + " #build ")

	// A fresh server process on the same database keeps the deletion and the
	// other channel's stored state.
	_ = server.Process.Kill()
	<-done
	server = exec.Command(filepath.Join(bin, "aircd"), "serve", "--config", configPath)
	server.Stderr = stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done = make(chan struct{})
	go func() { _ = server.Wait(); close(done) }()
	previous := initial.Incarnation
	for deadline := time.Now().Add(10 * time.Second); ; {
		if initial, err = control.Call(t.Context(), "status"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted aircd control socket did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := initial.Matches(deleted); err != nil || initial.Incarnation == previous {
		t.Fatalf("restarted server does not prove the deletion generation: %v", err)
	}
	human = dialIRC(t, published.ListenUnix, humanAccount, humanToken, humanNick, DefaultChannel)
	human.send("TOPIC #space")
	human.until(" 332 " + humanNick + " #space space topic")
	human.send("JOIN #build")
	human.until(" 403 " + humanNick + " #build ")

	// Recreating the name, no longer listed for deletion, starts empty.
	r, err = s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	recreated := publish(r)
	if published.RemovedChannels != nil || r.ChannelCatalog()[1].ID == build.ID {
		t.Fatalf("recreated name kept its deletion intent or identity: %v", published.RemovedChannels)
	}
	reload(recreated)
	human.send("JOIN #build")
	human.until(" 331 " + humanNick + " #build ")
	human.until(" 366 " + humanNick + " #build ")
}
