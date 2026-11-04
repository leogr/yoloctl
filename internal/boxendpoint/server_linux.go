//go:build linux

package boxendpoint

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const (
	handshakeTimeout = 10 * time.Second
	// Below the runtime's 30-second stop grace, so PID 1 always escalates
	// before the runtime has to.
	shutdownGrace       = 10 * time.Second
	defaultHangupGrace  = 5 * time.Second
	defaultDrainTimeout = 2 * time.Second
	defaultCloseTimeout = 2 * time.Second
	// abandonGrace lets copies that were mid-read finish after their sources
	// close; a copy parked on the peer's channel window does not.
	abandonGrace = 100 * time.Millisecond
	// killWait bounds the final server shutdown wait, not session ownership.
	killWait = 2 * time.Second
)

// Config configures a Server.
type Config struct {
	Workdir string
	// Env is the base child environment from ChildEnvironment.
	Env []string
	// Identity, when non-nil, is applied to every child before exec. Nil keeps
	// the server's credentials; only isolated protocol tests use that.
	Identity *Identity
	// PID1 makes the server the namespace reaper and lets Shutdown signal every
	// process in the container's private PID namespace. Never set it anywhere
	// else: kill(-1) outside a private namespace reaches unrelated processes.
	PID1 bool
	// Logf receives non-secret diagnostics. Commands and environment values
	// are never logged.
	Logf func(string, ...any)
	// HangupGrace bounds a hung-up child group before SIGKILL. DrainTimeout
	// bounds output forwarding after the child exits, because a background
	// descendant may hold the terminal or pipe open. CloseTimeout bounds each
	// channel-shutdown step; a peer or transport that withholds progress past
	// it loses its whole SSH connection (see session.finish).
	HangupGrace, DrainTimeout, CloseTimeout time.Duration
}

// Identity is the mapped sandbox user's process credentials.
type Identity struct {
	UID, GID uint32
	Groups   []uint32
}

// Server serves the endpoint protocol. A peer that merely stops reading one
// channel affects only that channel; a peer or transport that withholds SSH
// progress past the cleanup bounds loses its whole connection (see
// session.finish), and other connections are unaffected.
type Server struct {
	cfg    Config
	ssh    *ssh.ServerConfig
	reaper *reaper
	slots  chan struct{}

	mu      sync.Mutex
	closing bool
	conns   map[net.Conn]struct{}
}

// NewServer prepares a server with a fresh in-memory host key. Clients reach
// it only through the root-owned socket and ignore the key.
func NewServer(cfg Config) (*Server, error) {
	if !filepath.IsAbs(cfg.Workdir) || filepath.Clean(cfg.Workdir) != cfg.Workdir {
		return nil, errors.New("endpoint workdir must be a clean absolute path")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.HangupGrace <= 0 {
		cfg.HangupGrace = defaultHangupGrace
	}
	if cfg.DrainTimeout <= 0 {
		cfg.DrainTimeout = defaultDrainTimeout
	}
	if cfg.CloseTimeout <= 0 {
		cfg.CloseTimeout = defaultCloseTimeout
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}
	config := &ssh.ServerConfig{
		NoClientAuth: true,
		NoClientAuthCallback: func(m ssh.ConnMetadata) (*ssh.Permissions, error) {
			if m.User() != SandboxUser {
				return nil, errors.New("unknown user")
			}
			return nil, nil
		},
		MaxAuthTries:  3,
		ServerVersion: "SSH-2.0-yoloctl_box_endpoint_" + ProtocolVersion,
	}
	config.AddHostKey(signer)
	return &Server{
		cfg:    cfg,
		ssh:    config,
		reaper: newReaper(cfg.PID1),
		slots:  make(chan struct{}, maxConnections),
		conns:  map[net.Conn]struct{}{},
	}, nil
}

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Descriptor exhaustion or an aborted peer must not end PID 1, and
			// with it the box; retry after a short pause.
			if errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE) || errors.Is(err, unix.ECONNABORTED) || errors.Is(err, unix.EINTR) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = nc.Close() // over the connection bound; other peers are unaffected
			continue
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			<-s.slots
			_ = nc.Close()
			continue
		}
		s.conns[nc] = struct{}{}
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.conns, nc)
				s.mu.Unlock()
				<-s.slots
			}()
			s.serveConn(nc)
		}()
	}
}

