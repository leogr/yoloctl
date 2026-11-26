//go:build linux && amd64

package firecracker

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

func TestAPISocketWaitFollowsHelperLifetime(t *testing.T) {
	for _, scenario := range []string{"late socket", "child exit", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "api.sock")
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
				defer cancel()
				wait := make(chan error, 1)
				childErr := errors.New("child stopped")
				switch scenario {
				case "late socket":
					go func() {
						time.Sleep(6 * time.Second)
						if err := os.WriteFile(path, nil, 0600); err != nil {
							t.Error(err)
						}
					}()
				case "child exit":
					wait <- childErr
				}
				exited, err := waitForAPISocket(ctx, path, wait)
				switch scenario {
				case "late socket":
					if exited || err != nil || len(wait) != 0 {
						t.Fatal(exited, err)
					}
				case "child exit":
					if !exited || !errors.Is(err, childErr) || len(wait) != 1 || <-wait != childErr {
						t.Fatal("child status was lost", exited, err)
					}
				case "cancel":
					if exited || !errors.Is(err, context.DeadlineExceeded) || len(wait) != 0 {
						t.Fatal(exited, err)
					}
				}
			})
		})
	}
}

func TestHelperReleasesBothLocksAfterBootstrapDeadline(t *testing.T) {
	const name = "timeout-control"
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	imageDir := fixtureImage(t, t.TempDir())
	image, files, err := VerifyImage(imageDir)
	if err != nil {
		t.Fatal(err)
	}
	driver := Driver{}
	dir, err := driver.InstanceDir(name)
	if err != nil {
		t.Fatal(err)
	}
	diskID := strings.Repeat("1", 32)
	dataDir, err := driver.AccountDiskDir(diskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, dataDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteConfig(dir, Config{
		Schema: 1, Name: name, UID: 1000, GID: 1000,
		Spec: vmapi.Spec{Image: imageDir, DiskID: diskID}, Image: image, ImageFiles: files,
	}); err != nil {
		t.Fatal(err)
	}
	locks := []string{filepath.Join(dir, "lock"), filepath.Join(dataDir, "lock")}
	for _, phase := range []string{"socket", "request"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				err := withHelperInstance(name, func(dir, dataDir string, _ Config) error {
					for _, path := range locks {
						lock, err := lockFile(path)
						if err == nil {
							_ = lock.Close()
							t.Errorf("boot entered without owning %s", path)
						} else if !errors.Is(err, unix.EWOULDBLOCK) {
							t.Error(err)
						}
					}
					if phase == "socket" {
						_, err := waitForAPISocket(t.Context(), filepath.Join(dir, "api.sock"), make(chan error, 1))
						return err
					}
					transport := apiTransportFunc(func(req *http.Request) (*http.Response, error) {
						<-req.Context().Done()
						return nil, req.Context().Err()
					})
					return apiRequest(t.Context(), transport, "PUT", "/actions", nil)
				})
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 2*time.Minute {
					t.Fatalf("bootstrap returned %v after %v", err, time.Since(start))
				}
				for _, path := range locks {
					lock, err := lockFile(path)
					if err != nil {
						t.Errorf("helper still holds %s: %v", path, err)
					} else {
						_ = lock.Close()
					}
				}
			})
		})
	}
}

func TestBootHelperPostKillWait(t *testing.T) {
	for _, reaped := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			done := make(chan error, 1)
			start := time.Now()
			term, kill := 0, 0
			err := retireBootHelper(done, func(signal os.Signal) error {
				if signal != unix.SIGTERM || time.Since(start) != 0 {
					t.Errorf("initial signal %v at %v", signal, time.Since(start))
				}
				term++
				return nil
			}, func() error {
				if time.Since(start) != 5*time.Second {
					t.Error("TERM grace changed", time.Since(start))
				}
				kill++
				if reaped {
					go func() {
						time.Sleep(7 * time.Second)
						done <- nil
					}()
				}
				return nil
			})
			if term != 1 || kill != 1 {
				t.Fatalf("TERM/KILL %d/%d", term, kill)
			}
			if reaped && (err != nil || time.Since(start) != 12*time.Second) {
				t.Fatalf("late retirement: %v after %v", err, time.Since(start))
			}
			if !reaped && (err == nil || !strings.Contains(err.Error(), "termination could not be established") || time.Since(start) != 125*time.Second) {
				t.Fatalf("unverified retirement: %v after %v", err, time.Since(start))
			}
		})
	}
}

