package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sys/unix"
)

func TestRuntimeMainExitMarkerObservation(t *testing.T) {
	for _, row := range []struct {
		name, text string
		valid      bool
		code       int
	}{
		{"zero", "exit_code=0\n", true, 0},
		{"nonzero", "exit_code=7\n", true, 7},
		{"signal", "exit_code=137\n", true, 137},
		{"max", "exit_code=255\n", true, 255},
		{"negative", "exit_code=-1\n", false, 1},
		{"too-large", "exit_code=256\n", false, 1},
		{"no-newline", "exit_code=7", false, 1},
		{"extra-newline", "exit_code=7\n\n", false, 1},
		{"leading-zero", "exit_code=07\n", false, 1},
		{"plus", "exit_code=+7\n", false, 1},
		{"empty", "", false, 1},
		{"truncated", "exit_code=", false, 1},
		{"unbounded", strings.Repeat("x", 1000), false, 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, runtimeMainExitName), []byte(row.text), 0o600); err != nil {
				t.Fatal(err)
			}
			fd, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fd.Close() }()
			exit, err := readRuntimeMainExitAt(fd, os.Geteuid(), os.Getegid())
			if err != nil || exit == nil || (exit.code != nil) != row.valid || exit.exitCode() != row.code {
				t.Fatalf("exit=%+v error=%v", exit, err)
			}
		})
	}
}

func TestRuntimeMainExitMarkerTypesOwnersAbsenceAndUnavailable(t *testing.T) {
	for _, kind := range []string{"absent", "file", "link", "directory", "fifo", "owner", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, runtimeMainExitName)
			uid, gid := os.Geteuid(), os.Getegid()
			var err error
			switch kind {
			case "file", "owner":
				err = os.WriteFile(path, []byte("exit_code=0\n"), 0o600)
			case "link":
				err = os.Symlink("missing-target", path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "fifo":
				err = unix.Mkfifo(path, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			fd, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fd.Close() }()
			if kind == "unavailable" {
				_ = fd.Close()
			}
			if kind == "owner" {
				uid++
			}
			exit, err := readRuntimeMainExitAt(fd, uid, gid)
			if kind == "unavailable" {
				if exit != nil || err == nil {
					t.Fatal("unavailable descriptor invented a main exit", exit, err)
				}
				return
			}
			if err != nil || (exit == nil) != (kind == "absent") {
				t.Fatal(exit, err)
			}
			if exit != nil && (exit.code != nil) != (kind == "file") {
				t.Fatal("invalid object supplied a code", exit)
			}
		})
	}
}

func TestRuntimeMainExitStaleRemovalPreservesForeignObjects(t *testing.T) {
	dir := t.TempDir()
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	uid, gid := os.Geteuid(), os.Getegid()
	if err := clearRuntimeMainExitAt(fd, uid, gid); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, runtimeMainExitName)
	if err := os.WriteFile(path, []byte("old partial marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearRuntimeMainExitAt(fd, uid+1, gid); err == nil {
		t.Fatal("foreign owner's marker removed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("foreign marker lost", err)
	}
	if err := clearRuntimeMainExitAt(fd, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale marker survived", err)
	}
	if err := os.Symlink("untouched", path); err != nil {
		t.Fatal(err)
	}
	if err := clearRuntimeMainExitAt(fd, uid, gid); err == nil {
		t.Fatal("stale symlink accepted")
	}
}

func TestRuntimeMainExitPublishesBeforeNormalCleanupAndRetainsCode(t *testing.T) {
	f := newMaterialFixture(t)
	children := []*runtimeProcess{{role: runtimeSupervisor, originalPID: 7}, {role: runtimeBoundary, originalPID: 8}}
	for _, known := range []bool{false, true} {
		for _, code := range []int{0, 7, 255} {
			exit := &runtimeMainExit{}
			if known {
				exit.code = &code
			}
			var admission atomic.Bool
			admission.Store(true)
			writeFailure := errors.New("status store unavailable")
			result := stopForRuntimeMainExit(f.record, children, exit, &admission, func(record RuntimeRecord, status RuntimeStatus) error {
				if admission.Load() || status.Reason != "main-exited" || status.Phase != "stopping" || status.MainExit != exit.code {
					t.Fatalf("admission/status ordering: %+v", status)
				}
				return errors.Join(status.validate(record), writeFailure)
			})
			if !errors.Is(result, writeFailure) || projectedRuntimeResult(result) != exit.exitCode() {
				t.Fatal("publication failure lost the recorded main outcome", result)
			}
			for _, reaped := range []bool{false, true} {
				children[0].exited, children[1].exited = reaped, reaped
				phase, reason := "stopping", "cleanup-unproved"
				if reaped {
					phase, reason = "stopped", "stopped"
				}
				status := observeRuntimeEnd(f.record, phase, reason, children, errors.Join(result, errors.New("cleanup diagnostic")), time.Now())
				if status.Phase != phase || status.Reason != "main-exited" || status.MainExit != exit.code || status.validate(f.record) != nil {
					t.Fatalf("cleanup overwrote main outcome: %+v", status)
				}
			}
		}
	}
	if projectedRuntimeResult(nil) != 0 || projectedRuntimeResult(errors.New("infrastructure failure")) != 1 {
		t.Fatal("changed ordinary helper results")
	}
}

func TestRuntimeMainExitStatusDecodingBindsTheExactRun(t *testing.T) {
	f := newMaterialFixture(t)
	for _, code := range []int{-1, 0, 7, 255, 256} {
		status := observeRuntimeEnd(f.record, "stopped", "stopped", nil, &runtimeMainExit{&code}, time.Now())
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		_, err = DecodeRuntimeStatus(raw, f.record)
		if (err == nil) != (code >= 0 && code <= 255) {
			t.Fatal(code, err)
		}
		other := f.record
		other.Generation = "g" + strings.Repeat("1", 48)
		if _, err := DecodeRuntimeStatus(raw, other); err == nil {
			t.Fatal("foreign generation supplied a main exit")
		}
		status.Phase, status.Reason = "ready", "confirmed"
		if status.validate(f.record) == nil {
			t.Fatal("main exit accepted outside end phase")
		}
	}
	legacy := observeRuntimeStatus(f.record, "stopped", "stopped", nil, time.Now())
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "main_exit") {
		t.Fatal("legacy record gained a required field")
	}
	if _, err := DecodeRuntimeStatus(raw, f.record); err != nil {
		t.Fatal("old status rejected", err)
	}
	for _, bad := range []string{`"main_exit":1,"main_exit":2,`, `"unknown":1,`} {
		if _, err := DecodeRuntimeStatus([]byte("{"+bad+string(raw[1:])), f.record); err == nil {
			t.Fatalf("invalid keys accepted: %s", bad)
		}
	}
}

