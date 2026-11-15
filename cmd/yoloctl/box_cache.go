package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxcache"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

func newAgentboxCacheCmd() *cobra.Command {
	parent := &cobra.Command{Use: "cache", Short: "Manage one box's VM-local caches"}
	var space string
	cmd := &cobra.Command{
		Use: "clear NAME", Short: "Empty a stopped box's separate cache directory",
		Long: `Clear only this box's controller-owned /home/sandbox/.cache bind on the VM.
Stop the box first. A box created before the cache bind keeps its caches in its
container layer; this command cannot remove those. Recreate that legacy box
(which discards files outside its binds) or clear from inside it instead.

The workdir, account state, installed binaries outside .cache, and other boxes
are preserved. No automatic cleanup collects these per-box caches. A process
that points its cache elsewhere still writes outside this directory.

See yoloctl space storage to identify large boxes. Inside a running box use
its tool's own cache commands, for example: box exec NAME go clean -cache -modcache.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := resolveSpaceFlag(cmd, &space); err != nil {
				return err
			}
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			if err := spaceenv.RequireSpace(space); err != nil {
				return err
			}
			if err := controller.VerifyRuntimeStorage(cmd.Context()); err != nil {
				return err
			}
			lock, err := lockSessionBoxFn(space, name)
			if err != nil {
				return err
			}
			defer lock.Release()
			bytes, err := clearStoppedBoxCache(cmd.Context(), space, name,
				func(ctx context.Context, space, name string) (boxstate.Box, error) {
					return newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.RuntimeInventory)
				},
				func(ctx context.Context, name string) (uint64, error) {
					native := spaceenv.Current()
					return boxcache.Clear(ctx, name, native.UID, native.GID)
				})
			if err != nil {
				return err
			}
			cliInfo(logging.Storage, "cleared %d bytes from box %s's cache directory", bytes, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&space, "space", defaultSpaceName, "Space hosting the box")
	parent.AddCommand(cmd)
	return parent
}

func clearStoppedBoxCache(ctx context.Context, space, name string, observe func(context.Context, string, string) (boxstate.Box, error), clear func(context.Context, string) (uint64, error)) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	box, err := observe(ctx, space, name)
	if err != nil {
		return 0, fmt.Errorf("cannot establish that box %s is stopped; cache retained: %w", name, err)
	}
	if box.Declaration == boxstate.DeclarationMissing {
		return 0, fmt.Errorf("box %s has no declaration; cache retained", name)
	}
	if box.Declaration != boxstate.DeclarationDeclared && box.Declaration != boxstate.DeclarationInvalid {
		return 0, errors.New("box declaration state is unknown; cache retained")
	}
	switch box.Runtime {
	case boxstate.RuntimeRunning:
		return 0, fmt.Errorf("box %s is running; stop it first (yoloctl box stop %s) or clear from inside: box exec %s go clean -cache -modcache", name, name, name)
	case boxstate.RuntimeStopped:
		if box.Container == nil || !cacheInventoryStopped(box.Container.Status) {
			return 0, errors.New("container status does not affirm that it is stopped; cache retained")
		}
	case boxstate.RuntimeMissing:
		if box.Container != nil {
			return 0, errors.New("container absence is inconsistent; cache retained")
		}
	default:
		return 0, errors.New("container state is unknown; cache retained")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return clear(ctx, name)
}

func recreateCacheContext(cmd *cobra.Command, name string) context.Context {
	clear, _ := cmd.Flags().GetBool("clear-cache")
	if !clear {
		return cmd.Context()
	}
	return agentbox.WithRecreateCacheClear(cmd.Context(), func(bytes uint64) {
		cliInfo(logging.Storage, "cleared %d bytes from box %s's separate cache", bytes, name)
	})
}

// This is a destructive-observation check, not the display resolver's
// non-running fallback. Accept only affirmative terminal/never-started forms.
func cacheInventoryStopped(status string) bool {
	value := strings.ToLower(strings.TrimSpace(status))
	switch value {
	case "created", "stopped", "exited":
		return true
	}
	if !strings.HasPrefix(value, "exited (") {
		return false
	}
	end := strings.IndexByte(value, ')')
	if end < len("exited (") {
		return false
	}
	code, err := strconv.ParseInt(value[len("exited ("):end], 10, 64)
	if err != nil || code < 0 {
		return false
	}
	return len(value) == end+1 || value[end+1] == ' '
}
