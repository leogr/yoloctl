package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newClientRootCmd shares every public command definition with workers, then
// routes space-owned execution to the controller. Host lifecycle retains only
// its local boot record and selected VM adapter authority.
func newClientRootCmd() *cobra.Command {
	root := newRootCmd()
	root.Annotations = map[string]string{"controller-client": "true"}
	routeControllerCommands(root)
	space, _, _ := root.Find([]string{"space"})
	for _, action := range []string{"create", "start", "stop", "recreate", "destroy"} {
		cmd, _, _ := root.Find([]string{"space", action})
		cmd.RunE = hostControllerLifecycle(action)
		if action == "start" || action == "recreate" {
			cmd.Flags().String(installPreconditionFlag, "", "Installer consent snapshot")
			cmd.Flags().Int(installReceiptFlag, 0, "Private installer result descriptor")
			_ = cmd.Flags().MarkHidden(installPreconditionFlag)
			_ = cmd.Flags().MarkHidden(installReceiptFlag)
		}
		if action == "start" {
			cmd.Flags().Bool("update-controller", false, "Explicitly select this yoloctl build for the space controller and helpers")
			cmd.Flags().Bool("restart-sessions", false, "With --update-controller, restart without asking the boxes whose agent sessions still run the previous build; each agent must then be resumed explicitly")
		}
		if action == "recreate" {
			cmd.Flags().Bool("update-vm-image", false, "Prepare/select this build's Firecracker image and controller, or resume a pending image update")
			cmd.Flags().String("vm-image", "", "Replace the Firecracker VM using this verified image directory and the current controller build")
			cmd.Flags().Bool("rollback-vm-image", false, "Recover a pending update using its previous image, if compatible with this build; native data is not rolled back")
			cmd.MarkFlagsMutuallyExclusive("update-vm-image", "vm-image", "rollback-vm-image")
		}
	}
	space.AddCommand(newSpaceExportCmd(), newSpaceImportCmd(), newSpaceOperationCmd(), newSpaceViewCmd(), newSpaceStorageCmd(), newSpaceSetCmd(), newSpaceEditCmd())
	root.AddCommand(newChatCmd())
	show, _, _ := root.Find([]string{"space", "show"})
	show.RunE = controllerSpaceShow
	list, _, _ := root.Find([]string{"space", "list"})
	list.RunE = controllerSpaceList
	mountList, _, _ := root.Find([]string{"space", "mount", "list"})
	mountList.RunE = controllerMountList
	for _, action := range []string{"add", "remove"} {
		cmd, _, _ := root.Find([]string{"space", "mount", action})
		cmd.RunE = controllerMountChange(action)
	}
	shell, _, _ := root.Find([]string{"space", "ssh"})
	shell.RunE = controllerSpaceShell
	// Host-only commands (including import) are added after newRootCmd's
	// completion pass. Fill missing callbacks without replacing existing ones.
	registerCompletions(root)
	guardOperatorCommands(root)
	return root
}

