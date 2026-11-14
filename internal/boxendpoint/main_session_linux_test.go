//go:build linux

package boxendpoint

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Exercise the PID1 serving/stop path with ordinary test credentials and PID1
// disabled. Shutdown can signal only process groups created by this fixture.
func startMainEndpoint(t *testing.T, mutate func(*Config)) (string, *Server, <-chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	env, err := ChildEnvironment(dir, SandboxUser, os.Getenv("PATH"), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workdir: dir, Env: env, HangupGrace: 100 * time.Millisecond, DrainTimeout: 200 * time.Millisecond, CloseTimeout: 200 * time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	if cfg.PID1 {
		t.Fatal("test must not signal a PID namespace")
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
	stop := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.serveUntilStopped(ln, stop)
	}()
	t.Cleanup(func() {
		stop <- syscall.SIGTERM
		awaitClosed(t, done, shutdownGrace+killWait+time.Second, "endpoint shutdown")
		_ = ln.Close()
	})
	return sock, srv, done
}

func markMainSession(t *testing.T, s *ssh.Session) {
	t.Helper()
	ok, err := s.SendRequest(MainSessionRequest, true, nil)
	if err != nil || !ok {
		t.Fatalf("main mark: accepted=%v error=%v", ok, err)
	}
}

func requireEndpointRunning(t *testing.T, srv *Server, done <-chan struct{}, client *ssh.Client) {
	t.Helper()
	select {
	case <-srv.mainExited:
		t.Fatal("unlaunched/unmarked channel requested shutdown")
	case <-done:
		t.Fatal("endpoint stopped without a marked process exit")
	default:
	}
	s := mustSession(t, client)
	if out, err := s.Output("printf still-running"); err != nil || string(out) != "still-running" {
		t.Fatalf("ordinary next exec: %q %v", out, err)
	}
}

func TestMarkedMainExit(t *testing.T)    { testMarkedMainExit(t, false) }
func TestMarkedMainPTYExit(t *testing.T) { testMarkedMainExit(t, true) }

func testMarkedMainExit(t *testing.T, pty bool) {
	sock, srv, done := startMainEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	other := dialEndpoint(t, sock)
	sibling := startEchoSession(t, other)
	checkEchoSession(t, sibling)
	s := mustSession(t, client)
	if pty {
		if err := s.RequestPty("xterm-256color", 24, 80, nil); err != nil {
			t.Fatal(err)
		}
	}
	markMainSession(t, s)
	out, err := s.CombinedOutput("printf main-last; printf main-error >&2; exit 7")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 7 || !strings.Contains(string(out), "main-last") || !strings.Contains(string(out), "main-error") {
		t.Fatalf("finish lost output/status before shutdown: %q %v", out, err)
	}
	awaitClosed(t, done, 3*time.Second, "main exit shutdown")
	if len(srv.reaper.tracked()) != 0 {
		t.Fatal("Shutdown left an owned sibling process")
	}
	if nc, err := net.Dial("unix", sock); err == nil {
		_ = nc.Close()
		t.Fatal("endpoint listener survived main exit")
	}
}

func TestMarkedMainExitWithoutHolder(t *testing.T)    { testMarkedMainExitWithoutHolder(t, false) }
func TestMarkedMainPTYExitWithoutHolder(t *testing.T) { testMarkedMainExitWithoutHolder(t, true) }

func testMarkedMainExitWithoutHolder(t *testing.T, pty bool) {
	sock, srv, done := startMainEndpoint(t, func(c *Config) { c.HangupGrace = 5 * time.Second })
	client := dialEndpoint(t, sock)
	s := mustSession(t, client)
	if pty {
		if err := s.RequestPty("xterm-256color", 24, 80, nil); err != nil {
			t.Fatal(err)
		}
	}
	markMainSession(t, s)
	out, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("trap '' HUP; printf 'ready\n'; while [ ! -f allow-exit ]; do sleep 0.01; done; exit 7"); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatal("main did not install HUP handling", line, err)
	}
	procs := srv.reaper.tracked()
	if len(procs) != 1 {
		t.Fatalf("main ownership: %d processes", len(procs))
	}
	main := procs[0]
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srv.cfg.Workdir, "allow-exit"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, done, 3*time.Second, "main exit after holder loss")
	if ws := main.exitStatus(); !ws.Exited() || ws.ExitStatus() != 7 {
		t.Fatalf("main did not exit by itself after holder loss: %v", ws)
	}
	if len(srv.reaper.tracked()) != 0 {
		t.Fatal("main was not reaped")
	}
}

func TestMainSessionProbeAndUnmarkedExit(t *testing.T) {
	sock, srv, done := startMainEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	probe := mustSession(t, client)
	markMainSession(t, probe)
	if len(srv.reaper.tracked()) != 0 {
		t.Fatal("capability probe launched a process")
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	plain := mustSession(t, client)
	var exit *ssh.ExitError
	if err := plain.Run("exit 7"); !errors.As(err, &exit) || exit.ExitStatus() != 7 {
		t.Fatal("unmarked process result changed", err)
	}
	requireEndpointRunning(t, srv, done, client)
}

func TestMainSessionRequestRefusals(t *testing.T) {
	sock, srv, done := startMainEndpoint(t, nil)
	client := dialEndpoint(t, sock)
	if ok, _, err := client.SendRequest(MainSessionRequest, true, nil); err != nil || ok {
		t.Fatal("main mark accepted as a global request", ok, err)
	}
	s := mustSession(t, client)
	for _, request := range []string{MainSessionRequest, "unknown-main-request"} {
		if ok, err := s.SendRequest(request, true, []byte("command text is not accepted here")); err != nil || ok {
			t.Fatal("request with payload accepted", request, ok, err)
		}
	}
	out, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("printf 'ready\n'; while [ ! -f allow-exit ]; do sleep 0.01; done"); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatal(line, err)
	}
	if ok, err := s.SendRequest(MainSessionRequest, true, nil); err != nil || ok {
		t.Fatal("late main mark accepted", ok, err)
	}
	if err := os.WriteFile(filepath.Join(srv.cfg.Workdir, "allow-exit"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	requireEndpointRunning(t, srv, done, client)
}

func TestMainSessionFailedStart(t *testing.T) {
	sock, srv, done := startMainEndpoint(t, func(c *Config) { c.Workdir = filepath.Join(c.Workdir, "missing") })
	client := dialEndpoint(t, sock)
	s := mustSession(t, client)
	markMainSession(t, s)
	if err := s.Start("exit 7"); err == nil {
		t.Fatal("fixture did not fail before process launch")
	}
	_ = s.Close()
	if len(srv.reaper.tracked()) != 0 {
		t.Fatal("failed start left a process")
	}
	select {
	case <-srv.mainExited:
		t.Fatal("failed start was mistaken for a main-process exit")
	case <-done:
		t.Fatal("failed start stopped endpoint")
	default:
	}
}
