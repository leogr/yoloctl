package main

import (
	"context"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/spf13/cobra"
)

func newAgentboxDestroyCmd() *cobra.Command {
	var (
		spaceName    string
		force        bool
		keepManifest bool
	)
	cmd := &cobra.Command{
		Use:   "destroy [BOX]",
		Short: "Stop and remove a box (runtime + its declaration)",
		Long: "Stops the box gracefully (so openshell-sandbox cleans up its " +
			"network namespace), removes the container, port forwards, and " +
			"VM-local runtime dirs, AND deletes the box YAML declaration. " +
			"A host --workdir and account state are always preserved; only " +
			"an ephemeral box's VM-local /workdir is wiped (guarded — pass " +
			"--force). Use `box stop` to keep both the manifest and runtime " +
			"declaration; --keep-manifest here removes only the runtime. --force also " +
			"skips the graceful stop. Without --keep-manifest, the box's own chat " +
			"membership is revoked too, so the name can be enrolled again.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			c := container.New(spaceName)
			// Deletion requires a complete inventory. A summary's unavailable
			// state is useful for display, but does not prove runtime is absent.
			resolved, err := newBoxStateResolver().ResolveOne(cmd.Context(), spaceName, name, boxstate.RuntimeInventory)
			if err != nil {
				return fmt.Errorf("inspect box %q before destroy: %w", name, err)
			}
			if resolved.Declaration == boxstate.DeclarationMissing && resolved.Runtime == boxstate.RuntimeMissing {
				return asUserError(fmt.Errorf("box %q not found in space %q", name, spaceName))
			}
			// Determine the workdir mode from the declared config so we
			// know whether destroy wipes real data. Host workdirs are
			// preserved (the host dir is the source of truth); only an
			// ephemeral box's VM-local /workdir is lost. If the YAML is
			// unreadable we can't tell — guard conservatively.
			ephemeralData := true
			if cfg := resolved.Config; cfg != nil {
				ephemeralData = !cfg.Workdir.IsHost()
			}
			if ephemeralData && !force {
				return asUserError(fmt.Errorf("refusing to destroy %s: its /workdir is VM-local (ephemeral, at %s) and will be wiped without a host copy. Pass --force to proceed, or copy state out first; use --workdir PATH next time to keep work on the host",
					name, sandbox.AgentboxDirHost(name)))
			}
			live := agentbox.LiveApplier{Client: c, VerifyStorage: controller.VerifyRuntimeStorage}
			var prepared *agentbox.NativeContainerRemoval
			native := false
			if resolved.Runtime != boxstate.RuntimeMissing {
				prepared, native, err = live.PrepareNativeContainerRemoval(cmd.Context(), name, force)
				if err != nil {
					return fmt.Errorf("prepare removal of %s: %w", name, err)
				}
			} else {
				// Old-path cleanup decides below; native records only produce
				// notices naming what was left in place.
				notices, err := live.CheckMissingNativeRemoval(cmd.Context(), name)
				if err != nil {
					return err
				}
				for _, notice := range notices {
					cliNotice(logging.Box, "%s", notice)
				}
			}
			if native {
				if err := removePreparedNativeBox(cmd.Context(), prepared,
					func(ctx context.Context) error { return withdrawNativeHolder(ctx, c, name) },
					nativeDestroyForwardWithdrawal(c, name)); err != nil {
					return err
				}
			} else {
				// The session dies with the box: stop the holder unit (its
				// clients see a clean disconnect; the agent itself goes down
				// with the container stop/remove below).
				if err := session.StopHolderUnit(cmd.Context(), c, name); err != nil {
					cliWarn(logging.Box, "session holder cleanup for %s: %v", name, err)
				}
				// Drop any port forwards owned by this agentbox before the
				// container goes away — the socat targets stop being routable
				// once the bridge IP is released.
				if err := port.UnforwardAgentbox(cmd.Context(), c, name); err != nil {
					return fmt.Errorf("port forward cleanup for %s: %w", name, err)
				}
				// A missing container is not a failure: destroy must still
				// clean up the declaration + VM dirs (that "configured but no
				// runtime" stale state is exactly what this removes). Only a
				// genuine removal error aborts.
				if resolved.Runtime != boxstate.RuntimeMissing {
					if !force && resolved.Runtime == boxstate.RuntimeRunning {
						_ = sandbox.Stop(cmd.Context(), c, name)
					}
					if err := c.Remove(cmd.Context(), name, force); err != nil {
						if !isContainerGone(err) {
							return err
						}
						cliNotice(logging.Box, "box %s has no running container; cleaning up its declaration and VM state", name)
					}
				}
				// Missing containers can still have a stale operator registration.
				if err := operator.RemoveBox(name, ""); err != nil {
					return err
				}
				// The forced path SIGKILLs the supervisor, and even the
				// graceful one escalates when the grace period runs out —
				// either way the sandbox netns/veth can leak (spike 0.3).
				// Collect whatever this teardown orphaned.
				if err := sandbox.ReapOrphanedNetns(cmd.Context(), spaceName, cmd.ErrOrStderr()); err != nil {
					cliWarn(logging.Box, "leaked netns cleanup for %s: %v", name, err)
				}

			}
			// Native control/generation/namespace metadata was removed ONLY by
			// the SDK under its original ACKs above. These are the existing
			// separate VM-local workdir/policy/session paths, never account or
			// host-workdir storage. Existing ephemeral/force guard still applies.
			if err := c.Shell(cmd.Context(), "sudo", "rm", "-rf",
				sandbox.SocketDirHost(name),
				sandbox.AgentboxDirHost(name),
				sandbox.AgentboxPolicyHostDir(name),
				session.StateDirFor(name),
				session.SocketPath(name)); err != nil {
				return err
			}
			// The container is gone, so its observation tmpfs can be unmounted.
			if err := agentbox.RemoveObservationRuntime(name); err != nil {
				return err
			}
			// Delete the per-agentbox declaration unless asked to keep it.
			// Host workdir + agent-state are never touched here.
			if !keepManifest {
				if err := removeAgentboxConfig(spaceName, name, cmd.ErrOrStderr()); err != nil {
					return err
				}
				// The name's chat membership would otherwise refuse a later
				// box with the same name. It only warns: the box is gone.
				boxID := ""
				if resolved.Config != nil {
					boxID = resolved.Config.ID
				}
				revokeDestroyedChat(cmd.Context(), spaceName, name, boxID, cmd.ErrOrStderr())
				// The controller schedules image GC after releasing this mutation.
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force-remove even if running (skips graceful stop) and wipe an ephemeral /workdir")
	cmd.Flags().BoolVar(&keepManifest, "keep-manifest", false, "Keep the box YAML manifest (remove only runtime state)")
	return cmd
}