func hostControllerLifecycle(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if operator, err := loadOperatorClient(); err != nil {
			return err
		} else if operator != nil {
			return asUserError(errors.New("box operator cannot administer spaces"))
		}
		space, err := resolveSpacePositional(args)
		if err != nil {
			return err
		}
		update, restartSessions := false, false
		if action == "start" {
			update, _ = cmd.Flags().GetBool("update-controller")
			restartSessions, _ = cmd.Flags().GetBool("restart-sessions")
			if restartSessions && !update {
				return asUserError(errors.New("--restart-sessions requires --update-controller"))
			}
		}
		if action == "start" && !hasInstallPrecondition(cmd) {
			// Compatibility may offer recreation before taking the lifecycle
			// lock. Refuse an unbootable target before that offer as well.
			boot, err := spaceconfig.LoadBootstrap(space)
			if err != nil {
				return err
			}
			if err := vmhost.ValidateInstancePaths(boot.VM.Backend, space); err != nil {
				return asUserError(err)
			}
			if err := ensureCommandVMCompatibility(cmd, space); err != nil {
				return err
			}
		}
		release, err := lockHostLifecycle(cmd, space)
		if err != nil {
			return err
		}
		defer release()
		if err := checkInstallPrecondition(cmd, space, action); err != nil {
			return err
		}
		if action == "create" {
			return createControllerSpace(cmd, space)
		}
		boot, err := spaceconfig.LoadBootstrap(space)
		if err != nil {
			return err
		}
		// Recheck the locked snapshot, after installer consent validation and
		// before either boot or recreate teardown. Cleanup remains available.
		if action == "start" || action == "recreate" {
			if err := vmhost.ValidateInstancePaths(boot.VM.Backend, space); err != nil {
				return asUserError(err)
			}
		}
		switch action {
		case "start":
			seed, err := loadControllerSeed(space)
			if err != nil {
				return err
			}
			if err := startControllerSpace(cmd.Context(), boot, update, seed, cmd.ErrOrStderr()); err != nil {
				var mismatch *spaceVMCompatibilityError
				if !errors.As(err, &mismatch) {
					return err
				}
				release() // approved recreation owns its own lifecycle lock
				return offerSpaceVMRecreate(cmd, boot, err)
			}
			if err := retireControllerSeed(space); err != nil || !update {
				return err
			}
			if err := writeInstallReceipt(cmd, space); err != nil {
				return err
			}
			// The prompt and the box restarts run outside the host lifecycle
			// lock; each restart takes the controller's own native locks.
			release()
			return offerSessionRestarts(cmd.Context(), space, hostSessionRestartOffer(cmd, boot), restartSessions)
		case "stop":
			if err := stopControllerSpace(cmd.Context(), boot, cmd.ErrOrStderr()); err != nil {
				return err
			}
			// A stopped VM is when a finished data disk grow is recorded.
			if err := settleDataDiskGrow(boot, cmd.OutOrStdout()); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "data disk grow not recorded yet: %v\n", err)
			}
			return nil
		case "recreate", "destroy":
			force, _ := cmd.Flags().GetBool("force")
			if !force {
				prompt := fmt.Sprintf("destroy space %s, its persistent disk, accounts and declarations? [y/N]: ", space)
				if action == "recreate" {
					prompt = fmt.Sprintf("recreate space %s? Declarations and accounts survive; sessions and VM-local workdirs are lost. [y/N]: ", space)
				}
				ok, err := promptYesNo(prompt, cmd.InOrStdin(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if !ok {
					return asUserError(errors.New(action + " declined"))
				}
			}
			if action == "recreate" {
				updateImage, _ := cmd.Flags().GetBool("update-vm-image")
				rollbackImage, _ := cmd.Flags().GetBool("rollback-vm-image")
				if updateImage || rollbackImage || cmd.Flags().Changed("vm-image") {
					if err := recreateSpaceImage(cmd, boot); err != nil {
						return err
					}
					return writeInstallReceipt(cmd, space)
				}
				if boot.ImageUpdate != nil {
					return pendingImageUpdate(boot)
				}
				identity, err := controller.ExecutableIdentity()
				if err != nil {
					return err
				}
				if identity != boot.Bundle && boot.VM.Backend == "firecracker" {
					return errors.New("controller build differs; use space start --update-controller before recreating")
				}
				if boot.DiskUUID == "" {
					return errors.New("initialization incomplete; run space start before recreating")
				}
				if _, err := controllerMounts(boot); err != nil {
					return err
				}
				if err := vmhost.New().RequireAccountDisk(cmd.Context(), space, boot.DiskID); err != nil {
					return err
				}
			}
			var replaced uint64 // the deleted VM's system disk, when known
			if action == "recreate" {
				if inst, found, err := findSpaceInstance(cmd.Context(), space); err == nil && found {
					replaced = inst.Disk
				}
			}
			if err := deleteSpaceRuntime(cmd.Context(), space, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if action == "destroy" {
				if err := vmhost.New().DeleteAccountDisk(cmd.Context(), boot.DiskID); err != nil {
					return fmt.Errorf("delete persistent disk (bootstrap retained for retry): %w", err)
				}
				return purgeSpaceBundle(space, cmd.ErrOrStderr())
			}
			if err := startControllerSpace(cmd.Context(), boot, boot.VM.Backend != "firecracker", nil, cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("replacement failed; persistent data retained; retry space start %s: %w", space, err)
			}
			if saved, err := spaceconfig.VMDiskBytes(boot.VM.Disk); err == nil && replaced != 0 && replaced != saved {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "system disk %d → %d GiB (the saved setting)\n", replaced/spaceconfig.GiB, saved/spaceconfig.GiB)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "recreated space %s; start boxes with box start --space %s\n", space, space)
			return writeInstallReceipt(cmd, space)
		}
		return errors.New("unknown space lifecycle action")
	}
}