func TestMonitorRuntimeUsesMainExitBeforeReadinessAndKeepsAbsentBehavior(t *testing.T) {
	f := newMaterialFixture(t)
	dir := t.TempDir()
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	shared := runtimeShared{supervisorRun: fd}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if ordered, err := monitorRuntime(ctx, f.record, shared, nil, readRuntimeMainExitAt); ordered || err != nil {
		t.Fatal("absent marker changed startup cancellation", ordered, err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, runtimeMainExitName)); err != nil {
		t.Fatal(err)
	}
	var outcome *runtimeMainExit
	if ordered, err := monitorRuntime(ctx, f.record, shared, nil, readRuntimeMainExitAt); ordered || !errors.As(err, &outcome) || outcome.code != nil {
		t.Fatal("stop superseded the main-exit decision", ordered, err)
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := time.Now()
		code := 7
		mainExit := &runtimeMainExit{code: &code}
		readMainExit := func(*os.File, int, int) (*runtimeMainExit, error) {
			cancel()
			return mainExit, nil
		}
		ordered, err := monitorRuntime(ctx, f.record, shared, nil, readMainExit)
		if ordered || !errors.Is(err, mainExit) || time.Since(started) != 0 {
			t.Fatalf("stop during main-exit drain: ordered=%v result=%v elapsed=%v", ordered, err, time.Since(started))
		}
	})
}

func TestRuntimeMainExitDiagnosticKeepsCleanupErrorsOnly(t *testing.T) {
	code := 0
	exit := &runtimeMainExit{&code}
	if runtimeMainExitDiagnostics(errors.Join(exit, nil)) != nil {
		t.Fatal("ordinary exit logged as a failure")
	}
	failure := errors.New("relay drain failed")
	if !errors.Is(runtimeMainExitDiagnostics(errors.Join(errors.Join(exit, failure), nil)), failure) {
		t.Fatal("cleanup diagnostic lost")
	}
}

func TestRuntimeMainExitCleanupCandidateNamesAreProducerSpecific(t *testing.T) {
	prefix := "generations/g" + strings.Repeat("a", 48) + "/status/runtime-supervisor/"
	for _, name := range []string{"main-exit", ".main-exit.tmp-1", ".main-exit.tmp-12345"} {
		rule, ok := runtimeReservationMember(prefix + name)
		if !ok || rule.kind != unix.S_IFREG || rule.permissions != 0o600 || rule.mask != 0o7777 {
			t.Fatal("producer member not covered", name, rule)
		}
	}
	for _, name := range []string{"main-exit.other", ".main-exit.tmp-", ".main-exit.tmp-0", ".main-exit.tmp-07", ".main-exit.tmp-+7", ".main-exit.tmp-7/other"} {
		if _, ok := runtimeReservationMember(prefix + name); ok {
			t.Fatal("generic name admitted", name)
		}
	}
}

func TestRuntimeMainExitSocketIsInvalidInsteadOfUnavailable(t *testing.T) {
	fd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	name := fmt.Sprintf("/proc/self/fd/%d/%s", fd.Fd(), runtimeMainExitName)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	exit, err := readRuntimeMainExitAt(fd, os.Geteuid(), os.Getegid())
	if err != nil || exit == nil || exit.code != nil {
		t.Fatal("socket observation not classified invalid", exit, err)
	}
}
