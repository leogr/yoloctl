package openshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
)

// Existing injected ordinary-directory
// census fixtures confer no procfs, VM, OFD or consumer-completeness evidence.
func nativeFDUniverseFixture(t *testing.T) (*nativeUniverseFixture, *nativeProcessUniverse) {
	t.Helper()
	f := newNativeUniverseFixture(t)
	f.write("__target", []byte("ordinary reference fixture"))
	u := f.capture()
	for _, thread := range u.threads {
		directory := filepath.Join(f.root, strconv.Itoa(thread.pid), "task", strconv.Itoa(thread.tid), "fd")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		for _, slot := range []string{"0", "7"} {
			if err := os.Symlink(filepath.Join(f.root, "__target"), filepath.Join(directory, slot)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f, u
}

func TestNativeFDNumbersIncludesZeroAndRejectsAliases(t *testing.T) {
	for _, valid := range []string{"0", "1", "2147483647"} {
		if n, err := nativeFDNumber(valid); err != nil || strconv.Itoa(n) != valid {
			t.Fatal(valid, n, err)
		}
	}
	for _, invalid := range []string{"", "00", "01", "-1", "+1", "1x", "2147483648"} {
		if _, err := nativeFDNumber(invalid); err == nil {
			t.Fatal("ambiguous FD slot", invalid)
		}
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	for pass := 0; pass < 2; pass++ {
		got, err := nativeFDNumbers(context.Background(), directory, 0)
		if err != nil || !reflect.DeepEqual(got, []int{}) {
			t.Fatal("actual empty enumeration failed", got, err)
		}
	}
}

func TestNativeFDObservationRetainsEveryTaskKindAndOriginalHandles(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	f, u := nativeFDUniverseFixture(t)
	observation, err := readNativeProcessFDObservation(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.tables) != 5 || len(observation.references) != 10 {
		t.Fatal("root/self/zombie/kernel task or FD omitted")
	}
	originalTables := append([]nativeProcessFDTable(nil), observation.tables...)
	for _, reference := range observation.references {
		if reference.fd != 0 && reference.fd != 7 {
			t.Fatal("invented slot")
		}
		if reference.start == "" || reference.inode == 0 || reference.mount == 0 {
			t.Fatal("lost primary identity")
		}
	}
	for pass := 0; pass < 2; pass++ {
		if err := observation.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for pass := 0; pass < 2; pass++ {
		if err := observation.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range originalTables {
		if _, err := table.directory.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("collector handle not closed once", err)
		}
	}
	if err := u.Check(t.Context()); err != nil {
		t.Fatal("collector closed borrowed universe", err)
	}
	if err := observation.Check(t.Context()); err == nil {
		t.Fatal("expired observation repaired")
	}
	if _, err := f.borrowed.Stat(); err != nil {
		t.Fatal("borrowed proc closed", err)
	}
}

func TestNativeFDObservationExtraSameProcessReferenceAndStickyFailure(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	f, u := nativeFDUniverseFixture(t)
	observation, err := readNativeProcessFDObservation(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observation.Close() }()
	slot := filepath.Join(f.root, "7/task/8/fd/13")
	if err := os.Symlink(filepath.Join(f.root, "__target"), slot); err != nil {
		t.Fatal(err)
	}
	if err := observation.Check(t.Context()); err == nil {
		t.Fatal("extra same-inode/self FD was hidden")
	}
	// No deletion or repair attempt: the original observation must remain failed.
	if err := observation.Check(t.Context()); err == nil {
		t.Fatal("failed observation regained validity")
	}
}

func TestNativeFDObservationMissingTaskFDDirectoryIsNotEmpty(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	f := newNativeUniverseFixture(t)
	u := f.capture()
	if observation, err := readNativeProcessFDObservation(t.Context(), u); err == nil || observation != nil {
		t.Fatal("unavailable FD directory became empty observation")
	}
	if err := u.Check(t.Context()); err != nil {
		t.Fatal("partial collector closed borrowed universe", err)
	}
}

func TestNativeFDObservationPartialFailureClosesOnlyCollectorHandles(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	f, u := nativeFDUniverseFixture(t)
	last := filepath.Join(f.root, "11/task/11/fd")
	if err := os.Rename(last, last+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	before := len(f.observed)
	// The existing kernel-fact seam sees each real directory acquisition;
	// retain the exact original pointer when its fixture inode is an fd dir.
	owned := map[*os.File]bool{}
	originalIdentity := u.ops.identity
	u.ops.identity = func(file *os.File, directory bool) (pinFileIdentity, error) {
		id, err := originalIdentity(file, directory)
		if err == nil && directory {
			for _, thread := range u.threads {
				name := filepath.Join(f.root, strconv.Itoa(thread.pid), "task", strconv.Itoa(thread.tid), "fd")
				info, statErr := os.Stat(name)
				actual, fileErr := file.Stat()
				if statErr == nil && fileErr == nil && os.SameFile(info, actual) {
					owned[file] = true
				}
			}
		}
		return id, err
	}
	if observation, err := readNativeProcessFDObservation(t.Context(), u); err == nil || observation != nil {
		t.Fatal("partial acquisition became complete", err)
	}
	closed := 0
	for _, file := range f.observed[before:] {
		if owned[file] {
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("partial owned enumeration handle leaked", err)
			}
			closed++
		}
	}
	if closed == 0 {
		t.Fatal("fixture did not reach partial acquisition")
	}
	if err := u.Check(t.Context()); err != nil {
		t.Fatal("borrowed universe closed by partial failure", err)
	}
}

func TestNativeFDObservationReboundTargetAndClosedUniverseRefuse(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	f, u := nativeFDUniverseFixture(t)
	observation, err := readNativeProcessFDObservation(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observation.Close() }()
	f.write("__replacement", []byte("different original object"))
	replacement := filepath.Join(f.root, "__new-link")
	if err := os.Symlink(filepath.Join(f.root, "__replacement"), replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(f.root, "7/task/8/fd/7")); err != nil {
		t.Fatal(err)
	}
	if err := observation.Check(t.Context()); err == nil {
		t.Fatal("rebound target accepted")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observation.Check(t.Context()); err == nil {
		t.Fatal("closed universe accepted")
	}
}