func (s *Server) serveConn(nc net.Conn) {
	defer func() { _ = nc.Close() }()
	_ = nc.SetDeadline(time.Now().Add(handshakeTimeout))
	sc, chans, reqs, err := ssh.NewServerConn(nc, s.ssh)
	if err != nil {
		return // includes readiness probes, which close after the greeting
	}
	_ = nc.SetDeadline(time.Time{})
	go globalRequests(reqs)
	sessions := make(chan struct{}, maxSessionsPerConn)
	var wg sync.WaitGroup
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "only session channels are served")
			continue
		}
		select {
		case sessions <- struct{}{}:
		default:
			_ = nch.Reject(ssh.ResourceShortage, "too many sessions on this connection")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			<-sessions
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sessions }()
			(&session{srv: s, ch: ch, conn: nc, peerClosed: make(chan struct{})}).serve(chReqs)
		}()
	}
	_ = sc.Close()
	wg.Wait()
}

// Keepalives get a positive reply; nothing else is offered globally.
func globalRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.WantReply {
			_ = req.Reply(req.Type == "keepalive@yoloctl.dev" || req.Type == "keepalive@openssh.com", nil)
		}
	}
}

// Shutdown stops serving and ends every child. In PID-1 mode it signals the
// whole private PID namespace, including root descendants started through
// sudo, then escalates to SIGKILL after grace. Exiting PID 1 afterwards makes
// the kernel kill anything that is still left.
func (s *Server) Shutdown(listeners []net.Listener, grace time.Duration) {
	s.mu.Lock()
	s.closing = true
	conns := make([]net.Conn, 0, len(s.conns))
	for nc := range s.conns {
		conns = append(conns, nc)
	}
	s.mu.Unlock()
	for _, ln := range listeners {
		_ = ln.Close()
	}
	for _, nc := range conns {
		_ = nc.Close()
	}
	s.signalAll(unix.SIGTERM)
	if !s.waitIdle(grace) {
		s.cfg.Logf("children still running after %s; killing them", grace)
		s.signalAll(unix.SIGKILL)
		if !s.waitIdle(killWait) {
			return // keep reaping whatever is left
		}
	}
	s.reaper.stop()
}

func (s *Server) signalAll(sig unix.Signal) {
	if s.cfg.PID1 && os.Getpid() == 1 {
		// kill(-1) from PID 1 reaches every other process in this namespace.
		_ = unix.Kill(-1, sig)
		return
	}
	for _, p := range s.reaper.tracked() {
		p.signalGroup(sig)
	}
}

// waitIdle reports when every tracked leader has been reaped and, as PID 1,
// no other process remains in the namespace.
func (s *Server) waitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if len(s.reaper.tracked()) == 0 && (!s.cfg.PID1 || !otherProcesses()) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		s.reaper.kick()
		time.Sleep(20 * time.Millisecond)
	}
}

// spawn forks one child in its own session (and therefore process group),
// with the mapped identity when configured. Fork and registration exclude the
// reaper, so even an immediate exit is observed for this child.
func (s *Server) spawn(argv, env []string, files []uintptr, ctty bool) (*process, error) {
	attr := &syscall.ProcAttr{
		Dir:   s.cfg.Workdir,
		Env:   env,
		Files: files,
		Sys:   &syscall.SysProcAttr{Setsid: true, Setctty: ctty, Ctty: 0},
	}
	if id := s.cfg.Identity; id != nil {
		attr.Sys.Credential = &syscall.Credential{Uid: id.UID, Gid: id.GID, Groups: id.Groups}
	}
	p := &process{exited: make(chan struct{}), reaped: make(chan struct{})}
	if err := s.reaper.start(p, func() (int, error) { return syscall.ForkExec(argv[0], argv, attr) }); err != nil {
		return nil, err
	}
	return p, nil
}