func lockHostLifecycle(cmd *cobra.Command, space string) (func(), error) {
	acquire := spacelock.AcquireWithWait
	guarded, _ := cmd.Flags().GetString(installPreconditionFlag)
	if guarded != "" {
		acquire = spacelock.AcquireExistingWithWait
	}
	release, err := acquire(cmd.Context(), space, true, func() {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for space %s lifecycle operation; Ctrl-C cancels.\n", space)
	})
	if err != nil && guarded != "" {
		// An interrupted wait is an interruption, never "state changed, skipped".
		if ctxErr := cmd.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, skipped(err)
	}
	return release, err
}

func createControllerSpace(cmd *cobra.Command, space string) error {
	if exists, err := spaceconfig.Exists(space); err != nil {
		return err
	} else if exists {
		return asUserError(fmt.Errorf("space %s already exists; use space start, recreate or destroy", space))
	}
	workdir, _ := cmd.Flags().GetString("workdir")
	acct, _ := cmd.Flags().GetString("account")
	if err := account.ValidateName(acct); err != nil {
		return asUserError(err)
	}
	canonical, exists, err := canonicalizeMountArg(workdir)
	if err != nil {
		return err
	}
	legacy, err := spaceconfig.New(space, canonical, acct)
	if err != nil {
		return err
	}
	legacy.AccountDiskInitializing = true
	if err := rejectSpaceHostPath(canonical, legacy); err != nil {
		return err
	}
	boot, settings, err := spaceconfig.Split(legacy, os.Getuid(), os.Getgid(), rand.Text())
	if err != nil {
		return err
	}
	boot.VM.Backend, _ = cmd.Flags().GetString("backend")
	if err := vmhost.ValidateInstancePaths(boot.VM.Backend, space); err != nil {
		return asUserError(err)
	}
	boot.VM.Memory, err = selectSpaceMemory(cmd)
	if err != nil {
		return err
	}
	boot.VM.CPUs, err = selectSpaceCPUs(cmd, boot.VM.Backend)
	if err != nil {
		return err
	}
	boot.VM.Disk, err = selectSpaceDisk(cmd, boot.VM.Backend, space)
	if err != nil {
		return err
	}
	boot.VM.DataDisk, err = selectSpaceDataDisk(cmd)
	if err != nil {
		return err
	}
	// Refuse adapter bounds before image preparation or any disk effect. The
	// concrete values are saved; later starts never measure the host again.
	if err := vmapi.ValidateResources(boot.VM.Backend, vmapi.Resources{CPUs: boot.VM.CPUs, Memory: boot.VM.Memory}); err != nil {
		return asUserError(err)
	}
	boot.VM.Image, _ = cmd.Flags().GetString("vm-image")
	boot.VM.Image, err = vmhost.ResolveImage(cmd.Context(), boot.VM.Backend, boot.VM.Image)
	if err != nil {
		return err
	}
	if err := boot.Validate(); err != nil {
		return err
	}
	boot.Bundle, err = controller.ExecutableIdentity()
	if err != nil {
		return err
	}
	if _, found, err := vmhost.New().LookupBackend(cmd.Context(), space, boot.VM.Backend); err != nil {
		return err
	} else if found {
		return asUserError(errors.New("space has an orphan VM; recover or remove it before creating a new space"))
	}
	if !exists {
		if err := os.MkdirAll(canonical, 0o755); err != nil {
			return err
		}
	}
	raw, err := yaml.Marshal(account.New(space, acct))
	if err != nil {
		return err
	}
	seed := &controller.StoreBundle{Schema: 1, Settings: *settings, Accounts: map[string][]byte{acct: raw}, Boxes: map[string][]byte{}}
	if err := saveControllerSeed(space, seed); err != nil {
		return err
	}
	if err := boot.Save(); err != nil {
		return fmt.Errorf("bootstrap publication failed; initialization seed retained: %w", err)
	}
	// Publish ownership before allocating anything. An interrupted allocation
	// remains discoverable through this boot record, never an orphan disk.
	if err := vmhost.New().CreateAccountDisk(cmd.Context(), boot.DiskID); err != nil {
		return fmt.Errorf("allocate data disk (bootstrap retained; inspect before retrying): %w", err)
	}
	if err := startControllerSpace(cmd.Context(), boot, false, seed, cmd.ErrOrStderr()); err != nil {
		return err
	}
	if err := retireControllerSeed(space); err != nil {
		return err
	}
	noStart, _ := cmd.Flags().GetBool("no-start")
	if noStart {
		return stopControllerSpace(cmd.Context(), boot, cmd.ErrOrStderr())
	}
	return nil
}

