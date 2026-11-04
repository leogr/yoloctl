//go:build linux

package vmguest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

const controllerGroup = "/sys/fs/cgroup/yoloctl-controller"

var controllerDelays = [...]time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}

type controllerProcess interface {
	wait() error
	terminate() error
}

type controllerChild struct{ cmd *exec.Cmd }

func (p controllerChild) wait() error {
	err := p.cmd.Wait()
	ownedChildren.Delete(p.cmd.Process.Pid)
	return err
}
func (p controllerChild) terminate() error { return p.cmd.Process.Signal(syscall.SIGTERM) }

// Only this fixed launcher enters the controller cgroup. Each successful spawn
// has exactly one Wait, registered against PID 1's orphan reaper by startOwned.
func launchController() (controllerProcess, error) {
	if err := prepareGroup(controllerGroup); err != nil {
		return nil, err
	}
	fd, err := unix.Open(controllerGroup, unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	cmd := exec.Command(vmwire.GuestBinary, "controller-internal", "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, UseCgroupFD: true, CgroupFD: fd}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return startAnchoredController(cmd)
}

type controllerHooks struct {
	spawn func() (controllerProcess, error)
	clean func() error
	now   func() time.Time
	timer func(time.Duration) *time.Timer
	logf  func(string, ...any)
}

type controllerRun struct {
	cancel      chan struct{}
	done        chan struct{}
	first       chan error
	stopping    bool
	finished    bool
	state       string
	cleanupErr  error
	attempts    int
	episodeOpen bool
}

// A run's worker exclusively owns spawn, Wait, cgroup cleanup and its timers.
// Stop only cancels and joins that worker. It never cleans a shared path from
// an old callback, so it cannot kill a later explicit start's incarnation.
// Counters belong to PID 1, surviving explicit service stop/start in memory.
type controllerSupervisor struct {
	mu                sync.Mutex
	hooks             controllerHooks
	run               *controllerRun
	shuttingDown      bool
	episodes          uint64
	abnormalExits     uint64
	automaticRestarts uint64
}

func newControllerSupervisor(h controllerHooks) *controllerSupervisor {
	return &controllerSupervisor{hooks: h}
}

var controllers = newControllerSupervisor(controllerHooks{
	spawn: launchController,
	clean: func() error { return cleanGroup(controllerGroup) },
	now:   time.Now, timer: time.NewTimer,
	logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, "controller: "+format+"\n", args...) },
})

func (s *controllerSupervisor) start() error {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return errors.New("guest is shutting down")
	}
	if r := s.run; r != nil {
		if r.stopping {
			s.mu.Unlock()
			return errors.New("controller is stopping; retry after stop completes")
		}
		if !r.finished {
			s.mu.Unlock()
			return nil
		}
		if r.cleanupErr != nil {
			s.mu.Unlock()
			return r.cleanupErr
		}
	}
	r := &controllerRun{cancel: make(chan struct{}), done: make(chan struct{}), first: make(chan error, 1), state: "activating"}
	s.run = r
	s.mu.Unlock()
	go s.watch(r)
	return <-r.first
}

func (s *controllerSupervisor) state() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return "inactive", nil
	}
	return s.run.state, s.run.cleanupErr
}

func (s *controllerSupervisor) cancelLocked(r *controllerRun) {
	if !r.stopping && !r.finished {
		r.stopping = true
		r.state = "deactivating"
		close(r.cancel)
	}
}
func (s *controllerSupervisor) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shuttingDown = true
	if s.run != nil {
		s.cancelLocked(s.run)
	}
}
func (s *controllerSupervisor) stop() error {
	deadline := s.hooks.timer(20 * time.Second)
	defer deadline.Stop()
	s.mu.Lock()
	r := s.run
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	s.cancelLocked(r)
	s.mu.Unlock()
	select {
	case <-r.done:
		s.mu.Lock()
		r.stopping = false
		err := r.cleanupErr
		s.mu.Unlock()
		return err
	case <-deadline.C:
		return errors.New("controller did not stop within 20 seconds")
	}
}

// Caller holds mu. No environment, request bodies or secret values are logged.
func (s *controllerSupervisor) log(r *controllerRun, format string, args ...any) {
	prefix := fmt.Sprintf("episode=%d abnormal_exits=%d automatic_restarts=%d attempt=%d ", s.episodes, s.abnormalExits, s.automaticRestarts, r.attempts)
	s.hooks.logf(prefix+format, args...)
}
func (s *controllerSupervisor) finish(r *controllerRun, state string, cleanup error) {
	r.state, r.cleanupErr, r.finished = state, cleanup, true
	close(r.done)
}

