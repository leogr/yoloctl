package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/mainlaunch"
)

func (fs *fakeSandbox) snapshot() (envs []string, subsystem string, marked bool, execs []string, handover, handoverCmd string, stdin string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return slices.Clone(fs.envs), fs.subsystem, fs.mainMarked, slices.Clone(fs.execs), fs.handoverStdin.String(), fs.handoverCmd, fs.stdin.String()
}

// A main start attaches to openshell-main with raw input, writes its marker,
// hands the launch over once (no login shell, the record on stdin), and
// serves clients and forwards their input only after it was delivered.
func TestHolderMainStartDelivered(t *testing.T) {
	opts, _ := testPaths(t)
	opts.StartMain = true
	fs := startFakeSandbox(t, opts.SSHSocket)
	gate := make(chan struct{})
	fs.handoverGate = gate
	markerAtHandover := false
	fs.later = func() { _, err := readSessionFile[Marker](opts.MarkerPath); markerAtHandover = err == nil }
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	// While the hand-over is pending nothing reaches the main process and no
	// client is served yet (the attach socket opens once the session started).
	waitFor(t, "the hand-over", func() bool { _, _, _, _, _, cmd, _ := fs.snapshot(); return cmd != "" })
	if _, err := os.Stat(opts.AttachSocket); !os.IsNotExist(err) {
		t.Fatal("clients were served before the hand-over answered")
	}
	close(gate)
	a := attachWhenReady(t, opts.AttachSocket)
	if _, err := a.stdinW.Write([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "input after the hand-over", func() bool { _, _, _, _, _, _, stdin := fs.snapshot(); return stdin == "typed" })
	fs.emit("output")
	fs.exit(7)
	r := <-res
	if r.err != nil || r.status != 7 {
		t.Fatalf("RunHolder = %d, %v", r.status, r.err)
	}
	envs, subsystem, marked, execs, record, cmd, _ := fs.snapshot()
	if subsystem != "openshell-main" || marked || !slices.Contains(envs, "0:OPENSHELL_MAIN_RAW_INPUT=1") || !slices.Contains(envs, "1:OPENSHELL_NO_LOGIN_SHELL=1") {
		t.Fatalf("envs %q subsystem %q marked %v", envs, subsystem, marked)
	}
	if cmd != mainlaunch.HandoverScript || len(execs) != 1 || !markerAtHandover {
		t.Fatalf("execs %d (hand-over only), marker before it %v", len(execs), markerAtHandover)
	}
	lines := strings.SplitN(record, "\n", 3)
	if len(lines) != 3 || lines[0] != "TERM="+opts.Term {
		t.Fatalf("record header %q", record)
	}
	token := lines[1]
	want, err := mainlaunch.Record(opts.Term, token, []byte("exec "+shellJoin(opts.Argv)))
	if err != nil || record != string(want) {
		t.Fatalf("record %q, want %q (%v)", record, want, err)
	}
}

// The outcome table: no mkfifo and no FIFO are proven not delivered (marker
// removed, nothing forwarded); any other answer is not known (marker kept,
// the holder ends as connection lost). The launch is never sent twice.
func TestHolderMainStartHandoverOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   func(error) bool
		marker bool
	}{
		{"no mkfifo", mainlaunch.HandoverNoMkfifo, func(err error) bool { return errors.Is(err, ErrNoMkfifo) }, false},
		{"no FIFO", mainlaunch.HandoverNoFIFO, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not waiting") }, false},
		{"name holds the token", mainlaunch.HandoverNameHasToken, func(err error) bool { return err != nil && !errors.Is(err, ErrHolderConnLost) }, false},
		{"another status", 9, func(err error) bool { return errors.Is(err, ErrHolderConnLost) }, true},
		{"no status", -1, func(err error) bool { return errors.Is(err, ErrHolderConnLost) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, dir := testPaths(t)
			opts.StartMain = true
			opts.RefusalPath = filepath.Join(dir, "refused")
			fs := startFakeSandbox(t, opts.SSHSocket)
			fs.handoverStatus = tc.status
			r := <-runHolderAsync(context.Background(), opts)
			// Only directive 61's refusal leaves a record for the caller.
			refusal, err := readSessionFile[Refusal](opts.RefusalPath)
			if tc.status == mainlaunch.HandoverNoMkfifo {
				if err != nil || refusal.Reason != RefusalNoMkfifo {
					t.Fatalf("refusal record %+v, %v", refusal, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("a refusal record was written: %+v, %v", refusal, err)
			}
			if !tc.want(r.err) || r.status != -1 {
				t.Fatalf("RunHolder = %d, %v", r.status, r.err)
			}
			if _, err := readSessionFile[Marker](opts.MarkerPath); (err == nil) != tc.marker {
				t.Fatalf("marker present %v, want %v", err == nil, tc.marker)
			}
			if _, err := readSessionFile[Tombstone](opts.TombstonePath); !os.IsNotExist(err) {
				t.Fatal("a tombstone was written")
			}
			_, _, _, execs, _, _, stdin := fs.snapshot()
			if len(execs) != 1 || stdin != "" {
				t.Fatalf("the launch was sent %d times; input %q", len(execs), stdin)
			}
		})
	}
}

// A refused raw-input switch or main attachment is a startup failure before
// the marker: nothing is handed over.
func TestHolderMainStartRefusals(t *testing.T) {
	for _, refuse := range []string{"raw", "main"} {
		t.Run(refuse, func(t *testing.T) {
			opts, _ := testPaths(t)
			opts.StartMain = true
			fs := startFakeSandbox(t, opts.SSHSocket)
			fs.refuseRaw, fs.refuseMain = refuse == "raw", refuse == "main"
			r := <-runHolderAsync(context.Background(), opts)
			if r.err == nil || errors.Is(r.err, ErrHolderConnLost) {
				t.Fatalf("RunHolder err %v", r.err)
			}
			if _, err := readSessionFile[Marker](opts.MarkerPath); !os.IsNotExist(err) {
				t.Fatal("a marker was written")
			}
			if _, _, _, execs, _, _, _ := fs.snapshot(); len(execs) != 0 {
				t.Fatal("a launch was handed over")
			}
		})
	}
}

// A privileged main session: the main-session request before today's exec;
// a refusal starts nothing and leaves no marker.
func TestHolderMarkMain(t *testing.T) {
	opts, _ := testPaths(t)
	opts.MarkMain = true
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v", r.status, r.err)
	}
	if _, subsystem, marked, execs, _, _, _ := fs.snapshot(); !marked || subsystem != "" || len(execs) != 1 {
		t.Fatalf("marked %v subsystem %q execs %d", marked, subsystem, len(execs))
	}

	opts, _ = testPaths(t)
	opts.MarkMain = true
	fs = startFakeSandbox(t, opts.SSHSocket)
	fs.refuseMark = true
	r := <-runHolderAsync(context.Background(), opts)
	if r.err == nil || !strings.Contains(r.err.Error(), "main-session request") {
		t.Fatalf("RunHolder err %v", r.err)
	}
	if _, err := readSessionFile[Marker](opts.MarkerPath); !os.IsNotExist(err) {
		t.Fatal("a marker was kept")
	}
	if _, _, _, execs, _, _, _ := fs.snapshot(); len(execs) != 0 {
		t.Fatal("the session started after a refusal")
	}
}

// Section 5's condition (coordinator, option a): the holder ends its whole
// SSH connection right after its output is drained and it has the exit
// status, before local viewer finalization, also with a viewer attached, so
// the supervisor's drain is expected to complete promptly.
func TestHolderMainStartClosesItsConnectionAfterTheStatus(t *testing.T) {
	opts, _ := testPaths(t)
	opts.StartMain = true
	fs := startFakeSandbox(t, opts.SSHSocket)
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	a := attachWhenReady(t, opts.AttachSocket)
	if _, err := a.stdinW.Write([]byte("typed")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the viewer", func() bool { _, _, _, _, _, _, stdin := fs.snapshot(); return stdin == "typed" })
	start := time.Now()
	fs.exit(5)
	if r := <-res; r.err != nil || r.status != 5 {
		t.Fatalf("RunHolder = %d, %v", r.status, r.err)
	}
	select {
	case <-fs.connClosed:
	case <-time.After(time.Second):
		t.Fatal("the holder kept its SSH connection after the exit status")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("connection ended %v after the status", elapsed)
	}
	_ = a.wait(t)
}
