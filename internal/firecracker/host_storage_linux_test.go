//go:build linux && amd64

package firecracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/vmapi"
	"golang.org/x/sys/unix"
)

func TestHostStorageChecksBothBackingFiles(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "system"), filepath.Join(dir, "data")}
	for _, p := range paths {
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := openBackingStorage(paths...)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if _, err := hostAvailable(s.files[0]); err != nil {
		t.Fatal(err)
	}
	available := map[string]uint64{paths[0]: 10 << 30, paths[1]: 3 << 30}
	s.sample = func(f *os.File) (uint64, error) { return available[f.Name()], nil }
	if err := s.require(hostStorageStartFree); err == nil || !strings.Contains(err.Error(), paths[1]) || !strings.Contains(err.Error(), "host filesystem") {
		t.Fatalf("low data disk not identified: %v", err)
	}
	available[paths[1]] = hostStorageStartFree
	if err := s.require(hostStorageStartFree); err != nil {
		t.Fatal(err)
	}
	available[paths[0]] = hostStorageStartFree - 1
	if err := s.require(hostStorageStartFree); err == nil || !strings.Contains(err.Error(), paths[0]) {
		t.Fatalf("low system disk not identified: %v", err)
	}
	s.sample = func(*os.File) (uint64, error) { return 0, unix.EIO }
	if err := s.require(hostStorageStartFree); !errors.Is(err, unix.EIO) {
		t.Fatalf("unknown capacity accepted: %v", err)
	}
	// Replacing the path cannot change what the open monitor descriptor measures.
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(paths[1], paths[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := hostAvailable(s.files[0]); err != nil {
		t.Fatal(err)
	}
	if bad, err := openBackingStorage(paths[0]); err == nil {
		bad.close()
		t.Fatal("followed a backing-file symlink")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if bad, err := openBackingStorage(fifo); err == nil {
		bad.close()
		t.Fatal("accepted a special file")
	}
}

// Creating a VM requires the start headroom plus what formatting its system
// disk allocates, so a format cannot by itself bring the host to the pause
// threshold.
func TestCreateHeadroomCountsTheFormatAllocation(t *testing.T) {
	s, err := openBackingStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	format := vmapi.FormatAllocation("firecracker", 60<<30)
	s.sample = func(*os.File) (uint64, error) { return hostStorageStartFree + format - 1, nil }
	if err := s.require(hostStorageStartFree + format); err == nil || !strings.Contains(err.Error(), "need 5.1 GiB") {
		t.Fatalf("a 60 GiB create just above 4 GiB free was allowed: %v", err)
	}
	s.sample = func(*os.File) (uint64, error) { return hostStorageStartFree + format, nil }
	if err := s.require(hostStorageStartFree + format); err != nil {
		t.Fatal(err)
	}
}

func TestHostStorageGuardHysteresisAndErrors(t *testing.T) {
	available := hostStorageStartFree
	var measureErr, signalErr error
	var signals []unix.Signal
	var log bytes.Buffer
	g := &hostStorageGuard{log: &log, check: func(minimum uint64) error {
		if measureErr != nil {
			return measureErr
		}
		if available < minimum {
			return errors.New("fixture backing filesystem is low")
		}
		return nil
	}, signal: func(sig unix.Signal) error {
		if signalErr != nil {
			return signalErr
		}
		signals = append(signals, sig)
		return nil
	}}
	step := func(want bool) {
		t.Helper()
		if err := g.step(); err != nil || g.snapshot().Paused != want {
			t.Fatalf("state=%+v error=%v", g.snapshot(), err)
		}
	}
	step(false)
	available = hostStoragePauseFree
	step(false)
	available--
	step(true)
	if !errors.Is(g.snapshot().err(), vmapi.ErrHostStoragePaused) {
		t.Fatal("pause lost typed lifecycle error")
	}
	step(true)
	available = hostStorageStartFree - 1
	step(true)
	available++
	signalErr = unix.EPERM
	if err := g.step(); !errors.Is(err, unix.EPERM) || !g.snapshot().Paused {
		t.Fatalf("failed resume reported running: %v", err)
	}
	signalErr = nil
	step(false)
	measureErr = unix.EIO
	step(true)
	measureErr = nil
	step(false)
	if !slices.Equal(signals, []unix.Signal{unix.SIGSTOP, unix.SIGCONT, unix.SIGSTOP, unix.SIGCONT}) {
		t.Fatalf("redundant/wrong transitions: %v", signals)
	}
	available = 0
	signalErr = unix.EPERM
	if err := g.step(); !errors.Is(err, unix.EPERM) || g.snapshot().Paused {
		t.Fatalf("failed stop reported paused: %v", err)
	}
	if !strings.Contains(log.String(), "resumes automatically") || !strings.Contains(log.String(), "not replayed") {
		t.Fatalf("missing recovery guidance: %s", log.String())
	}
}

func TestHostStorageGuardWatchStopsWithHelper(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var n atomic.Int64
		g := &hostStorageGuard{log: io.Discard, check: func(uint64) error { n.Add(1); return nil }}
		done := make(chan struct{})
		go func() { defer close(done); g.watch(ctx) }()
		synctest.Wait()
		if n.Load() != 1 {
			t.Fatal("missing initial measurement")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if n.Load() != 2 {
			t.Fatal("missing periodic measurement")
		}
		cancel()
		<-done
		time.Sleep(2 * time.Second)
		if n.Load() != 2 {
			t.Fatal("monitor outlived helper")
		}
	})
}

func TestHostStorageStatusIsReadOnlyAndBounded(t *testing.T) {
	dir, err := os.MkdirTemp("", "yfc-storage-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// Old helpers have no endpoint. Missing is distinct from corrupt/unreachable.
	if state, err := readHostStorage(t.Context(), dir); err != nil || state.Paused {
		t.Fatalf("old helper compatibility: %+v %v", state, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	g := &hostStorageGuard{state: hostStorageState{Paused: true, Reason: "fixture low host capacity"}}
	if err := g.serve(ctx, dir); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		state, err := readHostStorage(t.Context(), dir)
		if err != nil || !errors.Is(state.err(), vmapi.ErrHostStoragePaused) {
			t.Fatalf("lost in-memory recovery state: %+v %v", state, err)
		}
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := readHostStorage(canceled, dir); err == nil {
		t.Fatal("ignored caller cancellation")
	}
}

func TestHostStorageRejectsInvalidAndStalledStatus(t *testing.T) {
	for _, payload := range []string{`{}`, `{"paused":null}`, `{"paused":true}`, `{"paused":false,"reason":"bad"}`, `{"paused":false,"unknown":1}`, strings.Repeat("x", 16385), "stalled"} {
		t.Run(fmt.Sprintf("%.40s", payload), func(t *testing.T) {
			dir, err := os.MkdirTemp("", "yfc-storage-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(dir) }()
			ln, err := net.Listen("unix", filepath.Join(dir, hostStorageSocket))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			go func() {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = c.Close() }()
				if payload == "stalled" {
					_, _ = io.Copy(io.Discard, c)
				} else {
					_, _ = io.WriteString(c, payload)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if _, err := readHostStorage(ctx, dir); err == nil {
				t.Fatal("uncertain guard status treated as healthy")
			}
		})
	}
}

// A real child is deliberately stopped; never signal the test runner or a VM.
func TestHostStorageWriterProcess(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_STORAGE_WRITER") != "1" {
		t.Skip("subprocess fixture")
	}
	f, err := os.OpenFile(os.Getenv("YOLOCTL_TEST_STORAGE_PROGRESS"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for {
		if _, err := f.Write([]byte(".")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostStoragePauseRetainsProcessAndBlocksForcedStop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime, err := os.MkdirTemp("", "yfc-storage-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(runtime) }()
	progress := filepath.Join(t.TempDir(), "progress")
	if err := os.WriteFile(progress, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostStorageWriterProcess$")
	cmd.Env = append(os.Environ(), "YOLOCTL_TEST_STORAGE_WRITER=1", "YOLOCTL_TEST_STORAGE_PROGRESS="+progress)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	child, err := identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(check func() bool) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for !check() {
			if time.Now().After(end) {
				t.Fatal("subprocess did not reach expected state")
			}
			time.Sleep(time.Millisecond)
		}
	}
	size := func() int64 { st, _ := os.Stat(progress); return st.Size() }
	waitFor(func() bool { return size() > 0 })
	low := true
	g := &hostStorageGuard{log: io.Discard, check: func(uint64) error {
		if low {
			return errors.New("fixture host filesystem is low")
		}
		return nil
	}, signal: child.signal}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := g.serve(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	d := Driver{}
	dir, _ := d.InstanceDir("storage-test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, Config{Schema: 1, Name: "storage-test", UID: 1000, GID: 1000, Spec: vmapi.Spec{DiskID: strings.Repeat("1", 32), Memory: "2GiB", Disk: "30GiB"}}); err != nil {
		t.Fatal(err)
	}
	// Both identities use the disposable child: even a teardown regression
	// must never send a signal to the test runner.
	if err := writeState(dir, runtimeState{Helper: child, VM: child, Dir: runtime}); err != nil {
		t.Fatal(err)
	}
	if err := g.step(); err != nil {
		// Sandboxes deny pidfd signaling: OpenShell v0.0.72 with EPERM,
		// 0.1.6 with ENOSYS (so launchers fall back). Both mean "skip here".
		if (errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS)) && os.Getenv("YOLOCTL_TEST_REQUIRE_STORAGE_SIGNALS") != "1" {
			t.Skip("sandbox denies pidfd signaling; run with YOLOCTL_TEST_REQUIRE_STORAGE_SIGNALS=1 on the Linux host")
		}
		t.Fatal(err)
	}
	waitFor(func() bool {
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child.PID))
		return strings.Contains(string(b), ") T ")
	})
	before := size()
	// All normal host entry points report the reason without connecting to SSH.
	for name, call := range map[string]func() error{
		"ssh":        func() error { return SSH(t.Context(), "storage-test", "", nil, false) },
		"proxy":      func() error { return Proxy(t.Context(), "storage-test") },
		"stop":       func() error { return d.Stop(t.Context(), "storage-test", false) },
		"force-stop": func() error { return d.Stop(t.Context(), "storage-test", true) },
	} {
		if err := call(); !errors.Is(err, vmapi.ErrHostStoragePaused) {
			t.Fatalf("%s lost pause protection: %v", name, err)
		}
	}
	rows, err := d.List(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Status != "Paused" || !strings.Contains(rows[0].StatusDetail, "host filesystem") {
		t.Fatalf("missing visible pause: %+v %v", rows, err)
	}
	time.Sleep(30 * time.Millisecond)
	if size() != before {
		t.Fatal("paused child continued writing")
	}
	low = false
	if err := g.step(); err != nil {
		t.Fatal(err)
	}
	waitFor(func() bool { return size() > before })
	rows, err = d.List(t.Context())
	if err != nil || rows[0].Status != "Running" || rows[0].StatusDetail != "" {
		t.Fatalf("did not restore availability: %+v %v", rows, err)
	}
}
