package openshell

import (
	"os"
	"slices"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestStopRuntimeChildrenOrdering(t *testing.T) {
	type signalEvent struct {
		at     time.Duration
		role   runtimeChildRole
		signal os.Signal
	}
	const (
		tick  = 20 * time.Millisecond
		grace = 5 * time.Second
	)
	for _, row := range []struct {
		name           string
		ordered        bool
		handshake      bool
		supervisorExit time.Duration // Negative means no independent exit.
		boundaryExit   time.Duration
		wantSignals    []signalEvent
		wantWait       time.Duration
		wantError      bool
	}{
		{
			name: "supervisor acknowledges before boundary TERM", ordered: true, handshake: true,
			supervisorExit: -1, boundaryExit: -1, wantWait: 2 * tick,
			wantSignals: []signalEvent{{0, runtimeSupervisor, syscall.SIGTERM}, {tick, runtimeBoundary, syscall.SIGTERM}},
		},
		{
			name: "parallel TERM interrupts the same handshake", handshake: true,
			supervisorExit: -1, boundaryExit: -1, wantWait: grace,
			wantSignals: []signalEvent{
				{0, runtimeSupervisor, syscall.SIGTERM},
				{0, runtimeBoundary, syscall.SIGTERM},
				{grace, runtimeSupervisor, syscall.SIGKILL},
				{grace, runtimeBoundary, syscall.SIGKILL},
			},
		},
		{
			name: "boundary exits first", ordered: true,
			supervisorExit: 2 * tick, boundaryExit: tick, wantWait: 2 * tick,
			wantSignals: []signalEvent{{0, runtimeSupervisor, syscall.SIGTERM}},
		},
		{
			name: "supervisor already exited", ordered: true,
			supervisorExit: 0, boundaryExit: tick, wantWait: tick,
			wantSignals: []signalEvent{{0, runtimeBoundary, syscall.SIGTERM}},
		},
		{
			name: "parallel cleanup", ordered: false,
			supervisorExit: tick, boundaryExit: tick, wantWait: tick,
			wantSignals: []signalEvent{{0, runtimeSupervisor, syscall.SIGTERM}, {0, runtimeBoundary, syscall.SIGTERM}},
		},
		{
			name: "supervisor uses all of grace", ordered: true,
			supervisorExit: -1, boundaryExit: -1, wantWait: grace,
			wantSignals: []signalEvent{
				{0, runtimeSupervisor, syscall.SIGTERM},
				{grace, runtimeSupervisor, syscall.SIGKILL},
				{grace, runtimeBoundary, syscall.SIGKILL},
			},
		},
		{
			name: "boundary gets only the remaining grace", ordered: true,
			supervisorExit: grace - tick, boundaryExit: -1, wantWait: grace,
			wantSignals: []signalEvent{
				{0, runtimeSupervisor, syscall.SIGTERM},
				{grace - tick, runtimeBoundary, syscall.SIGTERM},
				{grace, runtimeBoundary, syscall.SIGKILL},
			},
		},
		{
			name: "SIGKILL without wait status keeps cleanup unproved", ordered: true,
			supervisorExit: -1, boundaryExit: -1, wantWait: grace + 3*time.Second, wantError: true,
			wantSignals: []signalEvent{
				{0, runtimeSupervisor, syscall.SIGTERM},
				{grace, runtimeSupervisor, syscall.SIGKILL},
				{grace, runtimeBoundary, syscall.SIGKILL},
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				supervisor := &runtimeProcess{role: runtimeSupervisor, originalPID: 7}
				boundary := &runtimeProcess{role: runtimeBoundary, originalPID: 8}
				children := []*runtimeProcess{supervisor, boundary}
				exits := []time.Duration{row.supervisorExit, row.boundaryExit}
				statuses := []syscall.WaitStatus{7 << 8, 8 << 8}
				var supervisorTerm, boundaryTerm, acknowledged bool
				var signals []signalEvent
				signal := func(targets []*runtimeProcess, sig os.Signal) error {
					for _, child := range targets {
						if child.exited {
							continue
						}
						now := time.Since(started)
						signals = append(signals, signalEvent{now, child.role, sig})
						if row.handshake && sig == syscall.SIGTERM {
							if child == supervisor {
								supervisorTerm = true
							} else {
								boundaryTerm = true
								if acknowledged {
									exits[1] = now + tick
								}
							}
						}
						if sig == syscall.SIGKILL && !row.wantError {
							index := slices.Index(children, child)
							exits[index], statuses[index] = now, syscall.WaitStatus(syscall.SIGKILL)
						}
					}
					return nil
				}
				reap := func(tracked []*runtimeProcess) error {
					if len(tracked) != 2 || tracked[0] != supervisor || tracked[1] != boundary {
						t.Fatalf("reap did not track both original children: %v", tracked)
					}
					elapsed := time.Since(started)
					// The fake supervisor can finish its shutdown only if the
					// boundary is still available to acknowledge the request.
					if supervisorTerm && !boundaryTerm && !acknowledged && elapsed >= tick {
						acknowledged = true
						exits[0] = elapsed
					}
					for index, child := range tracked {
						if !child.exited && exits[index] >= 0 && exits[index] <= elapsed {
							child.exited, child.status = true, statuses[index]
						}
					}
					return nil
				}
				err := stopRuntimeChildrenWith(children, row.ordered, signal, reap)
				if (err != nil) != row.wantError || time.Since(started) != row.wantWait {
					t.Fatalf("stop returned %v after %v, want error=%v after %v", err, time.Since(started), row.wantError, row.wantWait)
				}
				if !slices.Equal(signals, row.wantSignals) {
					t.Fatalf("signals = %v, want %v", signals, row.wantSignals)
				}
				if row.handshake && acknowledged != row.ordered {
					t.Fatalf("shutdown acknowledgement = %v, want %v", acknowledged, row.ordered)
				}
				if row.wantError {
					if supervisor.exited || boundary.exited {
						t.Fatal("cleanup invented a wait status")
					}
					return
				}
				for index, child := range children {
					if !child.exited || child.status != statuses[index] {
						t.Fatalf("%s exit = %v, status = %v, want %v", child.role, child.exited, child.status, statuses[index])
					}
				}
			})
		})
	}
}
