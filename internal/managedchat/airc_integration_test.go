package managedchat

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in interoperability check uses real aircd control replies, not a Go
// fixture shaped like Applied. It does not prove container/UID authority: the
// local fixture explicitly stubs peer verification. Host VM acceptance must
// separately exercise the production verifier and confined runtime.
func TestRealAIRCControlContract(t *testing.T) {
	bin := os.Getenv("YOLOCTL_TEST_AIRC_BIN_DIR")
	if bin == "" {
		t.Skip("set YOLOCTL_TEST_AIRC_BIN_DIR to the directory holding published aircd")
	}
	// Keep Unix socket paths short even when testing under a long worktree path.
	dir, err := os.MkdirTemp("", "yc-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{"endpoint", "control"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tokenSum := sha256.Sum256([]byte(token))
	configPath := filepath.Join(dir, "server.toml")
	writeConfig := func(generation uint64, names, operators []string) Rendered {
		t.Helper()
		var body strings.Builder
		_, _ = fmt.Fprintf(&body, `server_name = "airc.local"
listen_unix = "endpoint/irc.sock"
control_unix = "control/control.sock"
membership_generation = %d
database = "airc.sqlite3"

[artifacts]
listen_unix = "endpoint/artifacts.sock"
public_url = "http://airc.local"
`, generation)
		quoted := make([]string, 0, len(names))
		for _, name := range names {
			_, _ = fmt.Fprintf(&body, "\n[[accounts]]\nname = %q\ntoken_sha256 = %q\n", name, hex.EncodeToString(tokenSum[:]))
			quoted = append(quoted, fmt.Sprintf("%q", name))
		}
		ops := make([]string, 0, len(operators))
		for _, name := range operators {
			ops = append(ops, fmt.Sprintf("%q", name))
		}
		_, _ = fmt.Fprintf(&body, "\n[[channels]]\nname = \"#space\"\nmembers = [%s]\noperators = [%s]\n", strings.Join(quoted, ", "), strings.Join(ops, ", "))
		data := []byte(body.String())
		if err := os.WriteFile(configPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		return Rendered{Generation: generation, ConfigSHA256: hex.EncodeToString(sum[:]), Accounts: names}
	}
	want := writeConfig(1, []string{"alice", "bob"}, []string{"alice"})
	log, err := os.OpenFile(filepath.Join(dir, "aircd.stderr"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	server := exec.Command(filepath.Join(bin, "aircd"), "serve", "--config", configPath)
	server.Stderr = log
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var serverErr error
	go func() { serverErr = server.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = server.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("aircd fixture did not stop")
		}
	})
	socket := filepath.Join(dir, "control", "control.sock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case <-done:
			t.Fatalf("aircd exited before its control socket was ready: %v", serverErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("aircd control socket did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	verified := 0
	control := Control{Socket: socket, VerifyPeer: func(*net.UnixConn) error {
		verified++ // Local test stub only; this is not live-container proof.
		return nil
	}}
	initial, err := control.Call(t.Context(), "status")
	if err != nil {
		t.Fatalf("real AIRC status did not decode: %v", err)
	}
	if err := initial.Matches(want); err != nil {
		t.Fatalf("real AIRC status did not match exact file/generation: %v", err)
	}
	if initial.ConfiguredAccounts != 2 || len(initial.Accounts) != 0 {
		t.Fatal("real AIRC initial configured/connected account inventory differs")
	}

	// Authenticate a real Unix IRC peer so status exercises its numeric count.
	peer, err := net.DialTimeout("unix", filepath.Join(dir, "endpoint", "irc.sock"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(peer, "PASS bob:"+token+"\r\nNICK bob\r\nUSER bob 0 * :control test\r\nJOIN #space\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(peer)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("real AIRC peer did not join: %v", err)
		}
		if strings.Contains(line, " 366 ") {
			break
		}
	}
	connected, err := control.Call(t.Context(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if err := connected.Matches(want); err != nil {
		t.Fatal(err)
	}
	if connected.Accounts["bob"].Connections != 1 || connected.ConfiguredAccounts != 2 {
		t.Fatal("real AIRC connected account count did not decode")
	}

	// A configured role change must reach an already connected IRC peer,
	// without restarting it or changing its account/credential.
	for index, mode := range []string{"+o", "-o"} {
		ops := []string{"alice"}
		if index == 0 {
			ops = append(ops, "bob")
		}
		roleProof := writeConfig(uint64(index+2), []string{"alice", "bob"}, ops)
		if _, err := control.Call(t.Context(), "reload"); err != nil {
			t.Fatal(err)
		}
		observed, err := control.Call(t.Context(), "status")
		if err != nil || observed.Matches(roleProof) != nil || observed.Incarnation != initial.Incarnation || observed.Accounts["bob"].Connections != 1 {
			t.Fatal("role reload changed connection or failed proof", err)
		}
		if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("role %s did not reach connected peer: %v", mode, err)
			}
			if strings.Contains(line, " MODE #space "+mode+" :bob") {
				break
			}
		}
	}

	next := writeConfig(4, []string{"alice"}, []string{"alice"})
	reloaded, err := control.Call(t.Context(), "reload")
	if err != nil {
		t.Fatalf("real AIRC reload did not decode: %v", err)
	}
	if reloaded.Generation != next.Generation || reloaded.ConfigSHA256 != next.ConfigSHA256 || reloaded.Incarnation != initial.Incarnation {
		t.Fatal("real AIRC reload omitted its exact applied generation/digest/incarnation")
	}
	// Reload's acknowledgement deliberately lacks feature/listener/account
	// readiness. Production callers must discard it and read a fresh status.
	if reloaded.Protocol != 0 || len(reloaded.Features.Features) != 0 || reloaded.ConfiguredAccounts != 0 || len(reloaded.Accounts) != 0 || reloaded.Validate() == nil {
		t.Fatal("real AIRC reload was mistaken for a full readiness proof")
	}
	after, err := control.Call(t.Context(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if err := after.Matches(next); err != nil {
		t.Fatalf("fresh real AIRC status does not prove reloaded membership: %v", err)
	}
	if after.Incarnation != initial.Incarnation || after.ConfiguredAccounts != 1 || after.Accounts["bob"].Connections != 0 {
		t.Fatal("reload changed the process identity or retained the revoked account")
	}
	if after.Matches(want) == nil {
		t.Fatal("fresh status falsely matched the preceding configuration")
	}
	if err := peer.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("real AIRC revoked peer did not disconnect: %v", err)
		}
	}
	if verified != 8 {
		t.Fatalf("peer verifier ran %d times for eight one-shot calls", verified)
	}
}
