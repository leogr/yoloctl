package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/spf13/cobra"
)

type spaceReleaseKey struct{}
type spaceHeldKey struct{}
type controllerAdvisoryKey struct{}

// spaceOperationHeld reports whether this command still holds its native
// space-operation lock: the wrapper took it and nothing has released it.
// A release function in the context is a witness only while its guard holds.
func spaceOperationHeld(ctx context.Context) bool {
	held, ok := ctx.Value(spaceHeldKey{}).(func() bool)
	return ok && held()
}

// releaseSpaceOperation ends setup: the native guard first (with its role
// synchronization), then the controller's acknowledged setup checkpoint. A
// failed synchronization never announces a successful setup.
// controller.ErrClientDetached means an owned attach setup finished after
// its client left; the caller must not stream.
func releaseSpaceOperation(ctx context.Context) error {
	if release, ok := ctx.Value(spaceReleaseKey{}).(func() error); ok {
		if err := release(); err != nil {
			return err
		}
	}
	if release, ok := ctx.Value(controllerReleaseKey{}).(func() error); ok {
		return release()
	}
	return nil
}

// attachFastPathEligible reports whether a native admin attach may join a
// live holder without the ordinary ensure. --new-session never may: the
// ensure refuses it on a live holder, so the guard cannot be skipped.
func attachFastPathEligible(native, admin bool, path string, args []string, guardedAttach, newSession bool) bool {
	return native && admin && (path == "box attach" || path == "attach") && len(args) == 1 && !guardedAttach && !newSession
}

