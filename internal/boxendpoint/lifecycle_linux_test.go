//go:build linux

package boxendpoint

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// These tests signal only groups created by the endpoint fixture. No root,
// PID-1 mode, namespace-wide signal or live box is involved.
func TestExitedLeaderRetainsIgnoringDescendantThroughEscalation(t *testing.T) {
	for _, natural := range []bool{true, false} {
		t.Run(fmt.Sprintf("natural-exit=%v", natural), func(t *testing.T) {
			sock, srv := startEndpoint(t, func(c *Config) { c.HangupGrace = time.Second })
			client := dialEndpoint(t, sock)
			sibling := startEchoSession(t, client)
			ch, reports, p, child := startIgnoringDescendant(t, client, srv, natural)
			if natural {
				if _, err := io.WriteString(ch, "exit\n"); err != nil {
					t.Fatal(err)
				}
			} else if err := ch.Close(); err != nil {
				t.Fatal(err)
			}
			awaitClosed(t, p.exited, 2*time.Second, "leader exit")
			// The child installed SIG_IGN before its readiness file. It must still
			// be live after HUP; an already-reaped leader would lose the reservation.
			if !liveProcess(child) || !reserved(p.pid) {
				t.Fatal("group lost before escalation")
			}
			select {
			case <-p.reaped:
				t.Fatal("leader reaped before escalation")
			default:
			}
			time.Sleep(100 * time.Millisecond)
			if !liveProcess(child) {
				t.Fatal("descendant did not survive HUP through the grace")
			}
			checkEchoSession(t, sibling)
			awaitClosed(t, p.reaped, 5*time.Second, "group cleanup and leader reap")
			if !waitFor(func() bool { return !liveProcess(child) }, time.Second) {
				t.Fatal("HUP-ignoring descendant survived escalation")
			}
			assertReapedOnce(t, p)
			if natural {
				select {
				case got := <-reports:
					if len(got) != 1 || got[0] != 7 {
						t.Fatalf("exit reports = %v, want exactly [7]", got)
					}
				case <-time.After(time.Second):
					t.Fatal("exit reports did not finish")
				}
				if ws := p.exitStatus(); !ws.Exited() || ws.ExitStatus() != 7 {
					t.Fatalf("leader status = %v", ws)
				}
			} else if ws := p.exitStatus(); !ws.Signaled() || ws.Signal() != unix.SIGHUP {
				t.Fatalf("disconnect status = %v, want HUP", ws)
			}
			checkEchoSession(t, sibling)
		})
	}
}

// Ready means the child has already ignored HUP, and both PIDs have been
// checked against the endpoint-owned process group before the test proceeds.
func startIgnoringDescendant(t *testing.T, client *ssh.Client, srv *Server, natural bool) (ssh.Channel, <-chan []uint32, *process, int) {
	t.Helper()
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	end := "while :; do sleep 1; done"
	if natural {
		end = "read -r release; exit 7"
	}
	command := `sh -c 'trap "" HUP; echo $$ > descendant.ready; exec sleep 300' >/dev/null 2>&1 & child=$!; until [ -s descendant.ready ]; do sleep 0.01; done; printf '%s %s\n' "$$" "$child"; ` + end
	reports := make(chan []uint32, 1)
	go func() {
		var statuses []uint32
		for req := range reqs {
			if req.Type == "exit-status" {
				var v struct{ Status uint32 }
				if ssh.Unmarshal(req.Payload, &v) == nil {
					statuses = append(statuses, v.Status)
				}
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
		reports <- statuses
	}()
	ok, err := ch.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{command}))
	if err != nil || !ok {
		t.Fatalf("exec = %v, %v", ok, err)
	}
	line, err := bufio.NewReader(ch).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var leader, child int
	if _, err = fmt.Sscanf(line, "%d %d", &leader, &child); err != nil {
		t.Fatal(err)
	}
	var owned *process
	for _, p := range srv.reaper.tracked() {
		if p.pid == leader {
			owned = p
		}
	}
	if owned == nil {
		t.Fatal("leader not owned by fixture")
	}
	found := false
	for _, st := range processTable() {
		if st.pid == child && st.pgrp == leader {
			found = true
		}
	}
	if !found {
		t.Fatal("descendant is not in the fixture leader's group")
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", child))
	if err != nil {
		t.Fatal(err)
	}
	ignored := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "SigIgn:") {
			mask, e := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "SigIgn:")), 16, 64)
			ignored = e == nil && mask&1 != 0
		}
	}
	if !ignored {
		t.Fatal("descendant readiness did not establish SIGHUP ignored")
	}
	return ch, reports, owned, child
}