// endGroup tears a session's process group down exactly once, when its channel
// closes or its leader exits, whichever is first: SIGHUP like a terminal
// hangup, then SIGKILL for whatever is still in the group after grace,
// including members that outlived the leader. The leader stays unreaped
// until after the final signal, so the group ID can never have been reused
// for a signal; only then is it released to the reaper. A process that moved
// to another group or session is outside this group and is ended by stop.
func (s *Server) endGroup(p *process) {
	p.cleanup.Do(func() {
		go func() {
			if !p.groupDone() {
				p.signalGroup(unix.SIGHUP)
				if !waitFor(p.groupDone, s.cfg.HangupGrace) {
					p.signalGroup(unix.SIGKILL)
					// A signal is not completion. Keep the leader and admission
					// reserved even when a descendant cannot exit yet.
					for !p.groupDone() {
						time.Sleep(20 * time.Millisecond)
					}
				}
			}
			<-p.exited // an uninterruptible leader is released when it exits
			s.reaper.release(p)
		}()
	})
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// process is one session's child: the leader of its own session and group.
// Its exit is observed without reaping; reaping happens once, after release.
type process struct {
	pid      int
	mu       sync.Mutex
	status   unix.WaitStatus
	done     bool // exit observed
	released bool // group teardown finished; no further signals
	exited   chan struct{}
	reaped   chan struct{}
	cleanup  sync.Once
	// live is an isolated-test observation seam; nil uses the process table.
	// It is read and changed under mu and is never a runtime setting.
	live func(int) bool
}

func (p *process) observe(ws unix.WaitStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		p.done, p.status = true, ws
		close(p.exited)
	}
}

func (p *process) exitStatus() unix.WaitStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *process) hasExited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}

// signalGroup signals the group only before release, while the unreaped
// leader still reserves the group ID.
func (p *process) signalGroup(sig unix.Signal) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.released || p.pid <= 0 {
		return false
	}
	_ = unix.Kill(-p.pid, sig)
	return true
}

// groupDone reports that the leader has exited and no live member remains.
func (p *process) groupDone() bool {
	p.mu.Lock()
	exited, live := p.done, p.live
	p.mu.Unlock()
	if !exited {
		return false
	}
	if live == nil {
		live = groupLive
	}
	return !live(p.pid)
}

// reaper observes child exits with waitid(WNOWAIT) and reaps each tracked
// leader exactly once, after its release. As PID 1 it also reaps untracked
// zombies whose parent is PID 1 (orphans). It never waits for an arbitrary
// child, so outside PID 1 it is safe in a shared test process.
type reaper struct {
	pid1     bool
	mu       sync.Mutex // fork and registration versus observation and reaping
	procs    map[int]*process
	wake     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newReaper(pid1 bool) *reaper {
	r := &reaper{pid1: pid1, procs: map[int]*process{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
	sig := make(chan os.Signal, 16)
	signal.Notify(sig, unix.SIGCHLD)
	interval := 50 * time.Millisecond
	if pid1 {
		interval = time.Second
	}
	go func() {
		defer signal.Stop(sig)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-sig:
			case <-tick.C:
			case <-r.wake:
			case <-r.done:
				return
			}
			r.poll()
		}
	}()
	return r
}

// stop ends the reaper once nothing it tracks remains.
func (r *reaper) stop() {
	r.stopOnce.Do(func() { close(r.done) })
}

func (r *reaper) start(p *process, fork func() (int, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid, err := fork()
	if err != nil {
		return err
	}
	p.pid = pid
	r.procs[pid] = p
	return nil
}

func (r *reaper) kick() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *reaper) release(p *process) {
	p.mu.Lock()
	p.released = true
	p.mu.Unlock()
	r.kick()
}

func (r *reaper) tracked() []*process {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*process, 0, len(r.procs))
	for _, p := range r.procs {
		out = append(out, p)
	}
	return out
}

func (r *reaper) poll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid, p := range r.procs {
		if !p.hasExited() {
			if ws, ok := exitedNoWait(pid); ok {
				p.observe(ws)
			}
		}
		p.mu.Lock()
		releasable := p.done && p.released
		p.mu.Unlock()
		if !releasable {
			continue
		}
		var ws unix.WaitStatus
		got, err := unix.Wait4(pid, &ws, unix.WNOHANG, nil)
		if got == pid || errors.Is(err, unix.ECHILD) {
			delete(r.procs, pid)
			close(p.reaped)
		}
	}
	if r.pid1 {
		r.reapOrphans()
	}
}

// Linux si_code values for SIGCHLD.
const (
	cldExited = 1
	cldKilled = 2
	cldDumped = 3
)

