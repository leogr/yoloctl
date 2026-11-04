package openshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// runtimeEntry is reached only by the dedicated helper after its fixed roles
// and authenticated qualification projection have been checked. It has no CLI,
// host/controller initialization or environment/config fallback.
func runtimeEntry(args []string, release QualifiedRelease) error {
	if len(args) == 0 {
		return errors.New("missing private runtime role")
	}
	if args[0] != "runtime" {
		role := runtimeChildRole(args[0])
		if _, err := runtimeChildFiles(role); err != nil {
			return err
		}
		launch, err := parseHelperFlags(args[1:])
		if err != nil {
			return err
		}
		// This child must exit on every returned error. It cannot reach Cobra,
		// host contexts or controller setup after a partial namespace transition.
		return execRuntimeChild(role, launch, release)
	}
	launch, err := ParseHelperLaunch(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()
	return runRuntimePID1(ctx, launch, release)
}

func runRuntimePID1(ctx context.Context, launch HelperLaunch, release QualifiedRelease) (err error) {
	material, err := readRuntimeMaterial(launch, release)
	if err != nil {
		return err
	}
	record := material.record
	if err := writeRuntimeStatus(record, observeRuntimeStatus(record, "starting", "startup", nil, time.Now())); err != nil {
		return err
	}
	if _, err := awaitNativeActivation(ctx, record); err != nil {
		return err
	}
	shared, err := prepareRuntimeShared(record)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, shared.close()) }()
	artifacts, err := openRuntimeInfrastructure(ArtifactProjection)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, artifacts.Close()) }()
	var children []*runtimeProcess
	defer func() {
		err = errors.Join(err, stopRuntimeChildren(children))
		phase, reason := "stopped", "stopped"
		for _, child := range children {
			if !child.exited {
				phase, reason = "stopping", "cleanup-unproved"
			}
		}
		err = errors.Join(err, writeRuntimeStatus(record, observeRuntimeStatus(record, phase, reason, children, time.Now())))
	}()
	for _, role := range []runtimeChildRole{runtimeSupervisor, runtimeBoundary} {
		child, err := startRuntimeChild(material, role, shared, artifacts)
		if err != nil {
			return err
		}
		children = append(children, child)
	}
	// No root child needs another copy. PID 1 remains nondumpable and never
	// exports these bytes through a diagnostic or inherited environment.
	for _, body := range material.files {
		clear(body)
	}
	material.files = nil
	return monitorRuntime(ctx, record, shared, children)
}

func runtimePhase(everReady, supervisorAlive, boundaryAlive, upstreamReady bool, probeErr error) (string, string, error) {
	if !boundaryAlive {
		return "stopped", "stopped", errors.New("OpenShell boundary exited; runtime terminated")
	}
	if !supervisorAlive {
		if !everReady {
			return "stopped", "stopped", errors.New("OpenShell supervisor exited before initial readiness")
		}
		return "degraded", "supervisor-lost", nil
	}
	if probeErr != nil {
		if !everReady {
			return "stopped", "stopped", probeErr
		}
		return "degraded", "endpoint-invalid", nil
	}
	if upstreamReady {
		return "ready", "confirmed", nil
	}
	if everReady {
		return "degraded", "upstream-unready", nil
	}
	return "starting", "startup", nil
}

func monitorRuntime(ctx context.Context, record RuntimeRecord, shared runtimeShared, children []*runtimeProcess) (err error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(record.SSHHostPublicKey))
	if err != nil {
		return err
	}
	var admission atomic.Bool
	relayCtx, cancelRelay := context.WithCancel(context.Background())
	defer cancelRelay()
	var publicDirectory *os.File
	var endpoint *runtimeSSH
	var relayDone chan error
	defer func() {
		admission.Store(false)
		cancelRelay()
		if endpoint != nil {
			err = errors.Join(err, endpoint.Close())
		}
		if relayDone != nil {
			select {
			case relayErr := <-relayDone:
				err = errors.Join(err, relayErr)
			case <-time.After(2 * time.Second):
				err = errors.Join(err, errors.New("public SSH relay did not drain before runtime stop"))
			}
		}
		if publicDirectory != nil {
			err = errors.Join(err, publicDirectory.Close())
		}
	}()
	initialDeadline := time.Now().Add(45 * time.Second)
	everReady := false
	lastPhase, lastReason := "", ""
	stored := false
	statusErrorReported := false
	var lastProbe, lastWrite time.Time
	upstreamReady := false
	var probeErr error
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := reapRuntimeChildren(children); err != nil {
			return err
		}
		supervisorAlive, boundaryAlive := false, false
		for _, child := range children {
			if child.role == runtimeSupervisor {
				supervisorAlive = !child.exited
			}
			if child.role == runtimeBoundary {
				boundaryAlive = !child.exited
			}
		}
		now := time.Now()
		if supervisorAlive && boundaryAlive && (lastProbe.IsZero() || now.Sub(lastProbe) >= time.Second) {
			upstreamReady, probeErr = probeRuntimeReadyAt(ctx, shared.supervisorRun, key, int(SupervisorUID), int(SupervisorGID))
			lastProbe = time.Now()
		}
		phase, reason, decisionErr := runtimePhase(everReady, supervisorAlive, boundaryAlive, upstreamReady, probeErr)
		if decisionErr != nil {
			return decisionErr
		}
		if !everReady && !time.Now().Before(initialDeadline) {
			return errors.New("OpenShell initial attach/SSH readiness timed out")
		}
		if phase == "ready" && endpoint == nil {
			publicDirectory, err = openRuntimeInfrastructure(publicRuntimeSSHDirectory)
			if err != nil {
				return err
			}
			endpoint, err = listenRuntimeSSHAt(publicDirectory, 0, 0)
			if err != nil {
				return err
			}
			relayDone = make(chan error, 1)
			target := fmt.Sprintf("/proc/self/fd/%d/ssh/ssh.sock", shared.supervisorRun.Fd())
			go func() {
				relayDone <- endpoint.serveRuntimeSSH(relayCtx, target, admission.Load, func() error {
					return checkRuntimePrivateSocket(shared.supervisorRun, "ssh/ssh.sock", int(SupervisorUID), int(SupervisorGID))
				})
			}()
		}
		// Remove NEW admission immediately on degradation. Existing streams and
		// the boundary remain alive; no D3 deadline, restart or replay is added.
		if phase != "ready" {
			admission.Store(false)
		}
		changed := phase != lastPhase || reason != lastReason
		if changed || (!stored && time.Since(lastWrite) >= time.Second) {
			writeErr := writeRuntimeStatus(record, observeRuntimeStatus(record, phase, reason, children, time.Now()))
			lastWrite = time.Now()
			lastPhase, lastReason = phase, reason
			stored = writeErr == nil
			if writeErr != nil {
				admission.Store(false)
				if !everReady {
					return writeErr
				}
				// Retrying a current observation never retries an application
				// operation or proves its lost reply. Preserve running children.
				if !statusErrorReported {
					_, _ = fmt.Fprintln(os.Stderr, "OpenShell runtime status unavailable; new SSH admission closed")
					statusErrorReported = true
				}
			} else {
				statusErrorReported = false
			}
		}
		if stored && phase == "ready" {
			admission.Store(true)
			everReady = true
		}
		select {
		case <-ctx.Done():
			return nil
		case relayErr := <-relayDone:
			// Consume once; deferred cleanup must not wait for this value twice.
			relayDone = nil
			if relayErr == nil {
				relayErr = errors.New("public SSH relay ended unexpectedly")
			}
			return relayErr
		case <-ticker.C:
		}
	}
}