func liveProcess(pid int) bool {
	for _, st := range processTable() {
		if st.pid == pid {
			return st.state != 'Z' && st.state != 'X'
		}
	}
	return false
}
func awaitClosed(t *testing.T, done <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("timed out waiting for %s", what)
	}
}
func assertReapedOnce(t *testing.T, p *process) {
	t.Helper()
	var ws unix.WaitStatus
	if _, err := unix.Wait4(p.pid, &ws, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
		t.Fatalf("leader not reaped: %v", err)
	}
	if p.signalGroup(0) {
		t.Fatal("released group remains signalable through ownership")
	}
}

type echoSession struct {
	in  io.WriteCloser
	out *bufio.Reader
}

func startEchoSession(t *testing.T, c *ssh.Client) echoSession {
	t.Helper()
	s := mustSession(t, c)
	in, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start("cat"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return echoSession{in, bufio.NewReader(out)}
}
func checkEchoSession(t *testing.T, s echoSession) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		if _, err := io.WriteString(s.in, "still-live\n"); err != nil {
			done <- err
			return
		}
		line, err := s.out.ReadString('\n')
		if err == nil && line != "still-live\n" {
			err = fmt.Errorf("echo = %q", line)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live sibling stopped responding")
	}
}

func TestConcurrentDisconnectAndShutdownReapOwnedGroups(t *testing.T) {
	sock, srv := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	ch, _, p, child := startIgnoringDescendant(t, client, srv, false)
	// A separate fixture must remain usable even when this whole server stops.
	otherSock, _ := startEndpoint(t, nil)
	other := startEchoSession(t, dialEndpoint(t, otherSock))
	gate := make(chan struct{})
	done := make(chan struct{})
	go func() { <-gate; _ = ch.Close() }()
	go func() { defer close(done); <-gate; srv.Shutdown(nil, 2*time.Second) }()
	close(gate)
	awaitClosed(t, done, 5*time.Second, "shutdown")
	awaitClosed(t, p.reaped, time.Second, "leader reap")
	if liveProcess(child) {
		t.Fatal("owned descendant survived shutdown")
	}
	assertReapedOnce(t, p)
	waitConnections(t, srv, 0, time.Second)
	checkEchoSession(t, other)
}

// writeGateConn is a deterministic transport stall, not an SSH window stall:
// after arming, net.Conn.Write cannot progress until that connection is closed.
// Closing a session/channel alone deliberately cannot release it.
type writeGateConn struct {
	net.Conn
	armed                atomic.Bool
	entered              chan struct{}
	closed               chan struct{}
	enterOnce, closeOnce sync.Once
}

