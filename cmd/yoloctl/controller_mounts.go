package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

func controllerMountList(cmd *cobra.Command, args []string) error {
	name, err := resolveSpacePositional(args)
	if err != nil {
		return err
	}
	boot, err := spaceconfig.LoadBootstrap(name)
	if err != nil {
		return err
	}
	out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(out, "SOURCE\tACCESS\tKIND")
	for _, mount := range boot.Mounts {
		_, _ = fmt.Fprintf(out, "%s\t%s\tgrant\n", mount.Source, accessLabel(mount.Writable))
	}
	return out.Flush()
}

func controllerMountChange(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		space := args[0]
		if err := validateSpaceName(space); err != nil {
			return err
		}
		release, err := lockHostLifecycle(cmd, space)
		if err != nil {
			return err
		}
		defer release()
		boot, err := checkControllerSpace(cmd.Context(), space)
		if err != nil {
			return err
		}
		canonical, exists, err := canonicalizeMountArg(args[1])
		if err != nil {
			return err
		}
		identity := &spaceconfig.Config{AccountDiskID: boot.DiskID}
		if err := rejectSpaceHostPath(canonical, identity); err != nil {
			return err
		}
		var settings spaceconfig.Config
		if err := controllerJSON(cmd.Context(), boot, "space.settings", nil, nil, &settings, cmd.ErrOrStderr()); err != nil {
			return err
		}
		next := *boot
		next.Mounts = slices.Clone(boot.Mounts)
		proposed := settings
		proposed.Mounts = slices.Clone(boot.Mounts)
		if action == "add" {
			readOnly, _ := cmd.Flags().GetBool("read-only")
			if !proposed.AddMount(canonical, !readOnly) {
				cliInfo(logging.Host, "%s is already granted to space %s", canonical, space)
				return nil
			}
		} else if !proposed.RemoveMount(canonical) {
			return asUserError(fmt.Errorf("%s is not a mount grant of space %s", canonical, space))
		}
		if err := proposed.Validate(); err != nil {
			return asUserError(err)
		}
		next.Mounts = proposed.Mounts
		if err := next.Validate(); err != nil {
			return err
		}
		payload, err := json.Marshal(next.Mounts)
		if err != nil {
			return err
		}
		validation := controller.Request{ID: rand.Text(), Method: "space.validate-grants", Space: space, Dash: -1, Payload: payload}
		if err := callHostController(cmd.Context(), boot, validation, controller.ClientIO{Out: io.Discard, Err: cmd.ErrOrStderr()}); err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			ok, err := promptYesNo(fmt.Sprintf("restart space %s and stop its box sessions to apply mount changes? [y/N]: ", space), cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if !ok {
				return asUserError(errors.New("mount change declined; nothing changed"))
			}
		}
		if err := controllerControl(cmd.Context(), boot, "control.drain", cmd.ErrOrStderr()); err != nil {
			return err
		}
		// Recheck against current guest settings after draining: a read taken
		// before the user's confirmation cannot authorize a stale transition.
		if err := nativeInfrastructure(cmd.Context(), space, "validate-grants", next.Mounts, io.Discard, cmd.ErrOrStderr()); err != nil {
			return fmt.Errorf("mount change rejected; run space start %s to reopen the unchanged controller: %w", space, err)
		}
		if action == "add" && !exists {
			if err := os.MkdirAll(canonical, 0o755); err != nil {
				return fmt.Errorf("create mount directory; run space start %s to reopen unchanged controller: %w", space, err)
			}
		}
		if err := stopControllerSpace(cmd.Context(), boot, cmd.ErrOrStderr()); err != nil {
			return err
		}
		next.Generation = rand.Text()
		if err := next.Save(); err != nil {
			return fmt.Errorf("space stopped; inspect bootstrap and run space start %s to recover: %w", space, err)
		}
		if err := startControllerSpace(cmd.Context(), &next, false, nil, cmd.ErrOrStderr()); err != nil {
			return err
		}
		cliInfo(logging.Host, "updated mounts for space %s; start its boxes explicitly", space)
		return nil
	}
}

func controllerSpaceShell(cmd *cobra.Command, args []string) error {
	selected, remote, err := splitSpaceShellArgs(args, cmd.ArgsLenAtDash())
	if err != nil {
		return asUserError(err)
	}
	space, err := resolveSpacePositional(selected)
	if err != nil {
		return err
	}
	if len(remote) == 0 && !isInteractive(os.Stdin) {
		return asUserError(errors.New("space ssh needs a terminal; pass -- COMMAND instead"))
	}
	if start, _ := cmd.Flags().GetBool("start"); start {
		release, err := lockHostLifecycle(cmd, space)
		if err != nil {
			return err
		}
		defer release()
		boot, err := spaceconfig.LoadBootstrap(space)
		if err != nil {
			return err
		}
		if _, found, err := findSpaceInstance(cmd.Context(), space); err != nil {
			return err
		} else if !found {
			return errors.New("space VM is missing; run space start explicitly")
		}
		if err := startControllerSpace(cmd.Context(), boot, false, nil, cmd.ErrOrStderr()); err != nil {
			return err
		}
		release()
	} else {
		inst, found, err := findSpaceInstance(cmd.Context(), space)
		if err != nil {
			return err
		}
		if !found || inst.Status != "Running" {
			return asUserError(fmt.Errorf("space %s is not running; run space start %s", space, space))
		}
	}
	root, _ := cmd.Flags().GetBool("root")
	if root {
		if len(remote) == 0 {
			remote = []string{"sudo", "-i"}
		} else {
			remote = append([]string{"sudo"}, remote...)
		}
	}
	resumeSteps := suspendCLISteps()
	defer resumeSteps()
	sh := vmhost.InteractiveShell(cmd.Context(), space, remote)
	sh.Stdin, sh.Stdout = os.Stdin, os.Stdout
	return asPassthroughExitError(vmhost.RunWithFilteredStderr(sh, os.Stderr))
}

func guardOperatorCommands(root *cobra.Command) {
	for _, cmd := range root.Commands() {
		guardOperatorCommands(cmd)
	}
	if root.RunE == nil {
		return
	}
	run := root.RunE
	root.RunE = func(cmd *cobra.Command, args []string) error {
		operator, err := loadOperatorClient()
		if err != nil {
			return err
		}
		if operator != nil {
			method := commandMethod(cmd)
			allowed := len(cmd.Commands()) > 0 && len(args) == 0 || method == "version" ||
				method == "box" || len(method) >= 4 && method[:4] == "box." || operatorChatMethod(method)
			if method == "chat" {
				return asUserError(errors.New("the chat view uses the human identity; operators use chat ensure/status/enroll/revoke/connect/disconnect and their own enrolled AIRC client"))
			}
			if !allowed {
				return asUserError(errors.New("this box operator grants only yoloctl box commands and explicit managed chat administration in its current space"))
			}
			if cmd == cmd.Root() {
				return cmd.Help()
			}
		}
		return run(cmd, args)
	}
}