// The SIGCHLD fields follow siginfo's three-int preamble, aligned to a pointer:
// si_pid, si_uid, si_status.
var sigchldFields = func() uintptr {
	var info unix.Siginfo
	ptr := unsafe.Sizeof(uintptr(0))
	return (unsafe.Offsetof(info.Code) + unsafe.Sizeof(info.Code) + ptr - 1) &^ (ptr - 1)
}()

// exitedNoWait reports pid's exit status if it has exited, leaving it a zombie.
func exitedNoWait(pid int) (unix.WaitStatus, bool) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || info.Signo != int32(unix.SIGCHLD) {
			return 0, false
		}
		break
	}
	base := unsafe.Pointer(&info)
	if int(*(*int32)(unsafe.Add(base, sigchldFields))) != pid {
		return 0, false
	}
	status := uint32(*(*int32)(unsafe.Add(base, sigchldFields+8)))
	switch info.Code {
	case cldExited:
		return unix.WaitStatus((status & 0xff) << 8), true
	case cldKilled:
		return unix.WaitStatus(status & 0x7f), true
	case cldDumped:
		return unix.WaitStatus(status&0x7f | 0x80), true
	}
	return 0, false
}

func (r *reaper) reapOrphans() {
	for _, st := range processTable() {
		if _, tracked := r.procs[st.pid]; tracked || st.state != 'Z' || st.ppid != 1 {
			continue
		}
		var ws unix.WaitStatus
		_, _ = unix.Wait4(st.pid, &ws, unix.WNOHANG, nil)
	}
}

type procStat struct {
	pid, ppid, pgrp int
	state           byte
}

// processTable reads the visible processes' state, parent and group.
func processTable() []procStat {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]procStat, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // already gone
		}
		// The command name may contain spaces and parentheses.
		end := bytes.LastIndexByte(raw, ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 3 || len(fields[0]) != 1 {
			continue
		}
		ppid, err1 := strconv.Atoi(fields[1])
		pgrp, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, procStat{pid: pid, ppid: ppid, pgrp: pgrp, state: fields[0][0]})
	}
	return out
}

// groupLive reports a member of pgrp that has not exited.
func groupLive(pgrp int) bool {
	for _, st := range processTable() {
		if st.pgrp == pgrp && st.state != 'Z' && st.state != 'X' {
			return true
		}
	}
	return false
}

// otherProcesses reports any process besides PID 1 in this namespace.
func otherProcesses() bool {
	for _, st := range processTable() {
		if st.pid != 1 {
			return true
		}
	}
	return false
}

// session is one SSH session channel.
type session struct {
	srv     *Server
	ch      ssh.Channel
	conn    net.Conn // closed only as the bounded-cleanup fallback
	pty     *ptyRequest
	modes   []terminalMode
	started bool
	proc    *process
	// peerClosed is closed once the channel is closed on both sides (the
	// peer answered or sent close, or the connection ended).
	peerClosed chan struct{}
	// work accounts for every goroutine forwarding this session's streams;
	// the session's slot is not released before they end.
	work sync.WaitGroup

	mu     sync.Mutex
	master *os.File // PTY master, once started
	closer sync.Once
	stdin  *os.File // pipe write end without a PTY
}

func (ss *session) serve(reqs <-chan *ssh.Request) {
	for req := range reqs {
		ok, after := ss.handle(req)
		if req.WantReply {
			_ = req.Reply(ok, nil)
		}
		// Start forwarding only after the reply, so a fast child's exit status
		// can never precede the success reply to its own request.
		if after != nil {
			after()
		}
	}
	// The peer closed the channel or the connection is gone.
	close(ss.peerClosed)
	ss.teardown()
	ss.work.Wait()
	// Keep this session's admission slot, and so its connection's, until its
	// process group is cleaned up and its leader reaped. The exit report and
	// channel close above do not wait for this. A process that cannot exit
	// keeps the slot occupied; no timeout returns capacity early.
	ss.mu.Lock()
	p := ss.proc
	ss.mu.Unlock()
	if p != nil {
		<-p.reaped
	}
}

func (ss *session) goWork(f func()) {
	ss.work.Add(1)
	go func() {
		defer ss.work.Done()
		f()
	}()
}