func (c *writeGateConn) Write(b []byte) (int, error) {
	if c.armed.Load() {
		c.enterOnce.Do(func() { close(c.entered) })
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(b)
}
func (c *writeGateConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type writeGateListener struct {
	net.Listener
	accepted chan *writeGateConn
}

func (l *writeGateListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	gate := &writeGateConn{Conn: c, entered: make(chan struct{}), closed: make(chan struct{})}
	l.accepted <- gate
	return gate, nil
}
func startWriteGatedEndpoint(t *testing.T) (string, *Server, <-chan *writeGateConn) {
	t.Helper()
	dir := t.TempDir()
	env, err := ChildEnvironment(dir, SandboxUser, os.Getenv("PATH"), "")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{Workdir: dir, Env: env, HangupGrace: 100 * time.Millisecond, DrainTimeout: 100 * time.Millisecond, CloseTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "ssh.sock")
	ln, err := ListenPrivate(sock)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &writeGateListener{Listener: ln, accepted: make(chan *writeGateConn, 8)}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(wrapped) }()
	t.Cleanup(func() {
		srv.Shutdown([]net.Listener{wrapped}, 2*time.Second)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return sock, srv, wrapped.accepted
}

func TestControlWriteFallbackReleasesRepeatedFaults(t *testing.T) {
	sock, srv, accepted := startWriteGatedEndpoint(t)
	healthy := dialEndpoint(t, sock)
	<-accepted
	sibling := startEchoSession(t, healthy)
	for i := 0; i < 3; i++ {
		client := dialEndpoint(t, sock)
		gate := <-accepted
		s := mustSession(t, client)
		input, err := s.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Start("read -r release; exit 9"); err != nil {
			t.Fatal(err)
		}
		// The exec success reply was read. No output is generated: the next
		// server write is post-exit EOF/status/close control traffic.
		gate.armed.Store(true)
		if _, err = io.WriteString(input, "exit\n"); err != nil {
			t.Fatal(err)
		}
		awaitClosed(t, gate.entered, time.Second, "blocked control write")
		// These assertions precede client/test rescue. serveConn returns only
		// after stream/control workers, owned-group cleanup and session release.
		awaitClosed(t, gate.closed, 2*time.Second, "server connection fallback")
		waitConnections(t, srv, 1, 2*time.Second)
		checkEchoSession(t, sibling)
		_ = client.Close()
	}
	_ = healthy.Close()
	waitConnections(t, srv, 0, 2*time.Second)
	waitUntracked(t, srv, 2*time.Second)
}

// Completed SSH channels must not let a client admit more than the session
// bound while their process groups are still completing cleanup.
func TestSessionLimitIncludesGroupsAwaitingCleanup(t *testing.T) {
	sock, srv := startEndpoint(t, func(c *Config) { c.HangupGrace = 30 * time.Second })
	client := dialEndpoint(t, sock)
	release := filepath.Join(srv.cfg.Workdir, "cleanup-release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	for i := 0; i < maxSessionsPerConn; i++ {
		command := fmt.Sprintf(`sh -c 'trap "while [ ! -e cleanup-release ]; do sleep 0.02; done; exit 0" HUP; : > ready-%d; while :; do sleep 0.02; done' >/dev/null 2>&1 & until [ -e ready-%d ]; do sleep 0.01; done`, i, i)
		if _, err := mustSession(t, client).Output(command); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(srv.reaper.tracked()); got != maxSessionsPerConn {
		t.Fatalf("tracked cleanup = %d, want %d", got, maxSessionsPerConn)
	}
	extra, err := client.NewSession()
	if err == nil {
		_ = extra.Close()
		t.Fatal("admitted a session past the cleanup capacity bound")
	}
	var refused *ssh.OpenChannelError
	if !errors.As(err, &refused) || refused.Reason != ssh.ResourceShortage {
		t.Fatalf("admission error = %v", err)
	}
	if err = os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitUntracked(t, srv, 5*time.Second)
	// Reaping precedes the serve goroutine's last step; wait for admission to
	// return without manufacturing capacity or replacing the connection.
	var next *ssh.Session
	if !waitFor(func() bool { next, err = client.NewSession(); return err == nil }, time.Second) {
		t.Fatalf("capacity not returned: %v", err)
	}
	if out, err := next.Output("echo reusable"); err != nil || string(out) != "reusable\n" {
		t.Fatalf("reused session = %q, %v", out, err)
	}
}

// A controlled observation represents a member whose exit is not yet visible
// after escalation. The real fixture child still exits normally on SIGKILL;
// this does not create a D-state process or interfere with the kernel.
func TestExitedLeaderKeepsCapacityUntilPostKillGroupCompletion(t *testing.T) {
	sock, srv := startEndpoint(t, func(c *Config) { c.HangupGrace = 100 * time.Millisecond })
	client := dialEndpoint(t, sock)
	ch, reports, p, child := startIgnoringDescendant(t, client, srv, true)
	healthy := dialEndpoint(t, sock)
	sibling := startEchoSession(t, healthy)
	var hold atomic.Bool
	hold.Store(true)
	t.Cleanup(func() { hold.Store(false) })
	actualDone := make(chan struct{})
	var observed sync.Once
	p.mu.Lock()
	p.live = func(group int) bool {
		actual := groupLive(group)
		if !actual {
			observed.Do(func() { close(actualDone) })
		}
		return actual || hold.Load()
	}
	p.mu.Unlock()
	if _, err := io.WriteString(ch, "exit\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-reports:
		if len(got) != 1 || got[0] != 7 {
			t.Fatalf("exit reports = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("exit reporting waited for group completion")
	}
	awaitClosed(t, actualDone, 2*time.Second, "real fixture group exit after escalation")
	if liveProcess(child) {
		t.Fatal("fixture descendant still live")
	}
	_ = client.Close()
	// Outlast the former post-KILL timeout. Returning ownership merely because
	// that deadline passed would close reaped and release this connection.
	select {
	case <-p.reaped:
		t.Fatal("leader reaped while group completion unavailable")
	case <-time.After(killWait + 200*time.Millisecond):
	}
	if !reserved(p.pid) {
		t.Fatal("leader identity released while completion unavailable")
	}
	waitConnections(t, srv, 2, time.Second)
	checkEchoSession(t, sibling)
	hold.Store(false)
	awaitClosed(t, p.reaped, 2*time.Second, "release after observed group completion")
	assertReapedOnce(t, p)
	waitConnections(t, srv, 1, 2*time.Second)
	replacement := dialEndpoint(t, sock)
	if out, err := mustSession(t, replacement).Output("echo reusable"); err != nil || string(out) != "reusable\n" {
		t.Fatalf("replacement = %q, %v", out, err)
	}
	_ = replacement.Close()
	waitConnections(t, srv, 1, 2*time.Second)
	checkEchoSession(t, sibling)
}
