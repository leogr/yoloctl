package openshell

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// execRuntimeChild is a terminal re-exec child entry: the entrypoint must exit
// on any returned error. After a partial identity/namespace transition it must
// never return to CLI/controller initialization or unlock its OS thread.
func execRuntimeChild(role runtimeChildRole, launch HelperLaunch, release QualifiedRelease) (err error) {
	handoff, err := readRuntimeChildHandoff(role, launch, release)
	if err != nil {
		return fmt.Errorf("%s child handoff: %w", role, err)
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, handoff.closeSources())
		}
	}()
	if err := prepareRuntimeChildView(handoff.payload, handoff.shared, handoff.artifacts); err != nil {
		return fmt.Errorf("%s child mount view: %w", role, err)
	}
	config := handoff.payload.Record.Launch
	if role == runtimeBoundary {
		if err := prepareBoundaryNetwork(config.UnconfinedNetwork, handoff.proof.Namespace); err != nil {
			return fmt.Errorf("boundary network setup: %w", err)
		}
		if err := prepareRuntimeDNS(config); err != nil {
			return fmt.Errorf("boundary DNS setup: %w", err)
		}
	}
	// The mount sources remain mounted without their source FDs. No native
	// generation directory, sealed bootstrap, namespace or directory descriptor
	// can survive into either privileged handoff or the final cap-free process.
	if err := handoff.closeSources(); err != nil {
		closed = true
		return fmt.Errorf("%s child source close: %w", role, err)
	}
	closed = true
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return fmt.Errorf("%s child close_range CLOEXEC: %w", role, err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("%s child chdir: %w", role, err)
	}
	if role == runtimeSupervisor {
		args, err := config.SupervisorArgs()
		if err != nil {
			return err
		}
		return fmt.Errorf("supervisor privilege/exec handoff: %w", execSupervisor(ArtifactProjection+"/openshell-supervisor", args, SupervisorEnvironment()))
	}
	if role != runtimeBoundary {
		return errors.New("unrecognized private runtime exec role")
	}
	args, err := config.BoundaryArgs()
	if err != nil {
		return err
	}
	environment, err := runtimeBoundaryEnvironment(handoff.payload.BoundaryEnv)
	if err != nil {
		return err
	}
	binary := ArtifactProjection + "/openshell-sandbox"
	// This exact qualified command performs the root→capability-free identity
	// transition. Workloads are still admitted only over authenticated StartAgent
	// and supervisor SSH. Container ENV comes only from the verified parent's
	// sealed boundary payload, never this private role's ambient environment.
	return fmt.Errorf("boundary exec: %w", unix.Exec(binary, append([]string{binary}, args...), environment))
}
