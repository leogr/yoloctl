//go:build linux

package boxendpoint

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/sshtransport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// These isolated protocol tests run the endpoint unprivileged and not as PID
// 1: children keep the test's credentials and only their own groups are ever
// signalled. Root, sudo, PID-1 reaping and namespace-wide shutdown are left to
// the disposable runtime smoke.

func startEndpoint(t *testing.T, mutate func(*Config)) (string, *Server) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env, err := ChildEnvironment(home, SandboxUser, os.Getenv("PATH"), `{"INJECTED":"from-account"}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workdir: dir, Env: env, HangupGrace: 300 * time.Millisecond, DrainTimeout: 300 * time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "ssh.sock")
	ln, err := ListenPrivate(sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Shutdown([]net.Listener{ln}, 2*time.Second)
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return sock, srv
}

var clientConfig = &ssh.ClientConfig{User: SandboxUser, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}

func dialEndpoint(t *testing.T, sock string) *ssh.Client {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, "endpoint", clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(c, chans, reqs)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// The real sshtransport client used by box exec, stop and helpers keeps
// stdout and stderr separate and reports the exit status.
func TestSSHTransportExecStreamsAndStatus(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = sshtransport.Run(t.Context(), conn, clientConfig, `printf 'out:%s:%s:%s:%s' "$INJECTED" "$TERM" "$USER" "$PWD"; printf err >&2; exit 7`, false, devNull(t), &stdout, &stderr)
	if code, ok := sshtransport.ExitStatus(err); !ok || code != 7 {
		t.Fatalf("status = %v (%v)", code, err)
	}
	dir := filepath.Dir(sock)
	if got, want := stdout.String(), "out:from-account:dumb:sandbox:"+dir; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.String() != "err" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestStdinEOFReachesChild(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	s.Stdin = strings.NewReader("line one\nline two\n")
	out, err := s.Output("wc -l")
	if err != nil || strings.TrimSpace(string(out)) != "2" {
		t.Fatalf("wc -l = %q, %v", out, err)
	}
}

func TestChildEnvironmentIsExplicit(t *testing.T) {
	t.Setenv("YOLOCTL_ENDPOINT_TEST_LEAK", "server-only")
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Output("env")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"YOLOCTL_ENDPOINT_TEST_LEAK", UserEnvironmentVariable, "OPENSHELL_SANDBOX"} {
		if strings.Contains(string(out), forbidden) {
			t.Fatalf("child environment contains %s:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(string(out), "INJECTED=from-account") || !strings.Contains(string(out), "SHELL=/bin/bash") {
		t.Fatalf("child environment:\n%s", out)
	}
}

func TestSignalExitIsReportedAsSignal(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	err = s.Run("kill -TERM $$")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.Signal() != "TERM" || exit.ExitStatus() != 128+15 {
		t.Fatalf("Run = %v", err)
	}
}

func readWinsize() string {
	return `python3 -c 'import fcntl,struct,termios; print("%d %d %d %d" % struct.unpack("HHHH", fcntl.ioctl(0, termios.TIOCGWINSZ, bytes(8))))'`
}

// PTY sessions apply the request's terminal, size, measured pixels and modes,
// and follow window changes.
func TestPTYGeometryModesAndResize(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestPty("xterm-direct", 30, 100, ssh.TerminalModes{ssh.ECHO: 0, ssh.ICANON: 1}); err != nil {
		t.Fatal(err)
	}
	// x/crypto's RequestPty invents eight pixels per cell; the holder sends
	// measured pixels in its own request, so set them through a resize.
	stdin, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	script := `printf 'term=%s\n' "$TERM"; stty -a | grep -o -- '-echo ' | head -1; ` + readWinsize() + `; read -r _; ` + readWinsize()
	if err := s.Start(script); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	next := func() string {
		if !lines.Scan() {
			t.Fatalf("PTY output ended: %v", lines.Err())
		}
		return strings.TrimRight(lines.Text(), "\r")
	}
	if got := next(); got != "term=xterm-direct" {
		t.Fatalf("term line = %q", got)
	}
	if got := next(); strings.TrimSpace(got) != "-echo" {
		t.Fatalf("echo mode line = %q", got)
	}
	if got := next(); got != "30 100 800 240" {
		t.Fatalf("initial winsize = %q", got)
	}
	if ok, err := s.SendRequest("window-change", true, ssh.Marshal(windowChange{Cols: 120, Rows: 40, Width: 1440, Height: 960})); err != nil || !ok {
		t.Fatalf("window-change = %v, %v", ok, err)
	}
	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != "40 120 1440 960" {
		t.Fatalf("resized winsize = %q", got)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestsAreBoundedAndRefused(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	if ok, _, err := client.SendRequest("keepalive@yoloctl.dev", true, nil); err != nil || !ok {
		t.Fatalf("keepalive = %v, %v", ok, err)
	}
	if ok, _, err := client.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Addr string
		Port uint32
	}{"127.0.0.1", 0})); err != nil || ok {
		t.Fatalf("tcpip-forward = %v, %v", ok, err)
	}
	if _, err := client.Dial("tcp", "127.0.0.1:22"); err == nil {
		t.Fatal("direct-tcpip channel accepted")
	}
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct {
		name    string
		payload []byte
	}{
		{"env", ssh.Marshal(struct{ Name, Value string }{"A", "B"})},
		{"subsystem", ssh.Marshal(struct{ Name string }{"sftp"})},
		{"signal", ssh.Marshal(struct{ Signal string }{"TERM"})},
		{"x11-req", nil},
		{"pty-req", []byte{1, 2, 3}},
		{"pty-req", ssh.Marshal(ptyRequest{Term: "xterm", Cols: 80, Rows: 24, Modes: string([]byte{ssh.ECHO, 0})})},
		{"exec", ssh.Marshal(struct{ Command string }{""})},
		{"exec", ssh.Marshal(struct{ Command string }{strings.Repeat("x", maxCommandBytes+1)})},
		{"exec", ssh.Marshal(struct{ Command string }{"true\x00false"})},
		{"shell", []byte("junk")},
	} {
		ok, err := s.SendRequest(req.name, true, req.payload)
		if err != nil || ok {
			t.Fatalf("%s request = %v, %v", req.name, ok, err)
		}
	}
	// The same session still runs a valid command afterwards.
	if out, err := s.Output("echo still-usable"); err != nil || string(out) != "still-usable\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
}

func TestOnlyTheSandboxUserAuthenticates(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _, _, err = ssh.NewClientConn(conn, "endpoint", &ssh.ClientConfig{User: "root", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("root authenticated")
	}
}

// A peer that never reads its output only stalls its own channel; another
// channel on the same connection and other connections keep working.
func TestBackpressureIsolatesChannels(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	flood, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flood.StdoutPipe(); err != nil { // never read
		t.Fatal(err)
	}
	if err := flood.Start("head -c 16777216 /dev/zero"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*ssh.Client{client, dialEndpoint(t, sock)} {
		done := make(chan error, 1)
		go func() {
			s, err := c.NewSession()
			if err != nil {
				done <- err
				return
			}
			out, err := s.Output("echo independent")
			if err == nil && string(out) != "independent\n" {
				err = errors.New("unexpected output " + string(out))
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a stalled channel blocked another session")
		}
	}
	_ = flood.Close()
}

func childPID(t *testing.T, r io.Reader) int {
	t.Helper()
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d still running after %s", pid, within)
}

// Closing a connection hangs up its children, including one that ignores
// SIGHUP, so no process stays detached from a closed channel.
func TestDisconnectEndsChildGroups(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, "endpoint", clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(c, chans, reqs)
	var pids []int
	for _, command := range []string{"echo $$; exec sleep 300", "trap '' HUP; echo $$; while :; do sleep 1; done"} {
		s, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Start(command); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, childPID(t, out))
	}
	_ = client.Close()
	for _, pid := range pids {
		waitGone(t, pid, 5*time.Second)
	}
}

func TestShutdownEndsTrackedChildren(t *testing.T) {
	sock, srv := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("trap '' TERM HUP; echo $$; while :; do sleep 1; done"); err != nil {
		t.Fatal(err)
	}
	pid := childPID(t, out)
	start := time.Now()
	srv.Shutdown(nil, 300*time.Millisecond)
	waitGone(t, pid, 3*time.Second)
	if time.Since(start) > 3*time.Second {
		t.Fatal("shutdown escalation was not bounded")
	}
}

func TestConcurrentChannelsAndPTYCoexist(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	interactive, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := interactive.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	in, err := interactive.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := interactive.Start("read -r line; echo got:$line"); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		s, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if out, err := s.Output("echo helper " + strconv.Itoa(i)); err != nil || string(out) != "helper "+strconv.Itoa(i)+"\n" {
			t.Fatalf("helper %d = %q, %v", i, out, err)
		}
	}
	if _, err := io.WriteString(in, "x\n"); err != nil {
		t.Fatal(err)
	}
	if err := interactive.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestListenPrivateRefusesUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "ssh.sock")
	ln, err := ListenPrivate(sock)
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(sock, &st); err != nil || st.Mode&0o777 != 0o600 {
		t.Fatalf("socket mode %o, %v", st.Mode&0o777, err)
	}
	// A stale socket is replaced.
	if l, ok := ln.(*net.UnixListener); ok {
		l.SetUnlinkOnClose(false)
	}
	_ = ln.Close()
	if ln, err = ListenPrivate(sock); err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	_ = ln.Close()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenPrivate(regular); err == nil {
		t.Fatal("replaced a regular file")
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenPrivate(filepath.Join(dir, "link")); err == nil {
		t.Fatal("replaced a symlink")
	}
	linkedDir := filepath.Join(t.TempDir(), "dir")
	if err := os.Symlink(dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenPrivate(filepath.Join(linkedDir, "ssh.sock")); err == nil {
		t.Fatal("accepted a symlinked socket directory")
	}
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenPrivate(filepath.Join(shared, "ssh.sock")); err == nil {
		t.Fatal("accepted a world-writable socket directory")
	}
}

// The readiness probe and other connections that close mid-handshake never
// disturb an established session.
func TestAbortedHandshakesLeaveSessionsAlone(t *testing.T) {
	sock, _ := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	for range 3 {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, "SSH-2.0-yoloctl-readiness\r\n"); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "SSH-2.0-yoloctl_box_endpoint_"+ProtocolVersion) {
			t.Fatalf("greeting = %q, %v", line, err)
		}
		_ = conn.Close()
		garbage, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = garbage.Write(bytes.Repeat([]byte{0xff}, 4096))
		_ = garbage.Close()
	}
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.Output("echo alive"); err != nil || string(out) != "alive\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}
}

func TestLookupSandboxIdentity(t *testing.T) {
	u, err := user.Lookup(SandboxUser)
	if err != nil {
		t.Skipf("no %s user here: %v", SandboxUser, err)
	}
	id, home, err := lookupSandbox()
	if u.Uid == "0" {
		if err == nil {
			t.Fatal("root sandbox identity accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if strconv.FormatUint(uint64(id.UID), 10) != u.Uid || strconv.FormatUint(uint64(id.GID), 10) != u.Gid || home != u.HomeDir || len(id.Groups) == 0 {
		t.Fatalf("identity = %+v home %q, user %+v", id, home, u)
	}
}

func TestMainRefusesOutsidePID1(t *testing.T) {
	if os.Getpid() == 1 {
		t.Skip("running as PID 1")
	}
	err := runPID1(Args("/workdir", "/run/openshell/ssh.sock")[2:], func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "PID 1") {
		t.Fatalf("runPID1 = %v", err)
	}
	if err := runPID1([]string{"--protocol", "999"}, func(string, ...any) {}); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}

// A background job left by a command that already exited is still ended when
// its session finishes, and the command's status is reported first.
func TestSessionEndsBackgroundJobAfterExit(t *testing.T) {
	sock, srv := startEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Output("sleep 300 >/dev/null 2>&1 & echo $!")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid, 5*time.Second)
	waitUntracked(t, srv, 5*time.Second)
}

func waitUntracked(t *testing.T, srv *Server, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for len(srv.reaper.tracked()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d children still tracked after %s", len(srv.reaper.tracked()), within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The leader stays unreaped while its group is being cleaned up, so the group
// ID that cleanup signals cannot be reused in the meantime.
func TestLeaderStaysReservedUntilGroupCleanup(t *testing.T) {
	sock, srv := startEndpoint(t, func(c *Config) { c.HangupGrace = 3 * time.Second })
	client := dialEndpoint(t, sock)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	// The job takes a moment to finish after the hangup.
	// The command waits until the job is set up, so the job's own setup does
	// not race the cleanup this test observes.
	out, err := s.Output(`sh -c 'trap "sleep 1; exit 0" HUP; : >ready; while :; do sleep 0.1; done' >/dev/null 2>&1 & until [ -e ready ]; do sleep 0.01; done; echo $$`)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if !reserved(leader) {
		t.Fatal("leader was reaped while its group was still running")
	}
	waitUntracked(t, srv, 5*time.Second)
	if reserved(leader) {
		t.Fatal("leader was not reaped after cleanup")
	}
}

func reserved(pid int) bool {
	for _, st := range processTable() {
		if st.pid == pid {
			return true
		}
	}
	return false
}

// A peer that stops reading one channel's output but still handles SSH keeps
// its other session; the stalled one still reports its status within the bound.
func TestStalledOutputIsBoundedForCooperativePeer(t *testing.T) {
	sock, srv := startEndpoint(t, func(c *Config) { c.CloseTimeout = time.Second })
	client := dialEndpoint(t, sock)
	stalled, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stalled.StdoutPipe(); err != nil { // never read
		t.Fatal(err)
	}
	if _, err := stalled.StderrPipe(); err != nil { // never read
		t.Fatal(err)
	}
	// Both streams together exceed the channel window but fit the buffers, so
	// the command exits while output is still pending.
	start := time.Now()
	if err := stalled.Start("head -c 1100000 /dev/zero; head -c 1050000 /dev/zero >&2; exit 4"); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- stalled.Wait() }()
	select {
	case err := <-waitErr:
		var exit *ssh.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 4 {
			t.Fatalf("Wait = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled session was not bounded")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("one shared drain deadline took %s", elapsed)
	}
	sibling, err := client.NewSession()
	if err != nil {
		t.Fatalf("connection lost after a cooperative stall: %v", err)
	}
	if out, err := sibling.Output("echo sibling"); err != nil || string(out) != "sibling\n" {
		t.Fatalf("sibling = %q, %v", out, err)
	}
	waitUntracked(t, srv, 5*time.Second)
}

// stallConn stops delivering transport bytes to its client once stalled.
type stallConn struct {
	net.Conn
	stalled chan struct{}
	done    chan struct{}
}

func (c *stallConn) Read(b []byte) (int, error) {
	select {
	case <-c.stalled:
		<-c.done
		return 0, io.EOF
	default:
	}
	return c.Conn.Read(b)
}

// A peer that withholds all transport progress loses only its own connection;
// cleanup stays bounded and another connection keeps working.
func TestUnresponsivePeerLosesOnlyItsConnection(t *testing.T) {
	sock, srv := startEndpoint(t, func(c *Config) { c.CloseTimeout = 500 * time.Millisecond })
	raw, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	conn := &stallConn{Conn: raw, stalled: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() { close(conn.done); _ = raw.Close() })
	c, chans, reqs, err := ssh.NewClientConn(conn, "endpoint", clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(c, chans, reqs)
	for _, command := range []string{"head -c 1048576 /dev/zero", "sleep 0.3; exit 3"} {
		s, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.StdoutPipe(); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(command); err != nil {
			t.Fatal(err)
		}
	}
	close(conn.stalled)
	other := dialEndpoint(t, sock)
	// The server itself must release the stalled connection, its sessions and
	// their slots; test cleanup only unblocks the client wrapper afterwards.
	waitConnections(t, srv, 1, 10*time.Second)
	waitUntracked(t, srv, 10*time.Second)
	s, err := other.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.Output("echo other"); err != nil || string(out) != "other\n" {
		t.Fatalf("other connection = %q, %v", out, err)
	}
}

// waitConnections waits until the server holds exactly n connections, each
// released only after all of its sessions and their cleanup have finished.
func waitConnections(t *testing.T, srv *Server, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		srv.mu.Lock()
		got, slots := len(srv.conns), len(srv.slots)
		srv.mu.Unlock()
		if got == n && slots == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server holds %d connections (%d slots), want %d", got, slots, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A session's admission stays charged until its group cleanup finishes, even
// after the exit report and the client's disconnect; then it is released.
func TestAdmissionHeldUntilGroupCleanup(t *testing.T) {
	sock, srv := startEndpoint(t, func(c *Config) { c.HangupGrace = 3 * time.Second })
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, "endpoint", clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(c, chans, reqs)
	s, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := s.Output(`sh -c 'trap "sleep 1; exit 0" HUP; : >ready; while :; do sleep 0.1; done' >/dev/null 2>&1 & until [ -e ready ]; do sleep 0.01; done`); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 800*time.Millisecond {
		t.Fatalf("exit report waited for group cleanup: %s", elapsed)
	}
	_ = client.Close()
	time.Sleep(300 * time.Millisecond)
	srv.mu.Lock()
	held := len(srv.conns)
	srv.mu.Unlock()
	if held != 1 {
		t.Fatalf("connection released with group cleanup outstanding (%d held)", held)
	}
	other := dialEndpoint(t, sock)
	if out, err := mustSession(t, other).Output("echo live"); err != nil || string(out) != "live\n" {
		t.Fatalf("unrelated session = %q, %v", out, err)
	}
	waitConnections(t, srv, 1, 5*time.Second) // only the unrelated connection
	waitUntracked(t, srv, 5*time.Second)
}

func mustSession(t *testing.T, c *ssh.Client) *ssh.Session {
	t.Helper()
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
