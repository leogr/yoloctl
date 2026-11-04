package openshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A normal temporary directory with explicitly injected kernel facts. No
// fixture opens /proc, creates a namespace or proves native VM completeness.
type nativeUniverseFixture struct {
	t                       *testing.T
	root                    string
	borrowed                *os.File
	ops                     nativeProcessUniverseOps
	observed, artificial    []*os.File
	readerPID, readerTID    int
	namespaceID             namespaceIdentity
	boot                    string
	pidfdCalls, failPIDFDAt int
}

func newNativeUniverseFixture(t *testing.T) *nativeUniverseFixture {
	t.Helper()
	f := &nativeUniverseFixture{t: t, root: t.TempDir(), readerPID: 7, readerTID: 8, namespaceID: namespaceIdentity{Device: 1, Inode: 90}, boot: "fixture-boot"}
	f.addGroup(1, []int{1}, "0", "S", false)
	f.addGroup(7, []int{7, 8, 9}, "70", "S", false)
	f.addGroup(11, []int{11}, "0", "Z", true)
	f.writeTask(7, 9, "79", "S", false, true)
	if err := os.Symlink("7/task/8", filepath.Join(f.root, "thread-self")); err != nil {
		t.Fatal(err)
	}
	f.write("__namespace", []byte("fixture namespace descriptor"))
	f.write("__pidfd", []byte("fixture pidfd descriptor"))
	var err error
	f.borrowed, err = os.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.borrowed.Close() })
	f.ops = nativeProcessUniverseOps{
		identity: func(file *os.File, directory bool) (pinFileIdentity, error) {
			f.observed = append(f.observed, file)
			if file == nil {
				return pinFileIdentity{}, errors.New("fixture closed descriptor")
			}
			var st unix.Stat_t
			if err := unix.Fstat(int(file.Fd()), &st); err != nil {
				return pinFileIdentity{}, err
			}
			want := uint32(unix.S_IFREG)
			if directory {
				want = unix.S_IFDIR
			}
			if st.Mode&unix.S_IFMT != want {
				return pinFileIdentity{}, errors.New("fixture type mismatch")
			}
			return pinFileIdentity{device: uint64(st.Dev), inode: st.Ino, mount: 3}, nil
		},
		namespace: func(_ *os.File) (*os.File, namespaceIdentity, error) {
			file, err := os.Open(filepath.Join(f.root, "__namespace"))
			if file != nil {
				f.artificial = append(f.artificial, file)
			}
			return file, f.namespaceID, err
		},
		namespaceID: func(file *os.File) (namespaceIdentity, error) {
			if _, err := file.Stat(); err != nil {
				return namespaceIdentity{}, err
			}
			return f.namespaceID, nil
		},
		pidfd: func(_ int) (*os.File, error) {
			f.pidfdCalls++
			if f.failPIDFDAt == f.pidfdCalls {
				return nil, errors.New("fixture pidfd acquisition failure")
			}
			file, err := os.Open(filepath.Join(f.root, "__pidfd"))
			if file != nil {
				f.artificial = append(f.artificial, file)
			}
			return file, err
		},
		checkPIDFD: func(ctx context.Context, _ *os.File, file *os.File, _ int, _ bool) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := file.Stat()
			return err
		},
		readerIDs: func() (int, int) { return f.readerPID, f.readerTID },
		boot:      func(_ *os.File) (string, error) { return f.boot, nil },
	}
	id, err := f.ops.identity(f.borrowed, true)
	if err != nil {
		t.Fatal(err)
	}
	f.write("7/task/8/mountinfo", []byte(fmt.Sprintf("3 2 %d:%d / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw\n", unix.Major(id.device), unix.Minor(id.device))))
	return f
}

func (f *nativeUniverseFixture) write(name string, raw []byte) {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *nativeUniverseFixture) writeTask(pid, tid int, start, state string, kernel, nested bool) {
	f.t.Helper()
	base := fmt.Sprintf("%d/task/%d/", pid, tid)
	f.write(base+"stat", nativeCensusStatFixture(tid, state, start))
	f.write(base+"status", nativeCensusStatusFixture(pid, tid, kernel, nested))
}