func (ss *session) handle(req *ssh.Request) (bool, func()) {
	switch req.Type {
	case "pty-req":
		if ss.started {
			return false, nil
		}
		var p ptyRequest
		if ssh.Unmarshal(req.Payload, &p) != nil || !validTerm(p.Term) {
			return false, nil
		}
		modes, err := parseModes(p.Modes)
		if err != nil {
			return false, nil
		}
		ss.pty, ss.modes = &p, modes
		return true, nil
	case "window-change":
		var w windowChange
		if ssh.Unmarshal(req.Payload, &w) != nil {
			return false, nil
		}
		return ss.resize(w), nil
	case "shell", "exec":
		if ss.started {
			return false, nil
		}
		command, hasCommand := "", false
		if req.Type == "exec" {
			var e struct{ Command string }
			if ssh.Unmarshal(req.Payload, &e) != nil || e.Command == "" || len(e.Command) > maxCommandBytes || strings.ContainsRune(e.Command, 0) {
				return false, nil
			}
			command, hasCommand = e.Command, true
		} else if len(req.Payload) != 0 {
			return false, nil
		}
		ss.started = true
		after, err := ss.start(shellArgv(command, hasCommand, ss.pty != nil))
		if err != nil {
			ss.srv.cfg.Logf("session start failed: %v", err)
			return false, nil
		}
		return true, after
	default:
		// env, signal, subsystem, x11, agent forwarding and anything unknown.
		return false, nil
	}
}

func (ss *session) resize(w windowChange) bool {
	ss.mu.Lock()
	master := ss.master
	ss.mu.Unlock()
	if master == nil {
		if ss.pty == nil || ss.started {
			return false
		}
		ss.pty.Cols, ss.pty.Rows, ss.pty.Width, ss.pty.Height = w.Cols, w.Rows, w.Width, w.Height
		return true
	}
	ws := winsize(w.Cols, w.Rows, w.Width, w.Height)
	rc, err := master.SyscallConn()
	if err != nil {
		return false
	}
	var ioctlErr error
	if err := rc.Control(func(fd uintptr) { ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, ws) }); err != nil {
		return false
	}
	return ioctlErr == nil
}

func winsize(cols, rows, width, height uint32) *unix.Winsize {
	c, r, x, y := clampWinsize(cols, rows, width, height)
	return &unix.Winsize{Row: r, Col: c, Xpixel: x, Ypixel: y}
}

func (ss *session) start(argv []string) (func(), error) {
	if ss.pty != nil {
		return ss.startPTY(argv)
	}
	return ss.startPipes(argv)
}

func (ss *session) startPTY(argv []string) (func(), error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(slave) }()
	if err := applyModes(slave, ss.modes); err != nil {
		_ = master.Close()
		return nil, err
	}
	if err := unix.IoctlSetWinsize(slave, unix.TIOCSWINSZ, winsize(ss.pty.Cols, ss.pty.Rows, ss.pty.Width, ss.pty.Height)); err != nil {
		_ = master.Close()
		return nil, err
	}
	term := ss.pty.Term
	if term == "" {
		term = "xterm-256color"
	}
	fd := uintptr(slave)
	p, err := ss.srv.spawn(argv, WithTerm(ss.srv.cfg.Env, term), []uintptr{fd, fd, fd}, true)
	if err != nil {
		_ = master.Close()
		return nil, err
	}
	ss.mu.Lock()
	ss.proc, ss.master = p, master
	ss.mu.Unlock()
	closeMaster := func() { ss.closer.Do(func() { _ = master.Close() }) }
	return func() {
		output := make(chan struct{})
		ss.goWork(func() { defer close(output); _, _ = io.Copy(ss.ch, master) })
		// Input EOF leaves the terminal open, as for a confined PTY session.
		ss.goWork(func() { _, _ = io.Copy(master, ss.ch) })
		ss.goWork(func() { ss.finish(p, []chan struct{}{output}, closeMaster) })
	}, nil
}