func saveControllerSeed(space string, seed *controller.StoreBundle) error {
	dir, err := layout.SpaceBundleDir(space)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(seed)
	if err != nil {
		return err
	}
	return safepath.WriteFile(dir, ".initialize.json", raw, 0o600)
}

func loadControllerSeed(space string) (*controller.StoreBundle, error) {
	dir, err := layout.SpaceBundleDir(space)
	if err != nil {
		return nil, err
	}
	raw, err := safepath.ReadFile(dir, ".initialize.json", 64<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var seed controller.StoreBundle
	if err := decodeBoundedJSON(bytes.NewReader(raw), &seed, 64<<20); err != nil {
		return nil, err
	}
	return &seed, nil
}

func retireControllerSeed(space string) error {
	dir, err := layout.SpaceBundleDir(space)
	if err != nil {
		return err
	}
	for _, name := range []string{".initialize.json", "import-state.tar"} {
		if err := safepath.RemoveFile(dir, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func newSpaceOperationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "operation SPACE [ID]", Short: "Inspect recorded controller mutation outcomes", Args: cobra.RangeArgs(1, 2),
		Long: "Inspect recorded controller mutation outcomes from the host. No operation is ever replayed.\n\n" +
			"  yoloctl space operation SPACE --acknowledge-all   accept every finished unknown outcome at once\n" +
			"  yoloctl space operation SPACE                     list recorded operations\n" +
			"  yoloctl space operation SPACE ID                  inspect one\n" +
			"  yoloctl space operation SPACE ID --acknowledge    accept one unknown outcome\n\n" +
			"The controller resolves an interrupted operation by itself only from evidence: an effect gate that " +
			"never granted permission (recovered, not applied), a durable completion record (recovered with its " +
			"known result, success or failure), or a reviewed read-back of the exact account declaration or desired " +
			"chat revocation it was writing (recovered, applied). It never replays, finishes or undoes an operation, " +
			"and a timer never stands in for evidence. An outcome it cannot prove stays uncertain and holds only the " +
			"boxes, accounts and shared chat, image or port domains it recorded and operations depending on them; unrelated changes can proceed and reads " +
			"keep working. An outcome without a recorded footprint, from an older controller or another disk holds " +
			"the whole space. Acknowledging records the host's decision to release a hold: it neither retries nor " +
			"undoes the operation and does not mean it succeeded. --acknowledge-all accepts the uncertain and " +
			"command-uncertain outcomes recorded when it runs and reports their IDs and whether changes remain " +
			"paused. A pending operation may still be running and is never acknowledged. If receipt storage fails, " +
			"changes pause and acknowledgment is refused; the controller rechecks the storage and reopens changes " +
			"once every receipt is reconciled, without a restart.\n\n" +
			"A command-uncertain operation is a box exec command that may have run after it was authorized but " +
			"whose result was lost. Inspect its effects in the box; it holds nothing, and acknowledging it is " +
			"optional. Finished and recovered outcomes are kept among the 256 most recent records; pending and " +
			"uncertain ones are never evicted.",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 2 {
				id = args[1]
			}
			acknowledge, _ := cmd.Flags().GetBool("acknowledge")
			acknowledgeAll, _ := cmd.Flags().GetBool("acknowledge-all")
			switch {
			case acknowledgeAll && (acknowledge || id != ""):
				return asUserError(errors.New("--acknowledge-all takes no request ID and cannot be combined with --acknowledge"))
			case acknowledge && id == "":
				return asUserError(errors.New("--acknowledge requires one request ID"))
			}
			boot, err := checkControllerSpace(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(struct {
				ID string `json:"id"`
			}{id})
			req := controller.Request{ID: rand.Text(), Method: "control.status", Space: boot.Name, Dash: -1, Payload: payload}
			switch {
			case acknowledgeAll:
				req.Method, req.Payload = "control.acknowledge-all", nil
			case acknowledge:
				req.Method = "control.acknowledge"
			case id == "":
				req.Method = "control.receipts"
			}
			return callHostController(cmd.Context(), boot, req, controller.ClientIO{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()})
		},
	}
	cmd.Flags().Bool("acknowledge", false, "Accept an inspected uncertain or command-uncertain outcome; records the decision without retrying, undoing or calling it successful")
	cmd.Flags().Bool("acknowledge-all", false, "Accept every finished uncertain or command-uncertain outcome in one host decision; running operations are untouched and nothing is retried or undone")
	return cmd
}