func (f *nativeUniverseFixture) addGroup(pid int, tids []int, start, state string, kernel bool) {
	f.t.Helper()
	f.write(strconv.Itoa(pid)+"/stat", nativeCensusStatFixture(pid, state, start))
	f.write(strconv.Itoa(pid)+"/status", nativeCensusStatusFixture(pid, pid, kernel, false))
	for _, tid := range tids {
		threadStart := start
		if tid != pid {
			threadStart = strconv.Itoa(pid*10 + tid)
		}
		f.writeTask(pid, tid, threadStart, state, kernel, false)
	}
}

func (f *nativeUniverseFixture) capture() *nativeProcessUniverse {
	f.t.Helper()
	u, err := readNativeProcessUniverseWithOps(context.Background(), f.borrowed, f.ops)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = u.Close() })
	return u
}

func (f *nativeUniverseFixture) assertOnlyBorrowedOpen() {
	f.t.Helper()
	for _, file := range append(slices.Clone(f.observed), f.artificial...) {
		if file == nil || file == f.borrowed {
			continue
		}
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			f.t.Fatalf("owned descriptor remains usable after cleanup: %s %v", file.Name(), err)
		}
	}
	if _, err := f.borrowed.Stat(); err != nil {
		f.t.Fatal("borrowed root was closed", err)
	}
}

func TestNativeUniverseRetainsAllTaskKindsAndIndependentReadOffsets(t *testing.T) {
	f := newNativeUniverseFixture(t)
	if _, err := f.borrowed.Readdirnames(-1); err != nil {
		t.Fatal(err)
	}
	u := f.capture()
	if len(u.processes) != 3 || len(u.threads) != 5 || f.pidfdCalls != 3 {
		t.Fatalf("incomplete fixture census: %d groups %d threads %d pidfds", len(u.processes), len(u.threads), f.pidfdCalls)
	}
	var tids []int
	for _, task := range u.threads {
		tids = append(tids, task.tid)
		if task.tid == 11 && (!task.zombie || !task.kernelThread || task.start != "0") {
			t.Fatal("explicit zombie/kernel/zero observation was lost", task)
		}
		if task.tid == 9 && (len(task.nspids) != 2 || task.nspids[0] != 9 || task.nspids[1] != 1) {
			t.Fatal("nested nonleader omitted or flattened", task)
		}
	}
	if !slices.Equal(tids, []int{1, 7, 8, 9, 11}) {
		t.Fatal("task membership changed", tids)
	}
	for range 2 {
		if err := u.Check(context.Background()); err != nil {
			t.Fatal("repeat fixture capture reused EOF or lost original identity", err)
		}
	}
	if names, err := f.borrowed.Readdirnames(1); err != io.EOF || len(names) != 0 {
		t.Fatal("reader changed borrowed directory offset", names, err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err != nil {
		t.Fatal("Close is not idempotent", err)
	}
	f.assertOnlyBorrowedOpen()
	if err := u.Check(context.Background()); err == nil {
		t.Fatal("closed universe passed Check")
	}
}

func TestNativeUniverseRefusesOriginalIdentityAndMembershipChurn(t *testing.T) {
	for _, kind := range []string{"pid-reuse", "tid-reuse", "pid-start", "tid-start", "pid-add", "tid-add", "tid-remove", "leader-exit", "status-partial", "status-foreign", "kernel-kind"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeUniverseFixture(t)
			u := f.capture()
			switch kind {
			case "pid-reuse":
				if err := os.Rename(filepath.Join(f.root, "11"), filepath.Join(f.root, "__old11")); err != nil {
					t.Fatal(err)
				}
				f.addGroup(11, []int{11}, "0", "Z", true) // Identical metadata still cannot replace the original.
			case "tid-reuse":
				if err := os.Rename(filepath.Join(f.root, "7/task/9"), filepath.Join(f.root, "__old9")); err != nil {
					t.Fatal(err)
				}
				f.writeTask(7, 9, "79", "S", false, true)
			case "pid-start":
				f.write("11/stat", nativeCensusStatFixture(11, "Z", "1"))
			case "tid-start":
				f.write("7/task/9/stat", nativeCensusStatFixture(9, "S", "80"))
			case "pid-add":
				f.addGroup(13, []int{13}, "13", "S", false)
			case "tid-add":
				f.writeTask(7, 10, "80", "S", false, false)
			case "tid-remove":
				if err := os.Rename(filepath.Join(f.root, "7/task/9"), filepath.Join(f.root, "__removed9")); err != nil {
					t.Fatal(err)
				}
			case "leader-exit":
				if err := os.Rename(filepath.Join(f.root, "11/task"), filepath.Join(f.root, "__leaderExited")); err != nil {
					t.Fatal(err)
				}
			case "status-partial":
				f.write("7/task/9/status", []byte("Tgid:\t7\nPid:\t9\n"))
			case "status-foreign":
				f.write("7/task/9/status", nativeCensusStatusFixture(11, 9, false, false))
			case "kernel-kind":
				f.write("7/task/9/status", nativeCensusStatusFixture(7, 9, true, true))
			}
			if err := u.Check(context.Background()); err == nil {
				t.Fatal("changed or unavailable identity was adopted")
			}
			if err := u.Check(context.Background()); err == nil {
				t.Fatal("failed universe regained completeness")
			}
		})
	}
}

