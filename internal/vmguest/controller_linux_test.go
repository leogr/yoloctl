//go:build linux

package vmguest

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type fakeController struct {
	exit       chan error
	ignoreTERM bool
	terms      int
}

func (p *fakeController) wait() error { return <-p.exit }
func (p *fakeController) terminate() error {
	p.terms++
	if !p.ignoreTERM {
		p.exit <- errors.New("TERM")
	}
	return nil
}

type controllerFixture struct {
	mu         sync.Mutex
	s          *controllerSupervisor
	children   []*fakeController
	launches   []time.Time
	logs       []string
	cleanCalls int
	spawn      func() error
	clean      func() error
}

func newControllerFixture() *controllerFixture {
	f := &controllerFixture{}
	f.s = newControllerSupervisor(controllerHooks{
		spawn: func() (controllerProcess, error) {
			f.mu.Lock()
			f.launches = append(f.launches, time.Now())
			spawn := f.spawn
			f.mu.Unlock()
			if spawn != nil {
				if err := spawn(); err != nil {
					return nil, err
				}
			}
			p := &fakeController{exit: make(chan error, 1)}
			f.mu.Lock()
			f.children = append(f.children, p)
			f.mu.Unlock()
			return p, nil
		},
		clean: func() error {
			f.mu.Lock()
			f.cleanCalls++
			clean := f.clean
			f.mu.Unlock()
			if clean != nil {
				return clean()
			}
			return nil
		},
		now: time.Now, timer: time.NewTimer,
		logf: func(format string, args ...any) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.logs = append(f.logs, fmt.Sprintf(format, args...))
		},
	})
	return f
}
func (f *controllerFixture) times() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.launches...)
}
func (f *controllerFixture) logLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}
func (f *controllerFixture) cleanCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.cleanCalls }
func (f *controllerFixture) child(i int) *fakeController {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 {
		i = len(f.children) - 1
	}
	return f.children[i]
}
func (f *controllerFixture) setSpawn(fn func() error) { f.mu.Lock(); defer f.mu.Unlock(); f.spawn = fn }
func (f *controllerFixture) setClean(fn func() error) { f.mu.Lock(); defer f.mu.Unlock(); f.clean = fn }

type controllerCounts struct{ episodes, exits, restarts uint64 }

func (s *controllerSupervisor) counts() controllerCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return controllerCounts{s.episodes, s.abnormalExits, s.automaticRestarts}
}
func (f *controllerFixture) start(t *testing.T) {
	t.Helper()
	if err := f.s.start(); err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
}
func (f *controllerFixture) state(t *testing.T, want string) {
	t.Helper()
	synctest.Wait()
	got, _ := f.s.state()
	if got != want {
		t.Fatalf("state=%s want=%s logs=%v", got, want, f.logLines())
	}
}
func (f *controllerFixture) crash() {
	f.child(-1).exit <- errors.New("exit 1")
	synctest.Wait()
}
func (f *controllerFixture) stop(t *testing.T) {
	t.Helper()
	if err := f.s.stop(); err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
}

func TestControllerRestartRequiresCleanupAndDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newControllerFixture()
		f.start(t)
		release := make(chan struct{})
		f.setClean(func() error { <-release; return nil })
		f.crash()
		f.state(t, "activating")
		time.Sleep(30 * time.Second)
		if len(f.times()) != 1 {
			t.Fatal("spawned before cleanup proof")
		}
		if err := f.s.start(); err != nil {
			t.Fatal(err)
		}
		close(release)
		synctest.Wait()
		time.Sleep(5*time.Second - time.Nanosecond)
		synctest.Wait()
		if len(f.times()) != 1 {
			t.Fatal("spawned before delay")
		}
		time.Sleep(time.Nanosecond)
		f.state(t, "active")
		if len(f.times()) != 2 || f.cleanCount() != 1 {
			t.Fatal(f.times(), f.cleanCount())
		}
		f.stop(t)
	})
}

func TestControllerRetryBudgetAndExplicitReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newControllerFixture()
		f.start(t)
		for i, delay := range controllerDelays {
			f.crash()
			f.state(t, "activating")
			for range 3 {
				if err := f.s.start(); err != nil {
					t.Fatal(err)
				}
			}
			before := time.Now()
			time.Sleep(delay)
			f.state(t, "active")
			if len(f.times()) != i+2 || !f.times()[i+1].Equal(before.Add(delay)) {
				t.Fatal(f.times())
			}
		}
		f.crash()
		f.state(t, "failed")
		if _, err := f.s.state(); err != nil {
			t.Fatal("exit failure broke state read", err)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(f.times()) != 6 {
			t.Fatal("exhaustion did not latch")
		}
		if f.s.counts().episodes != 1 || f.s.counts().exits != 6 || f.s.counts().restarts != 5 {
			t.Fatal(f.logLines())
		}
		f.start(t)
		f.crash()
		time.Sleep(5 * time.Second)
		f.state(t, "active")
		if f.s.counts().episodes != 2 || f.s.counts().restarts != 6 {
			t.Fatal(f.logLines())
		}
		f.stop(t)
		f.start(t)
		f.crash()
		if f.s.counts().episodes != 3 {
			t.Fatal("episode lost across explicit stop/start", f.logLines())
		}
		f.stop(t)
	})
}