func (ss *session) startPipes(argv []string) (func(), error) {
	var files [6]*os.File
	closeAll := func() {
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
	}
	var err error
	for i := 0; i < 3; i++ {
		if files[2*i], files[2*i+1], err = os.Pipe(); err != nil {
			closeAll()
			return nil, err
		}
	}
	stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW := files[0], files[1], files[2], files[3], files[4], files[5]
	p, err := ss.srv.spawn(argv, WithTerm(ss.srv.cfg.Env, "dumb"), []uintptr{stdinR.Fd(), stdoutW.Fd(), stderrW.Fd()}, false)
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	if err != nil {
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stderrR.Close()
		return nil, err
	}
	ss.mu.Lock()
	ss.proc, ss.stdin = p, stdinW
	ss.mu.Unlock()
	closeOutputs := func() { _ = stdoutR.Close(); _ = stderrR.Close(); _ = stdinW.Close() }
	return func() {
		stdout, stderr := make(chan struct{}), make(chan struct{})
		ss.goWork(func() { defer close(stdout); _, _ = io.Copy(ss.ch, stdoutR) })
		ss.goWork(func() { defer close(stderr); _, _ = io.Copy(ss.ch.Stderr(), stderrR) })
		ss.goWork(func() { _, _ = io.Copy(stdinW, ss.ch); _ = stdinW.Close() })
		ss.goWork(func() { ss.finish(p, []chan struct{}{stdout, stderr}, closeOutputs) })
	}, nil
}

// finish reports the child's exit after its output, in protocol order: data,
// EOF, exit status or signal, close. Every step is bounded:
//
//   - stdout and stderr share one drain deadline after the exit;
//   - on expiry the sources close; EOF is omitted while copies remain
//     unfinished, so no copy can send data after it. Joined copies allow EOF
//     but do not prove complete output delivery after forced source closure.
//     Status is still sent (requests are not limited by the channel window);
//   - the exit report and close must leave within CloseTimeout, and the peer
//     must then close its side within CloseTimeout. x/crypto wakes a parked
//     copy only when the peer answers close or the connection ends, so a peer
//     or transport that withholds that progress loses this whole SSH
//     connection, including sibling sessions on it. Other connections and a
//     cooperative peer's siblings are unaffected.
func (ss *session) finish(p *process, outputs []chan struct{}, closeOutputs func()) {
	<-p.exited
	// Descendants that keep the outputs open belong to the same group.
	ss.srv.endGroup(p)
	complete := waitAll(outputs, ss.srv.cfg.DrainTimeout)
	closeOutputs()
	if !complete {
		complete = waitAll(outputs, abandonGrace)
	}
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		if complete {
			_ = ss.ch.CloseWrite()
		}
		sendExit(ss.ch, p.exitStatus())
		_ = ss.ch.Close()
	}()
	select {
	case <-sent:
		select {
		case <-ss.peerClosed:
		case <-time.After(ss.srv.cfg.CloseTimeout):
			ss.closeConnection()
		}
	case <-time.After(ss.srv.cfg.CloseTimeout):
		ss.closeConnection()
	}
	// Both closing the channel and ending the connection release parked copies.
	waitAll(outputs, 0)
	<-sent
}

// waitAll waits for every channel to close, up to timeout (zero: no limit).
func waitAll(done []chan struct{}, timeout time.Duration) bool {
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	for _, d := range done {
		select {
		case <-d:
		case <-expired:
			return false
		}
	}
	return true
}

func (ss *session) closeConnection() {
	ss.srv.cfg.Logf("SSH connection withheld channel shutdown; closing it")
	_ = ss.conn.Close()
}

func (ss *session) teardown() {
	ss.mu.Lock()
	p, master, stdin := ss.proc, ss.master, ss.stdin
	ss.mu.Unlock()
	if p == nil {
		_ = ss.ch.Close()
		return
	}
	if master != nil {
		ss.closer.Do(func() { _ = master.Close() })
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	ss.srv.endGroup(p)
}

// rfcSignals are the RFC 4254 signal names x/crypto clients map to 128+n.
// Other signals are reported as that status directly.
var rfcSignals = map[unix.Signal]string{
	unix.SIGABRT: "ABRT", unix.SIGALRM: "ALRM", unix.SIGFPE: "FPE", unix.SIGHUP: "HUP",
	unix.SIGILL: "ILL", unix.SIGINT: "INT", unix.SIGKILL: "KILL", unix.SIGPIPE: "PIPE",
	unix.SIGQUIT: "QUIT", unix.SIGSEGV: "SEGV", unix.SIGTERM: "TERM",
}

func sendExit(ch ssh.Channel, ws unix.WaitStatus) {
	status := ws.ExitStatus()
	if ws.Signaled() {
		if name, ok := rfcSignals[ws.Signal()]; ok {
			msg := struct {
				Signal     string
				CoreDumped bool
				Error      string
				Lang       string
			}{name, ws.CoreDump(), "", ""}
			_, _ = ch.SendRequest("exit-signal", false, ssh.Marshal(&msg))
			return
		}
		status = 128 + int(ws.Signal())
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
}

// openPTY returns a pollable master (so Close interrupts a blocked read) and
// the peer descriptor, obtained from the master without a /dev/pts lookup.
func openPTY() (*os.File, int, error) {
	m, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, -1, err
	}
	if err := unix.IoctlSetPointerInt(m, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(m)
		return nil, -1, err
	}
	s, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(m), uintptr(unix.TIOCGPTPEER), uintptr(unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC))
	if errno != 0 {
		_ = unix.Close(m)
		return nil, -1, errno
	}
	if err := unix.SetNonblock(m, true); err != nil {
		_ = unix.Close(m)
		_ = unix.Close(int(s))
		return nil, -1, err
	}
	return os.NewFile(uintptr(m), "ptmx"), int(s), nil
}