func TestBootForcedStopGetsSeparateWait(t *testing.T) {
	for _, reaped := range []bool{true, false} {
		dir := t.TempDir()
		synctest.Test(t, func(t *testing.T) {
			// A fictional already-reaped helper retains its published record.
			owner := process{PID: 1 << 30, Start: "1"}
			state := runtimeState{Helper: owner, Dir: filepath.Join(dir, "yfc-fixture")}
			if err := writeState(dir, state); err != nil {
				t.Fatal(err)
			}
			caller, cancel := context.WithCancel(t.Context())
			cancel()
			stopCalls := 0
			err := settleBootFailure(context.WithoutCancel(caller), dir, owner, true, func() error {
				time.Sleep(12 * time.Second)
				return nil
			}, func(cleanup context.Context) error {
				stopCalls++
				start := time.Now()
				return waitForStop(cleanup, func() (bool, error) {
					return !reaped || time.Since(start) < 7*time.Second, nil
				}, func() error { return nil }, nil)
			})
			if stopCalls != 1 || reaped && err != nil || !reaped && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("late=%t, calls=%d, err=%v", reaped, stopCalls, err)
			}
			if _, err := readState(dir); err != nil {
				t.Fatal("settlement erased ownership record", err)
			}
		})
	}
}

func TestStopObservationUsesCaller(t *testing.T) {
	for _, scenario := range []string{"unlimited", "long", "short", "interrupt"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				switch scenario {
				case "long", "short":
					budget := time.Minute
					if scenario == "short" {
						budget = time.Second
					}
					deadline, end := context.WithTimeout(ctx, budget)
					defer end()
					ctx = deadline
				case "interrupt":
					go func() { time.Sleep(time.Second); cancel() }()
				}
				start := time.Now()
				shutdownErr := errors.New("SSH reply lost")
				err := waitForStop(ctx, func() (bool, error) {
					return time.Since(start) < 31*time.Second, nil
				}, func() error { return nil }, shutdownErr)
				switch scenario {
				case "short":
					if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, shutdownErr) {
						t.Fatal(err)
					}
				case "interrupt":
					if !errors.Is(err, context.Canceled) || !errors.Is(err, shutdownErr) {
						t.Fatal(err)
					}
				default:
					if err != nil || time.Since(start) < 31*time.Second {
						t.Fatal(err, time.Since(start))
					}
				}
			})
		})
	}
}

func TestDriverStopCanceledBeforeAdmission(t *testing.T) {
	driver := Driver{}
	const name = "timeout-control"
	_, child := vmStopFixture(t, driver, name)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, force := range []bool{false, true} {
		if err := driver.Stop(ctx, name, force); !errors.Is(err, context.Canceled) {
			t.Fatalf("force=%t returned %v", force, err)
		}
		if alive, err := child.alive(); !alive || err != nil {
			t.Fatalf("canceled stop changed the child: %t, %v", alive, err)
		}
	}
}

func TestDriverStopWaitsAfterLostPoweroffReply(t *testing.T) {
	const name = "timeout-control"
	calls := 0
	driver := Driver{poweroffGuest: func(ctx context.Context, got string) ([]byte, error) {
		calls++
		if got != name || ctx.Err() != nil {
			t.Errorf("poweroff named %q, context %v", got, ctx.Err())
		}
		return nil, errors.New("fixture SSH reply lost")
	}}
	cmd, child := vmStopFixture(t, driver, name)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		retired := make(chan struct{})
		defer func() {
			cancel()
			<-retired
		}()
		go func() {
			defer close(retired)
			select {
			case <-ctx.Done():
				return
			case <-time.After(35 * time.Second):
			}
			if alive, err := child.alive(); !alive || err != nil {
				t.Errorf("fixture retired before 35 seconds: %t, %v", alive, err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Error(err)
			}
			_ = cmd.Wait()
		}()
		start := time.Now()
		if err := driver.Stop(ctx, name, false); err != nil || time.Since(start) < 35*time.Second {
			t.Fatalf("stop returned %v after %v", err, time.Since(start))
		}
		if calls != 1 {
			t.Fatalf("sent %d poweroff requests after losing the reply", calls)
		}
		if alive, err := child.alive(); alive || err != nil {
			t.Fatalf("stop acknowledged before process retirement: %t, %v", alive, err)
		}
	})
}

func vmStopFixture(t *testing.T, driver Driver, name string) (*exec.Cmd, process) {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	dir, err := driver.InstanceDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	child, err := identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	retired := process{PID: 1 << 30, Start: "1"}
	if err := writeState(dir, runtimeState{Helper: retired, VM: child, Dir: filepath.Join(dir, "yfc-fixture")}); err != nil {
		t.Fatal(err)
	}
	return cmd, child
}