func TestControllerUptimeEpisodeBoundary(t *testing.T) {
	for _, uptime := range []time.Duration{60*time.Second - time.Nanosecond, 60 * time.Second, 70 * time.Second} {
		t.Run(uptime.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newControllerFixture()
				f.start(t)
				f.crash()
				time.Sleep(5 * time.Second)
				f.state(t, "active")
				time.Sleep(uptime)
				f.crash()
				delay, episode := 10*time.Second, uint64(1)
				if uptime >= 60*time.Second {
					delay, episode = 5*time.Second, 2
				}
				time.Sleep(delay)
				f.state(t, "active")
				if f.s.counts().episodes != episode || f.s.counts().exits != 2 || f.s.counts().restarts != 2 {
					t.Fatal(f.logLines())
				}
				if uptime >= 60*time.Second {
					joined := strings.Join(f.logLines(), "\n")
					if !strings.Contains(joined, "episode=1 abnormal_exits=2 automatic_restarts=1 attempt=1 uptime budget reset") || !strings.Contains(joined, "episode=2 abnormal_exits=2 automatic_restarts=1 attempt=0 restart scheduled delay=5s") {
						t.Fatal(joined)
					}
				}
				f.stop(t)
			})
		})
	}
}

func TestControllerSpawnFailureAndCleanupLatch(t *testing.T) {
	for _, initial := range []bool{true, false} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newControllerFixture()
				fail := errors.New("spawn failure")
				if initial {
					f.setSpawn(func() error { return fail })
					if err := f.s.start(); !errors.Is(err, fail) {
						t.Fatal(err)
					}
					f.state(t, "failed")
					time.Sleep(time.Hour)
					synctest.Wait()
					if len(f.times()) != 1 {
						t.Fatal("explicit launch error retried")
					}
					f.setSpawn(nil)
					f.start(t)
				} else {
					f.start(t)
					f.crash()
					f.setSpawn(func() error { return fail })
					for _, delay := range controllerDelays {
						time.Sleep(delay)
						synctest.Wait()
					}
					f.state(t, "failed")
					if len(f.times()) != 6 || f.cleanCount() != 6 || f.s.counts().restarts != 5 {
						t.Fatal(f.logLines())
					}
					f.setSpawn(nil)
					f.start(t)
				}
				dirty := errors.New("empty group unproven")
				f.setClean(func() error { return dirty })
				f.crash()
				f.state(t, "failed")
				if err := f.s.start(); !errors.Is(err, dirty) {
					t.Fatal("start bypassed cleanup failure", err)
				}
				if err := f.s.stop(); !errors.Is(err, dirty) {
					t.Fatal("stop erased cleanup failure", err)
				}
			})
		})
	}
}

func TestControllerCleanExitAndRequestedStopNeverRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newControllerFixture()
		f.start(t)
		f.child(0).exit <- nil
		f.state(t, "inactive")
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(f.times()) != 1 {
			t.Fatal("clean exit restarted")
		}
		f.start(t)
		f.stop(t)
		f.state(t, "inactive")
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(f.times()) != 2 || f.s.counts().exits != 0 {
			t.Fatal(f.logLines())
		}
	})
}

func TestControllerStopDuringEveryTransition(t *testing.T) {
	for _, phase := range []string{"start", "backoff", "cleanup", "exit"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newControllerFixture()
				release := make(chan struct{})
				first := make(chan error, 1)
				if phase == "start" {
					f.setSpawn(func() error { <-release; return nil })
					go func() { first <- f.s.start() }()
					synctest.Wait()
				} else {
					f.start(t)
					if phase == "cleanup" {
						f.setClean(func() error { <-release; return nil })
					}
					f.crash()
				}
				stopped := make(chan error, 1)
				go func() { stopped <- f.s.stop() }()
				synctest.Wait()
				if phase == "start" || phase == "cleanup" {
					f.state(t, "deactivating")
					if err := f.s.start(); err == nil {
						t.Fatal("start admitted while stopping")
					}
					close(release)
				}
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
				if phase == "start" {
					if err := <-first; err != nil {
						t.Fatal(err)
					}
				}
				f.state(t, "inactive")
				n := len(f.times())
				// A new explicit incarnation cannot be affected by old timers or cleanup.
				f.setSpawn(nil)
				f.setClean(nil)
				f.start(t)
				time.Sleep(time.Hour)
				f.state(t, "active")
				if len(f.times()) != n+1 {
					t.Fatal("stale restart fired", f.logLines())
				}
				f.stop(t)
			})
		})
	}
}

func TestControllerConcurrentStartStopAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newControllerFixture()
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if err := f.s.start(); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		synctest.Wait()
		if len(f.times()) != 1 {
			t.Fatal("duplicate concurrent launch")
		}
		f.crash()
		f.s.shutdown()
		for range 20 {
			wg.Go(func() {
				if err := f.s.stop(); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		f.state(t, "inactive")
		if err := f.s.start(); err == nil {
			t.Fatal("start passed shutdown latch")
		}
		f.s.shutdown() // Every shutdown entry remains terminal, including repeats.
		time.Sleep(time.Hour)
		synctest.Wait()
		if len(f.times()) != 1 {
			t.Fatal("poweroff restarted controller")
		}
	})
}

func TestControllerForcedStopAndUnjoinableChild(t *testing.T) {
	for _, join := range []bool{true, false} {
		t.Run(fmt.Sprint(join), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newControllerFixture()
				f.start(t)
				p := f.child(0)
				p.ignoreTERM = true
				f.setClean(func() error {
					if join {
						p.exit <- errors.New("KILL")
						return nil
					}
					return errors.New("group did not terminate")
				})
				before := time.Now()
				err := f.s.stop()
				if join {
					if err != nil || time.Since(before) != 10*time.Second {
						t.Fatal(err, time.Since(before))
					}
				} else {
					if err == nil || time.Since(before) != 20*time.Second {
						t.Fatal(err, time.Since(before))
					}
					if err := f.s.start(); err == nil {
						t.Fatal("unjoined run replaced")
					}
					p.exit <- errors.New("late reap")
					synctest.Wait()
					if err := f.s.stop(); err == nil {
						t.Fatal("cleanup error erased")
					}
				}
				if p.terms != 1 || f.cleanCount() != 1 {
					t.Fatal(p.terms, f.cleanCount())
				}
			})
		})
	}
}

// Real children verify signal exit classification and owned-child Wait handling.
// Only these exact test-owned children receive signals. No VM/cgroup required.
func TestControllerRealChildFatalSignal(t *testing.T) {
	for _, kill := range []bool{true, false} {
		t.Run(fmt.Sprint(kill), func(t *testing.T) {
			cmd := exec.Command("sleep", "30")
			s := newControllerSupervisor(controllerHooks{
				spawn: func() (controllerProcess, error) {
					if err := startOwned(cmd); err != nil {
						return nil, err
					}
					return controllerChild{cmd}, nil
				},
				clean: func() error { return nil }, now: time.Now, timer: time.NewTimer, logf: func(string, ...any) {},
			})
			if err := s.start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = s.stop() })
			if kill {
				if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				deadline := time.NewTimer(2 * time.Second)
				defer deadline.Stop()
				for {
					state, err := s.state()
					if err != nil {
						t.Fatal(err)
					}
					if state == "activating" {
						break
					}
					select {
					case <-deadline.C:
						t.Fatal("child not reaped")
					case <-time.After(time.Millisecond):
					}
				}
			}
			if err := s.stop(); err != nil {
				t.Fatal(err)
			}
			if _, owned := ownedChildren.Load(cmd.Process.Pid); owned {
				t.Fatal("child remained registered after Wait")
			}
		})
	}
}

func TestControllerTERMChild(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "controller-term-child" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestControllerRealTERMThroughForcedCleanup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerTERMChild$", "--", "controller-term-child")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanCalls := 0
	s := newControllerSupervisor(controllerHooks{
		spawn: func() (controllerProcess, error) {
			if err := startOwned(cmd); err != nil {
				return nil, err
			}
			return controllerChild{cmd}, nil
		},
		// Test seam only: kill this owned child in lieu of a real VM cgroup.
		clean: func() error { cleanCalls++; return cmd.Process.Kill() },
		now:   time.Now,
		timer: func(d time.Duration) *time.Timer {
			if d == 10*time.Second {
				d = 20 * time.Millisecond
			}
			if d == 20*time.Second {
				d = 2 * time.Second
			}
			return time.NewTimer(d)
		}, logf: func(string, ...any) {},
	})
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = s.stop() })
	ready := make(chan bool, 1)
	go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line == "ready\n" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child not ready")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child readiness timeout")
	}
	if err := s.stop(); err != nil {
		t.Fatal(err)
	}
	if cleanCalls != 1 {
		t.Fatal("TERM-ignoring child did not reach forced cleanup", cleanCalls)
	}
}