// Wrap mutations once at the command boundary, before box/account locks.
// Long-lived terminal attachment releases this guard after runtime setup.
func registerSpaceOperations(root *cobra.Command) {
	for _, cmd := range root.Commands() {
		registerSpaceOperations(cmd)
	}
	if root.RunE == nil {
		return
	}
	original := root.RunE
	root.RunE = func(cmd *cobra.Command, args []string) (result error) {
		path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
		if path == "box recreate" {
			cmd.SetContext(agentbox.WithRuntimeModeChange(agentbox.WithChatRecreate(cmd.Context())))
		}
		words := strings.Fields(path)
		if len(words) == 0 {
			return original(cmd, args)
		}
		mutates := false
		// Even box setup can boot the VM or prepare its data disk. Serialize
		// setup with replacement and account mutations, then release before
		// entering a live terminal. Reads do not lock; editors manage snapshot
		// transactions so they hold no locks while the editor is open.
		positional := false
		switch words[0] {
		case "space":
			if len(words) > 1 {
				switch words[1] {
				case "create", "start", "stop", "recreate", "destroy":
					mutates = true
					positional = true
				case "mount":
					if len(words) > 2 && words[2] != "list" {
						mutates = true
						positional = true
					}
				case "ssh":
					mutates = true
					positional = true
				}
			}
		case "account":
			mutates = path != "account edit" && path != "account list" && path != "account show" && path != "account secret list" && path != "account auth status"
		case "box":
			if len(words) > 1 {
				switch words[1] {
				case "create", "copy", "start", "stop", "restart", "recreate", "destroy", "reconcile", "set", "attach":
					mutates = true
				case "exec":
					mutates = !currentControllerPrincipal(cmd.Context()).Admin()
				}
			}
		case "attach":
			mutates = true
		case "port":
			mutates = path != "port list" && path != "port ls"
		case "image":
			if len(words) > 1 && words[1] != "list" {
				mutates = true
			}
		case "agent":
			mutates = len(words) > 1 && words[1] == "update"
		}
		if !mutates {
			return original(cmd, args)
		}
		var space string
		var err error
		if positional {
			selected := args
			if len(words) > 1 && words[1] == "ssh" {
				selected, _, err = splitSpaceShellArgs(args, cmd.ArgsLenAtDash())
				if err != nil {
					return err
				}
			}
			if len(selected) > 1 {
				selected = selected[:1]
			}
			space, err = resolveSpacePositional(selected)
		} else {
			space, err = selectedSpace(cmd)
		}
		if err != nil {
			return err
		}
		guardedAttach := chatAttachGuardPresent(cmd)
		newSession := cmd.Flag(newSessionFlag) != nil && cmd.Flag(newSessionFlag).Value.String() == "true"
		if attachFastPathEligible(spaceenv.Native(), currentControllerPrincipal(cmd.Context()).Admin(), path, args, guardedAttach, newSession) {
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			probe, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			active := holderUnitActiveFn(probe, container.New(space), args[0])
			cancel()
			if active {
				// Joining an existing holder has no setup mutation. If it
				// disappears during the probe, fail this attach; never revive
				// a session without taking the normal mutation lock. The
				// gated handoff still needs its durable permission first.
				if err := beginOperationEffects(cmd, path, space, args); err != nil {
					return err
				}
				if err := releaseSpaceOperation(cmd.Context()); err != nil {
					return err
				}
				return finishAttach(attachToSession(cmd.Context(), space, args[0]), cmd.ErrOrStderr(), space, args[0])
			}
		}
		if path == "image prune" {
			if err := beginOperationEffects(cmd, path, space, args); err != nil {
				return err
			}
		}
		if state, ok := cmd.Context().Value(warmKey{}).(*imageWarmState); ok && (path == "image pull" || path == "image refresh") && len(state.entries) != 0 {
			if err := beginOperationEffects(cmd, path, space, args); err != nil {
				return err
			}
		}
		spaces := []string{space}
		if path == "box copy" && !spaceenv.Native() {
			if f := cmd.Flag("from-space"); f != nil && f.Value.String() != "" && f.Value.String() != space {
				spaces = append(spaces, f.Value.String())
			}
		}
		sort.Strings(spaces)
		releases := []func(){}
		complete, started, released := false, false, false
		syncRoles := words[0] == "box" && path != "box exec" || path == "attach" || path == "account destroy"
		// The execution's own outcome, captured only when original returned
		// normally; a panic or an early setup release leaves it unset.
		var outcome *error
		recordsCompletion := claimWorkerCompletion(cmd.Context())
		var releaseErr error
		release := func() error {
			if released {
				return releaseErr
			}
			released = true
			// Reconcile/diff reads must not repair roles. Box lifecycle and
			// account destruction synchronize before releasing their guard.
			dryRun := cmd.Flag("dry-run") != nil && cmd.Flag("dry-run").Value.String() == "true"
			if complete && started && syncRoles && !dryRun {
				releaseErr = syncNativeChatRolesFn(cmd.Context(), space)
			}
			// Completion follows a successful role publication and precedes
			// the lock release. A failed publication is not a completed run.
			if recordsCompletion && outcome != nil && releaseErr == nil {
				recordWorkerCompletion(cmd.Context(), *outcome)
			}
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return releaseErr
		}
		defer func() {
			if err := release(); err != nil && !errors.Is(result, err) {
				result = errors.Join(result, err)
			}
		}()
		for _, name := range spaces {
			unlock, err := spacelock.AcquireWithWait(cmd.Context(), name, true, func() {
				if call, ok := cmd.Context().Value(controllerAdvisoryKey{}).(func(context.Context, controller.AdvisoryNotice) (controller.AdvisoryReply, error)); ok {
					if reply, err := call(cmd.Context(), controller.AdvisoryNotice{Action: "blocker"}); err == nil && reply.Holder != nil {
						cliInfo(logging.Host, "Waiting for space lock held by %s; Ctrl-C cancels.", reply.Holder.String())
						return
					}
				}
				cliInfo(logging.Host, "Waiting for another operation in space %s; Ctrl-C cancels.", name)
			})
			if err != nil {
				return err
			}
			releases = append(releases, unlock)
		}
		complete = true
		previous := cmd.Context()
		held := func() bool { return !released }
		cmd.SetContext(context.WithValue(context.WithValue(previous, spaceReleaseKey{}, release), spaceHeldKey{}, held))
		defer cmd.SetContext(previous)
		if (words[0] == "box" || words[0] == "attach") && !currentControllerPrincipal(cmd.Context()).Admin() {
			if err := revalidateOperatorFn(cmd.Context()); err != nil {
				return err
			}
		}
		if guardedAttach {
			if err := validateChatAttachCommand(cmd, space, args); err != nil {
				return err
			}
		}
		// Every gated mutation durably records its footprint and receives
		// permission here, after the native lock and before any effect,
		// including the deferred role synchronization below.
		lazy := warmingPath(path) && cmd.Context().Value(warmKey{}) != nil
		if lazy {
			cmd.SetContext(context.WithValue(cmd.Context(), warmGateKey{}, func() error {
				if err := beginOperationEffects(cmd, path, space, args); err != nil {
					return err
				}
				started = true
				return nil
			}))
		} else {
			if wrapperGatesEffects(path) {
				if err := beginOperationEffects(cmd, path, space, args); err != nil {
					return err
				}
			}
			started = true
		}
		err = original(cmd, args)
		outcome = &err
		return err
	}
}