var ccModes = map[byte]int{
	ssh.VINTR: unix.VINTR, ssh.VQUIT: unix.VQUIT, ssh.VERASE: unix.VERASE, ssh.VKILL: unix.VKILL,
	ssh.VEOF: unix.VEOF, ssh.VEOL: unix.VEOL, ssh.VEOL2: unix.VEOL2, ssh.VSTART: unix.VSTART,
	ssh.VSTOP: unix.VSTOP, ssh.VSUSP: unix.VSUSP, ssh.VREPRINT: unix.VREPRINT,
	ssh.VWERASE: unix.VWERASE, ssh.VLNEXT: unix.VLNEXT, ssh.VDISCARD: unix.VDISCARD,
}

type modeFlag struct {
	field byte // i, o, c or l
	bits  uint32
}

var flagModes = map[byte]modeFlag{
	ssh.IGNPAR: {'i', unix.IGNPAR}, ssh.PARMRK: {'i', unix.PARMRK}, ssh.INPCK: {'i', unix.INPCK},
	ssh.ISTRIP: {'i', unix.ISTRIP}, ssh.INLCR: {'i', unix.INLCR}, ssh.IGNCR: {'i', unix.IGNCR},
	ssh.ICRNL: {'i', unix.ICRNL}, ssh.IUCLC: {'i', unix.IUCLC}, ssh.IXON: {'i', unix.IXON},
	ssh.IXANY: {'i', unix.IXANY}, ssh.IXOFF: {'i', unix.IXOFF}, ssh.IMAXBEL: {'i', unix.IMAXBEL},
	ssh.IUTF8: {'i', unix.IUTF8},
	ssh.ISIG:  {'l', unix.ISIG}, ssh.ICANON: {'l', unix.ICANON}, ssh.XCASE: {'l', unix.XCASE},
	ssh.ECHO: {'l', unix.ECHO}, ssh.ECHOE: {'l', unix.ECHOE}, ssh.ECHOK: {'l', unix.ECHOK},
	ssh.ECHONL: {'l', unix.ECHONL}, ssh.NOFLSH: {'l', unix.NOFLSH}, ssh.TOSTOP: {'l', unix.TOSTOP},
	ssh.IEXTEN: {'l', unix.IEXTEN}, ssh.ECHOCTL: {'l', unix.ECHOCTL}, ssh.ECHOKE: {'l', unix.ECHOKE},
	ssh.PENDIN: {'l', unix.PENDIN},
	ssh.OPOST:  {'o', unix.OPOST}, ssh.OLCUC: {'o', unix.OLCUC}, ssh.ONLCR: {'o', unix.ONLCR},
	ssh.OCRNL: {'o', unix.OCRNL}, ssh.ONOCR: {'o', unix.ONOCR}, ssh.ONLRET: {'o', unix.ONLRET},
	ssh.CS7: {'c', unix.CS7}, ssh.CS8: {'c', unix.CS8}, ssh.PARENB: {'c', unix.PARENB},
	ssh.PARODD: {'c', unix.PARODD},
}