func TestNativeUniverseRetainsObservedNamespaceWithoutInventingVMAdmission(t *testing.T) {
	f := newNativeUniverseFixture(t)
	f.namespaceID = namespaceIdentity{Device: 1, Inode: 12345}
	u := f.capture()
	if u.view.initPIDNamespace != f.namespaceID || u.view.readerPIDNamespace != f.namespaceID {
		t.Fatal("internally consistent nested fixture observation was lost")
	}
	// A separate consumer must compare this retained observation with an
	// independently admitted anchor. Constructor success is not that proof.
	admittedElsewhere := namespaceIdentity{Device: 1, Inode: 90}
	if u.view.readerPIDNamespace == admittedElsewhere {
		t.Fatal("nested observed identity was replaced with VM identity")
	}
	f.namespaceID.Inode++
	if err := u.Check(context.Background()); err == nil {
		t.Fatal("changed original namespace observation accepted")
	}
}

func TestNativeUniverseRefusesViewAndReaderChanges(t *testing.T) {
	for _, kind := range []string{"reader-tid", "reader-pid", "boot", "proc-filter", "reader-start", "pid1-start"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeUniverseFixture(t)
			u := f.capture()
			switch kind {
			case "reader-tid":
				f.readerTID = 9
			case "reader-pid":
				f.readerPID = 11
			case "boot":
				f.boot = "other-boot"
			case "proc-filter":
				raw, err := os.ReadFile(filepath.Join(f.root, "7/task/8/mountinfo"))
				if err != nil {
					t.Fatal(err)
				}
				f.write("7/task/8/mountinfo", []byte(strings.Replace(string(raw), "proc proc rw", "proc proc rw,hidepid=2", 1)))
			case "reader-start":
				f.write("7/task/8/stat", nativeCensusStatFixture(8, "S", "999"))
			case "pid1-start":
				f.write("1/stat", nativeCensusStatFixture(1, "S", "999"))
			}
			if err := u.Check(context.Background()); err == nil {
				t.Fatal("changed proc/reader view accepted")
			}
		})
	}
}

func TestNativeUniverseConstructionFailuresClosePartialOwnership(t *testing.T) {
	for _, kind := range []string{"pidfd", "missing-status", "oversized-stat", "partial-status", "foreign-reader", "namespace-mismatch", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeUniverseFixture(t)
			ctx := context.Background()
			switch kind {
			case "pidfd":
				f.failPIDFDAt = 2
			case "missing-status":
				if err := os.Remove(filepath.Join(f.root, "7/task/9/status")); err != nil {
					t.Fatal(err)
				}
			case "oversized-stat":
				f.write("11/stat", []byte(strings.Repeat("x", nativeProcessMetadataLimit+1)))
			case "partial-status":
				f.write("11/status", []byte("Tgid:\t11\n"))
			case "foreign-reader":
				f.readerPID = 20
			case "namespace-mismatch":
				calls := 0
				original := f.ops.namespace
				f.ops.namespace = func(dir *os.File) (*os.File, namespaceIdentity, error) {
					file, id, err := original(dir)
					calls++
					if calls == 2 {
						id.Inode++
					}
					return file, id, err
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if u, err := readNativeProcessUniverseWithOps(ctx, f.borrowed, f.ops); err == nil || u != nil {
				t.Fatal("incomplete construction returned a universe", u, err)
			}
			f.assertOnlyBorrowedOpen()
		})
	}
}

