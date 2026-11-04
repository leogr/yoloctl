package openshell

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRuntimeSupervisorLossDegradesWithoutEndingTheBoundary(t *testing.T) {
	for _, row := range []struct {
		name                              string
		ever, supervisor, boundary, ready bool
		probe                             error
		phase, reason                     string
		fail                              bool
	}{
		{"startup", false, true, true, false, nil, "starting", "startup", false},
		{"confirmed", false, true, true, true, nil, "ready", "confirmed", false},
		{"lost-supervisor", true, false, true, true, nil, "degraded", "supervisor-lost", false},
		{"stalled-supervisor", true, true, true, false, nil, "degraded", "upstream-unready", false},
		{"invalid-endpoint", true, true, true, true, errors.New("invalid"), "degraded", "endpoint-invalid", false},
		{"same-process-ready-again", true, true, true, true, nil, "ready", "confirmed", false},
		{"initial-supervisor-death", false, false, true, false, nil, "stopped", "stopped", true},
		{"boundary-death", true, true, false, false, nil, "stopped", "stopped", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			phase, reason, err := runtimePhase(row.ever, row.supervisor, row.boundary, row.ready, row.probe)
			if phase != row.phase || reason != row.reason || (err != nil) != row.fail {
				t.Fatal(phase, reason, err)
			}
		})
	}
}

func TestRuntimeStatusBindsCurrentGenerationAndNeverClaimsUnprovedCleanup(t *testing.T) {
	f := newMaterialFixture(t)
	children := []*runtimeProcess{{role: runtimeSupervisor, process: &os.Process{Pid: 7}, originalPID: 7}, {role: runtimeBoundary, process: &os.Process{Pid: 8}, originalPID: 8}}
	status := observeRuntimeStatus(f.record, "ready", "confirmed", children, time.Now())
	if err := status.validate(f.record); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRuntimeStatus(raw, f.record); err != nil {
		t.Fatal(err)
	}
	other := f.record
	other.SessionID = "12345678-1234-4234-8234-123456789abc"
	if _, err := decodeRuntimeStatus(raw, other); err == nil {
		t.Fatal("another generation's observation accepted")
	}
	children[0].exited, children[0].status = true, syscall.WaitStatus(syscall.SIGKILL)
	degraded := observeRuntimeStatus(f.record, "degraded", "supervisor-lost", children, time.Now())
	if err := degraded.validate(f.record); err != nil || degraded.SupervisorExit == nil || *degraded.SupervisorExit != -int(syscall.SIGKILL) {
		t.Fatal(degraded, err)
	}
	if err := observeRuntimeStatus(f.record, "stopped", "stopped", children, time.Now()).validate(f.record); err == nil {
		t.Fatal("living boundary reported stopped")
	}
	if err := observeRuntimeStatus(f.record, "stopping", "cleanup-unproved", children, time.Now()).validate(f.record); err != nil {
		t.Fatal(err)
	}
	children[1].exited, children[1].status = true, 0
	if err := observeRuntimeStatus(f.record, "stopped", "stopped", children, time.Now()).validate(f.record); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{strings.Replace(string(raw), `"phase":"ready"`, `"phase":"ready","phase":"degraded"`, 1), strings.Replace(string(raw), `"schema":2`, `"schema":1`, 1), string(raw) + "{}", `{"unknown":true}`} {
		if _, err := decodeRuntimeStatus([]byte(body), f.record); err == nil {
			t.Fatal("malformed observation accepted")
		}
	}
}

func TestRuntimeStatusRetainsOriginalChildIdentityAfterRelease(t *testing.T) {
	f := newMaterialFixture(t)
	var children []*runtimeProcess
	for _, role := range []runtimeChildRole{runtimeSupervisor, runtimeBoundary} {
		cmd := exec.Command("sh", "-c", "exit 7")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		child := &runtimeProcess{role: role, process: cmd.Process, originalPID: cmd.Process.Pid}
		var exit *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &exit) {
			t.Fatalf("wait: %v", err)
		}
		child.exited, child.status = true, exit.Sys().(syscall.WaitStatus)
		if err := cmd.Process.Release(); err != nil {
			t.Fatal(err)
		}
		if cmd.Process.Pid != -1 {
			t.Fatal("release did not invalidate process PID")
		}
		children = append(children, child)
	}
	status := observeRuntimeStatus(f.record, "stopped", "stopped", children, time.Now())
	if err := status.validate(f.record); err != nil {
		t.Fatal(err)
	}
	if status.SupervisorPID != children[0].originalPID || status.BoundaryPID != children[1].originalPID || *status.SupervisorExit != 7 || *status.BoundaryExit != 7 {
		t.Fatalf("lost original status: %+v", status)
	}
}

func TestRuntimeEntryRejectsOrdinaryCallersAndUnknownRoles(t *testing.T) {
	f := newMaterialFixture(t)
	args, err := f.record.Launch.HelperLaunch().Args()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeEntry(args[2:], f.release); err == nil {
		t.Fatal("ordinary process became root runtime")
	}
	for _, args := range [][]string{nil, {"shell"}, {"runtime", "--command", "sh"}, {"boundary", "--parent-liveness-stdin"}} {
		if err := runtimeEntry(args, f.release); err == nil {
			t.Fatal("unsupported private entry accepted")
		}
	}
}
