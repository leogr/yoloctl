//go:build linux

package vmguest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestCgroupCleanupWait(t *testing.T) {
	for _, scenario := range []string{"late", "expiry", "parent", "read error"} {
		t.Run(scenario, func(t *testing.T) {
			group := t.TempDir()
			events := filepath.Join(group, "cgroup.events")
			if scenario != "read error" {
				if err := os.WriteFile(events, []byte("populated 1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ctx := t.Context()
				want := 2 * time.Minute
				switch scenario {
				case "late":
					want = 4 * time.Second
					go func() {
						time.Sleep(want)
						next := events + ".new"
						if err := os.WriteFile(next, []byte("populated 0\n"), 0600); err != nil {
							t.Error(err)
						} else if err := os.Rename(next, events); err != nil {
							t.Error(err)
						}
					}()
				case "parent":
					want = time.Second
					child, cancel := context.WithTimeout(ctx, want)
					defer cancel()
					ctx = child
				case "read error":
					want = 0
				}
				err := cleanGroupContext(ctx, group)
				switch scenario {
				case "late":
					if err != nil || time.Since(start) < want {
						t.Fatalf("late empty group: %v after %v", err, time.Since(start))
					}
				case "read error":
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				default:
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != want {
						t.Fatalf("unverified group: %v after %v", err, time.Since(start))
					}
				}
				kill, err := os.ReadFile(filepath.Join(group, "cgroup.kill"))
				if err != nil || string(kill) != "1" {
					t.Fatalf("kill file %q: %v", kill, err)
				}
			})
		})
	}
}

func TestCanceledPrepareGroupCannotCreateReplacement(t *testing.T) {
	group := filepath.Join(t.TempDir(), "replacement")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prepareGroup(ctx, group); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(group); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled preparation created a group", err)
	}
}

func TestManagedUnitWaitsForExactStopAcknowledgment(t *testing.T) {
	const name = "yoloctl-session-pending.service"
	for _, joined := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			u := &managedUnit{spec: vmwire.Unit{Name: name}, stop: make(chan struct{}), done: make(chan struct{})}
			m := unitRegistry{
				units: map[string]*managedUnit{name: u},
				names: make(map[string]*unitName),
				clean: func(string) error { return nil },
			}
			if joined {
				go func() {
					<-u.stop
					time.Sleep(14 * time.Second)
					close(u.done)
				}()
			}
			start := time.Now()
			err := m.stop(name)
			if joined && (err != nil || m.lookup(name) != nil || time.Since(start) != 14*time.Second) {
				t.Fatalf("late stop: %v, unit %p after %v", err, m.lookup(name), time.Since(start))
			}
			if !joined && (err == nil || m.lookup(name) != u || time.Since(start) != 130*time.Second) {
				t.Fatalf("unverified stop: %v, unit %p after %v", err, m.lookup(name), time.Since(start))
			}
		})
	}
}