// applyModes applies the requested terminal modes to the new terminal before
// the child starts. Speeds and modes Linux lacks are accepted and ignored.
func applyModes(fd int, modes []terminalMode) error {
	if len(modes) == 0 {
		return nil
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	for _, m := range modes {
		if index, ok := ccModes[m.Opcode]; ok {
			switch {
			case m.Value == 255:
				t.Cc[index] = 0 // _POSIX_VDISABLE
			case m.Value < 255:
				t.Cc[index] = byte(m.Value)
			}
			continue
		}
		f, ok := flagModes[m.Opcode]
		if !ok {
			continue
		}
		var field *uint32
		switch f.field {
		case 'i':
			field = &t.Iflag
		case 'o':
			field = &t.Oflag
		case 'c':
			field = &t.Cflag
		default:
			field = &t.Lflag
		}
		if m.Value != 0 {
			*field |= f.bits
		} else {
			*field &^= f.bits
		}
	}
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}

// Main runs the endpoint as the container's root PID 1 and returns its exit
// status. It never initializes CLI configuration or controller state.
func Main(args []string) int {
	logf := func(format string, a ...any) {
		_, _ = fmt.Fprintf(os.Stderr, "yoloctl box endpoint: "+format+"\n", a...)
	}
	if err := runPID1(args, logf); err != nil {
		logf("%v", err)
		return 1
	}
	return 0
}

func runPID1(args []string, logf func(string, ...any)) error {
	opts, err := ParseArgs(args)
	if err != nil {
		return err
	}
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return errors.New("the box endpoint runs only as the container's root PID 1")
	}
	// Register before anything can fail later, so an early stop is not lost.
	// SIGPIPE is handled (not ignored) so children still inherit its default.
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, unix.SIGTERM, unix.SIGINT)
	signal.Notify(make(chan os.Signal, 1), unix.SIGPIPE, unix.SIGHUP)
	if st, err := os.Stat(opts.Workdir); err != nil || !st.IsDir() {
		return fmt.Errorf("supervised workdir %s is unavailable", opts.Workdir)
	}
	id, home, err := lookupSandbox()
	if err != nil {
		return err
	}
	env, err := ChildEnvironment(home, SandboxUser, os.Getenv("PATH"), os.Getenv(UserEnvironmentVariable))
	if err != nil {
		return err
	}
	srv, err := NewServer(Config{Workdir: opts.Workdir, Env: env, Identity: id, PID1: true, Logf: logf})
	if err != nil {
		return err
	}
	ln, err := ListenPrivate(opts.SocketPath)
	if err != nil {
		return err
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	logf("ready (%s)", Launcher)
	select {
	case <-stop:
	case err := <-served:
		if err != nil {
			logf("listener failed: %v", err)
		}
	}
	srv.Shutdown([]net.Listener{ln}, shutdownGrace)
	return nil
}

// lookupSandbox resolves the identity after the entrypoint shim's remap.
func lookupSandbox() (*Identity, string, error) {
	u, err := user.Lookup(SandboxUser)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s user: %w", SandboxUser, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, "", err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, "", err
	}
	if uid == 0 || gid == 0 {
		return nil, "", errors.New("refusing to run sandbox children with root identity")
	}
	ids, err := u.GroupIds()
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s groups: %w", SandboxUser, err)
	}
	id := &Identity{UID: uint32(uid), GID: uint32(gid)}
	for _, raw := range ids {
		g, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return nil, "", err
		}
		id.Groups = append(id.Groups, uint32(g))
	}
	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return nil, "", errors.New("sandbox user has no absolute home directory")
	}
	return id, u.HomeDir, nil
}

// ListenPrivate binds a Unix socket that only its owner can connect to, in a
// directory owned by this user and writable by no one else. A stale socket is
// replaced; any other existing entry (including a symlink) is refused.
func ListenPrivate(socketPath string) (net.Listener, error) {
	dir := filepath.Dir(socketPath)
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return nil, fmt.Errorf("endpoint socket directory: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Geteuid()) || st.Mode&0o022 != 0 {
		return nil, fmt.Errorf("endpoint socket directory %s is not private", dir)
	}
	if err := unix.Lstat(socketPath, &st); err == nil {
		if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
			return nil, fmt.Errorf("refusing to replace non-socket %s", socketPath)
		}
		if err := unix.Unlink(socketPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	old := unix.Umask(0o177)
	ln, err := net.Listen("unix", socketPath)
	unix.Umask(old)
	return ln, err
}
