package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Ordinary filesystem mapping fixture ONLY. It does not qualify protected
// ancestry, install NativeCtx, create a LaunchAnchor or grant VM admission.
func controllerLeaseMappingFixture(t *testing.T, path string) *controllerLeasePath {
	t.Helper()
	p := &controllerLeasePath{}
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := os.NewFile(uintptr(rootFD), "fixture-root-metadata")
	id, err := controllerLeaseID(int(root.Fd()), "")
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	p.nodes = append(p.nodes, controllerLeasePathNode{file: root, id: id})
	t.Cleanup(func() { _ = p.Close() })
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if name == "" {
			continue
		}
		fd, err := unix.Openat(int(p.leaf().Fd()), name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(fd), "fixture-original-mapping")
		p.nodes = append(p.nodes, controllerLeasePathNode{file: f, name: name})
		id, err := controllerLeaseID(fd, "")
		if err != nil {
			t.Fatal(err)
		}
		p.nodes[len(p.nodes)-1].id = id
	}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestControllerActivationFirstReadRetainsOriginalObjectAndReplacementRefuses(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(strings.Repeat("a", 64) + "\n")
	path := filepath.Join(dir, "activated")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	observation, err := readOriginalControllerActivation(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := observation.(*controllerActivationPin)
	if !ok {
		t.Fatal("first actual read not retained")
	}
	defer func() { _ = p.Close() }()
	if !bytes.Equal(p.raw, raw) {
		t.Fatal("first actual activation bytes changed")
	}
	// Inspect mapping mechanics without upgrading any original proofErr. These
	// ordinary filesystem handles are never a native source or effect permission.
	if p.ancestry != nil {
		_ = p.ancestry.Close()
	}
	p.ancestry = controllerLeaseMappingFixture(t, dir)
	if err := p.checkMappingsLocked(); err != nil {
		t.Fatal(err)
	}
	if p.proofErr != nil && p.checkLocked() == nil {
		t.Fatal("mapping fixture erased protection refusal")
	}
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.checkMappingsLocked(); err == nil {
		t.Fatal("same-byte named replacement adopted")
	}
	got, err := controllerLeaseRead(p.file)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("original file lost after replacement", err)
	}
}
func TestControllerActivationUnsupportedQualificationPreservesOrdinaryRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "activated"), []byte("old installed activation bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	observation, err := readOriginalControllerActivation(dir)
	if err != nil {
		t.Fatal("optional qualification added ordinary startup refusal", err)
	}
	p := observation.(*controllerActivationPin)
	if p.proofErr == nil || p.checkLocked() == nil {
		t.Fatal("unqualified bytes became source proof")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("first observer not closed", err)
	}
}
func countControllerRootObserverFDs(t *testing.T, root controllerLeaseFileID) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("project process FD observation unavailable:", err)
	}
	count := 0
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		var st unix.Stat_t
		if unix.Fstat(fd, &st) == nil && uint64(st.Dev) == root.Device && st.Ino == root.Inode && st.Mode&unix.S_IFMT == unix.S_IFDIR {
			count++
		}
	}
	return count
}
func TestControllerSourceLateAncestorFailureClosesEveryOwnedObserver(t *testing.T) {
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := os.NewFile(uintptr(rootFD), "fixture-root-metadata")
	defer func() { _ = root.Close() }()
	id, err := controllerLeaseID(int(root.Fd()), "")
	if err != nil {
		t.Fatal(err)
	}
	before := countControllerRootObserverFDs(t, id)
	missing := fmt.Sprintf("/.yoloctl-owned-test-absent-%d-%d", os.Getpid(), time.Now().UnixNano())
	for i := 0; i < 3; i++ {
		p, err := captureControllerLeasePath(missing)
		if err == nil || p != nil {
			if p != nil {
				_ = p.Close()
			}
			t.Fatal("late missing ancestor accepted", err)
		}
	}
	if after := countControllerRootObserverFDs(t, id); after != before {
		t.Fatal("named-return cleanup leaked original root observers", before, after)
	}
}
func TestControllerSourceLeaseCloseNeverClosesOrUnlocksOriginalSingleton(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	mappings := controllerLeaseMappingFixture(t, dir)
	id, err := controllerLeaseID(int(f.Fd()), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkControllerSingletonMappings(int(f.Fd()), id, mappings); err != nil {
		t.Fatal(err)
	}
	l := &controllerWorkerSourceLease{runPath: mappings, singletonFD: int(f.Fd())} // OWN observer cleanup ONLY, not a source factory
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(); err != nil {
		t.Fatal("lease closed borrowed singleton", err)
	}
	another, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = another.Close() }()
	if err := unix.Flock(int(another.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal("lease released original singleton ownership", err)
	}
	if _, err := l.DuplicatePIDNamespace(); err == nil {
		t.Fatal("closed partial source minted namespace")
	}
}
func TestControllerSourceOriginalSingletonMappingRefusesSameByteReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	mapping := controllerLeaseMappingFixture(t, dir)
	id, err := controllerLeaseID(int(f.Fd()), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkControllerSingletonMappings(int(f.Fd()), id, mapping); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkControllerSingletonMappings(int(f.Fd()), id, mapping); err == nil {
		t.Fatal("matching lock bytes replaced original singleton")
	}
}
func TestControllerSourceLeaseMissingContextCannotMintBorrower(t *testing.T) {
	source, err := captureControllerServiceSource(context.Background(), nil, -1, nil)
	if source != nil || err == nil {
		t.Fatal("missing original context minted source")
	}
}