func TestNativeUniverseConstructionChurnNeverRetriesOrAdopts(t *testing.T) {
	for _, kind := range []string{"process-add", "task-add", "stat-during-status"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeUniverseFixture(t)
			if kind == "stat-during-status" {
				var target unix.Stat_t
				if err := unix.Stat(filepath.Join(f.root, "11/status"), &target); err != nil {
					t.Fatal(err)
				}
				original := f.ops.identity
				changed := false
				f.ops.identity = func(file *os.File, dir bool) (pinFileIdentity, error) {
					id, err := original(file, dir)
					if err == nil && id.inode == target.Ino && !changed {
						changed = true
						f.write("11/stat", nativeCensusStatFixture(11, "Z", "99"))
					}
					return id, err
				}
			} else {
				original := f.ops.pidfd
				f.ops.pidfd = func(pid int) (*os.File, error) {
					if pid == 11 {
						if kind == "process-add" {
							f.addGroup(13, []int{13}, "13", "S", false)
						} else {
							f.writeTask(7, 10, "80", "S", false, false)
						}
					}
					return original(pid)
				}
			}
			if u, err := readNativeProcessUniverseWithOps(context.Background(), f.borrowed, f.ops); err == nil || u != nil {
				t.Fatal("capture adopted a changing roster/identity", u, err)
			}
			if f.pidfdCalls > 3 {
				t.Fatal("capture retried or adopted new original processes", f.pidfdCalls)
			}
			f.assertOnlyBorrowedOpen()
		})
	}
}

func TestNativeUniverseOriginalPIDFDUncertaintyStopsWithoutRepinning(t *testing.T) {
	f := newNativeUniverseFixture(t)
	u := f.capture()
	originalCalls := f.pidfdCalls
	u.ops.checkPIDFD = func(context.Context, *os.File, *os.File, int, bool) error {
		return errors.New("fixture original pidfd lost/exited")
	}
	if err := u.Check(context.Background()); err == nil {
		t.Fatal("unavailable original pidfd ignored")
	}
	if f.pidfdCalls != originalCalls {
		t.Fatal("Check repinned a numeric PID")
	}
}

func TestNativeUniverseLimitsCancellationAndCloseErrorsRefuse(t *testing.T) {
	f := newNativeUniverseFixture(t)
	u := f.capture()
	for _, limit := range []int{0, 2} {
		if _, err := u.members(context.Background(), u.proc, true, limit); err == nil {
			t.Fatal("bounded roster became truncated success", limit)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := u.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	if err := u.Check(context.Background()); err == nil {
		t.Fatal("canceled universe regained completeness")
	}
	if err := u.processes[0].pidfd.Close(); err != nil {
		t.Fatal(err)
	}
	err := u.Close()
	if err == nil || u.Close() != err {
		t.Fatal("Close error was lost or changed on repeat", err)
	}
	f.assertOnlyBorrowedOpen()
	if err := (*nativeProcessUniverse)(nil).Check(context.Background()); err == nil {
		t.Fatal("nil universe passed Check")
	}
	if err := (&nativeProcessUniverse{}).Check(context.Background()); err == nil {
		t.Fatal("incomplete universe passed Check")
	}
}

func TestNativeUniverseProductionEntryRefusesOrdinaryFixture(t *testing.T) {
	f := newNativeUniverseFixture(t)
	if u, err := readNativeProcessUniverse(context.Background(), f.borrowed); err == nil || u != nil {
		t.Fatal("normal directory supplied positive procfs authentication", u, err)
	}
	if _, err := f.borrowed.Stat(); err != nil {
		t.Fatal("production refusal closed borrowed directory", err)
	}
}