// Cleanup is also an activating transition: a dead child's former workers
// are not a running controller and are not yet safe for deployment.
func (s *controllerSupervisor) cleanup(r *controllerRun) error {
	s.mu.Lock()
	if !r.stopping {
		r.state = "activating"
	}
	s.mu.Unlock()
	return s.hooks.clean()
}

type controllerExit struct {
	err       error
	at        time.Time
	requested bool
}

// Snapshot whether exit was requested before cleanup: a later stop may
// cancel the retry but must not hide a crash already observed in the counters.
func (s *controllerSupervisor) cleanedExit(r *controllerRun, e controllerExit) (controllerExit, error) {
	s.mu.Lock()
	e.requested = r.stopping
	s.mu.Unlock()
	return e, s.cleanup(r)
}

// A TERM-ignoring child is killed through its entire cgroup after ten seconds.
// The worker remains the sole cleanup owner, including on forced stop. The
// outer stop's twenty-second deadline also bounds an unjoinable child.
func (s *controllerSupervisor) wait(r *controllerRun, p controllerProcess) (controllerExit, error) {
	exited := make(chan controllerExit, 1)
	go func() { err := p.wait(); exited <- controllerExit{err: err, at: s.hooks.now()} }()
	select {
	case e := <-exited:
		return s.cleanedExit(r, e)
	case <-r.cancel:
	}
	if err := p.terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.hooks.logf("TERM failed: %v; forcing cgroup cleanup", err)
	} else {
		grace := s.hooks.timer(10 * time.Second)
		defer grace.Stop()
		select {
		case e := <-exited:
			return s.cleanedExit(r, e)
		case <-grace.C:
		}
	}
	cleanup := s.cleanup(r)
	// Even if cleanup failed, never release this run for replacement until Wait
	// joins. The stop deadline reports failure if the process cannot be reaped.
	e := <-exited
	e.requested = true
	return e, cleanup
}

func (s *controllerSupervisor) watch(r *controllerRun) {
	initial := true
	for {
		s.mu.Lock()
		if r.stopping {
			s.finish(r, "inactive", nil)
			s.mu.Unlock()
			if initial {
				r.first <- errors.New("controller start cancelled by stop")
			}
			return
		}
		if !initial {
			r.attempts++
			s.automaticRestarts++
			s.log(r, "automatic launch")
		}
		s.mu.Unlock()

		p, launchErr := s.hooks.spawn()
		started := s.hooks.now()
		var exit controllerExit
		var cleanup error
		if launchErr == nil {
			s.mu.Lock()
			if !r.stopping {
				r.state = "active"
			}
			s.mu.Unlock()
			if initial {
				r.first <- nil
			}
			exit, cleanup = s.wait(r, p)
		} else {
			exit = controllerExit{err: launchErr, at: s.hooks.now()}
			exit, cleanup = s.cleanedExit(r, exit)
		}

		s.mu.Lock()
		if exit.err != nil && !exit.requested {
			if launchErr == nil {
				s.abnormalExits++
				if exit.at.Sub(started) >= 60*time.Second && r.episodeOpen {
					s.log(r, "uptime budget reset after %s", exit.at.Sub(started))
					r.attempts, r.episodeOpen = 0, false
				}
			}
			if !r.episodeOpen {
				s.episodes++
				r.episodeOpen = true
			}
		}
		s.log(r, "exit=%v launch_error=%v cleanup=%v", exit.err, launchErr, cleanup)
		switch {
		case cleanup != nil:
			s.finish(r, "failed", cleanup)
		case r.stopping:
			s.finish(r, "inactive", nil)
		case launchErr != nil && initial:
			s.finish(r, "failed", nil)
		case exit.err == nil:
			s.finish(r, "inactive", nil)
		case r.attempts == len(controllerDelays):
			s.log(r, "retry budget exhausted: %v", exit.err)
			s.finish(r, "failed", nil)
		}
		if r.finished {
			s.mu.Unlock()
			if initial && launchErr != nil {
				r.first <- launchErr
			}
			return
		}
		r.state = "activating"
		delay := controllerDelays[r.attempts]
		s.log(r, "restart scheduled delay=%s reason=%v", delay, exit.err)
		s.mu.Unlock()
		timer := s.hooks.timer(delay)
		select {
		case <-timer.C:
		case <-r.cancel:
		}
		timer.Stop()
		initial = false
	}
}
