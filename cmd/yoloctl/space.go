package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	agentboxcfg "github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

const defaultSpaceName = config.DefaultSpaceName

func newSpaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "space",
		Aliases: []string{"sp"},
		Short:   "Manage yoloctl spaces (the VM isolation boundary)",
	}
	cmd.AddCommand(
		newSpaceCreateCmd(),
		newSpaceStartCmd(),
		newSpaceStopCmd(),
		newSpaceDestroyCmd(),
		newSpaceRecreateCmd(),
		newSpaceListCmd(),
		newSpaceShowCmd(),
		newSpaceUseCmd(),
		newSpaceShellCmd(),
		newSpaceLogsCmd(),
		newSpaceMountCmd(),
	)
	return cmd
}

func newSpaceLogsCmd() *cobra.Command {
	var (
		follow bool
		lines  int
	)
	cmd := &cobra.Command{
		Use:   "logs [SPACE]",
		Short: "Stream the VM boot/runtime log for a space",
		Long: `Tails the selected adapter's boot/runtime log. --follow keeps the tail
open as new lines arrive.

  yoloctl space logs
  yoloctl space logs default --follow
  yoloctl space logs prod-untrusted -n 500

Host-side Lima and interactive bridge diagnostics are captured separately in
logs/host.log under yoloctl's config root; they are not printed over live views.
It is kept under about 1 MiB, plus one previous file, host.log.1.

For guest service diagnostics, use 'space ssh --root'. Lima guests use
journalctl; Firecracker guests keep managed service logs under
/var/log/yoloctl-units and controller/runtime output in the VM log.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			return tailLimaLog(cmd.Context(), name, lines, follow,
				cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow new lines as they appear (tail -F)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "Number of trailing lines to print before following")
	return cmd
}

// tailLimaLog wraps `tail` on the Lima host-agent log file. We shell
// out to /usr/bin/tail rather than implementing the polling loop in Go
// because tail handles log rotation (Lima rotates ha.stdout.log on
// restart) and the streaming behavior the user expects.
func tailLimaLog(ctx context.Context, spaceName string, lines int, follow bool, stdout, stderr io.Writer) error {
	logPath, err := vmhost.LogPath(spaceName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			return asUserError(fmt.Errorf("no VM boot/runtime log at %s — is the space created?", logPath))
		}
		return err
	}
	tailArgs := []string{"-n", fmt.Sprintf("%d", lines)}
	if follow {
		tailArgs = append(tailArgs, "-F")
	}
	tailArgs = append(tailArgs, logPath)
	tail := exec.CommandContext(ctx, "tail", tailArgs...)
	tail.Stdout = stdout
	tail.Stderr = stderr
	return tail.Run()
}

func newSpaceCreateCmd() *cobra.Command {
	var workdir, initialAccount string
	var noStart bool
	cmd := &cobra.Command{
		Use:   "create [SPACE] --workdir PATH --account NAME",
		Short: "Create a space with a required workdir and initial account",
		Long: `Creates one VM, its controller, and a persistent native data disk. --workdir and
--account are required. The named empty account becomes this space's default
for new agent boxes. Import a login or authenticate inside a box afterward.
Only granted host projects and read-only catalogs are shared. Accounts and
box declarations stay on the native data disk.
--no-start initializes storage first, then leaves the VM stopped.
New space names use 1–16 ASCII characters (alphanumeric first, then ._-).
Existing longer names remain usable. New macOS Lima spaces use
~/.config/yoloctl/lima by default; existing spaces retain their saved/legacy
home. YOLOCTL_HOME and absolute XDG_CONFIG_HOME overrides remain explicit.
Lima refuses socket paths that exceed the platform limit; the 16-character
cap does not guarantee that every name fits under an arbitrarily long home.

Linux amd64 defaults to Firecracker with KVM; other platforms default to Lima.
Use --backend lima to select Lima explicitly. Firecracker prepares and caches
its VM image locally on first use; --vm-image is an optional override.
VM memory is chosen once from host capacity and available RAM (4–16 GiB),
then saved. Use --memory 8GiB for an explicit allocation.
VM CPUs default to every logical CPU this process may use, narrowed by
visible cgroup CPU bandwidth (rounded up) and the backend maximum, then saved.
This is not a reservation; VMs still share host CPUs. Use --cpus 4 explicitly.

  yoloctl space create work --workdir ~/code --account personal`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			if err := account.ValidateName(initialAccount); err != nil {
				return asUserError(fmt.Errorf("an initial account name is required: pass --account NAME: %w", err))
			}
			if strings.TrimSpace(workdir) == "" {
				return asUserError(errors.New("a default workdir is required: pass --workdir PATH"))
			}
			if exists, err := spaceconfig.Exists(name); err != nil {
				return err
			} else if exists {
				if _, err := spaceconfig.Load(name); err != nil {
					return err
				}
				return asUserError(fmt.Errorf("space %q already exists; use space start, space recreate or space destroy", name))
			}
			expanded, err := expandUserPath(workdir)
			if err != nil {
				return err
			}
			canonical, exists, err := agentboxcfg.CanonicalizeWorkdir(expanded)
			if err != nil {
				return err
			}
			cfg, err := spaceconfig.New(name, canonical, initialAccount)
			if err != nil {
				return err
			}
			if err := rejectSpaceHostPath(canonical, cfg); err != nil {
				return err
			}
			if !exists {
				if err := os.MkdirAll(canonical, 0755); err != nil {
					return err
				}
			}
			c := vmhost.New()
			if _, found, err := findSpaceInstance(cmd.Context(), name); err != nil {
				return err
			} else if found {
				return asUserError(fmt.Errorf("space %q has a VM without a declaration; restore its bundle or remove the orphan VM before creating a new space", name))
			}
			if err := c.CreateAccountDisk(cmd.Context(), cfg.AccountDiskID); err != nil {
				return fmt.Errorf("allocate new account disk (no declaration saved; retry space create): %w", err)
			}
			cfg.AccountDiskInitializing = true
			if err := cfg.Save(); err != nil {
				return errors.Join(err, c.DeleteAccountDisk(cmd.Context(), cfg.AccountDiskID))
			}
			if err := account.New(name, initialAccount).Save(); err != nil {
				return err
			}
			mounts, err := spaceWritableMounts(name)
			if err != nil {
				return err
			}
			template := vmhost.TemplateWithAccountDisk(mounts, cfg.AccountDiskID, true)
			if err := c.Create(cmd.Context(), name, template); err != nil {
				return err
			}
			if err := c.Start(cmd.Context(), name); err != nil {
				return err
			}
			if err := prepareSpaceAccounts(cmd.Context(), cfg); err != nil {
				return err
			}
			if noStart {
				return stopSpaceRuntime(cmd.Context(), name, cmd.ErrOrStderr())
			}
			return nil
		}}
	cmd.Flags().String("backend", vmhost.DefaultBackend(), "VM backend: lima or firecracker (Linux amd64 with KVM)")
	cmd.Flags().String("vm-image", "", "Optional prepared Firecracker image override (otherwise built locally on first use)")
	cmd.Flags().String("memory", "auto", "VM RAM: auto (host-aware 4–16 GiB), or an explicit size such as 8GiB")
	cmd.Flags().String("cpus", "auto", "VM CPUs: auto (available host CPUs, backend maximum), or an explicit count such as 4")
	cmd.Flags().String("disk", "auto", diskFlagHelp)
	cmd.Flags().String("data-disk", "10GiB", dataDiskFlagHelp)
	cmd.Flags().StringVar(&workdir, "workdir", "", "Required default host project directory")
	cmd.Flags().StringVar(&initialAccount, "account", "", "Required initial account name (also the space default)")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "Initialize the account disk, then leave the VM stopped")
	_ = cmd.MarkFlagRequired("account")
	_ = cmd.MarkFlagRequired("workdir")
	return cmd
}

func newSpaceMountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Manage a space's host→VM mount grants",
		Long: `A space owns the host paths shared into its VM. These commands list, add,
and remove those grants.

Changing a running VM's grants requires a restart, so add/remove
automatically ask to restart a running space before changing anything.
Declining the restart leaves both the space config and host filesystem
unchanged. Add/remove require a running controller to validate dependent
boxes; start a stopped space explicitly first. A VM restart stops boxes and
their agent sessions; box declarations are preserved for a later start or
attach.`,
	}
	cmd.AddCommand(newSpaceMountListCmd(), newSpaceMountAddCmd(), newSpaceMountRemoveCmd())
	return cmd
}

func newSpaceMountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [SPACE]",
		Short: "List a space's host→VM mount grants",
		Long: `Prints the space's host→VM mount grants from its host bootstrap.
Native account storage is an independent VM disk, not a host mount.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			scfg, err := spaceconfig.Load(name)
			if errors.Is(err, fs.ErrNotExist) {
				return asUserError(fmt.Errorf(
					"space %q is not configured — create it: yoloctl space create %s --workdir PATH --account NAME", name, name))
			}
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "Default workdir: %s\n", scfg.DefaultWorkdir)
			_, _ = fmt.Fprintln(w, "Mounts:")
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "  SOURCE\tACCESS\tKIND")
			for _, m := range scfg.Mounts {
				_, _ = fmt.Fprintf(tw, "  %s\t%s\tgrant\n", m.Source, accessLabel(m.Writable))
			}
			return tw.Flush()
		},
	}
}

func accessLabel(writable bool) string {
	if writable {
		return "rw"
	}
	return "ro"
}

func newSpaceMountAddCmd() *cobra.Command {
	var (
		readOnly bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "add SPACE PATH",
		Short: "Grant a host path as a mount in a space",
		Long: `Adds a host→VM mount grant to the space (writable by default;
--read-only for a read-only share). The host path is created if it doesn't
exist. The space controller must already be running to validate dependencies.
yoloctl asks to restart the space and applies the grant immediately; declining
leaves everything unchanged. Start boxes explicitly after the restart.

  yoloctl space mount add default ~/datasets
  yoloctl space mount add prod /srv/readonly --read-only
  yoloctl space mount add default ~/scratch --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSpaceName(args[0]); err != nil {
				return err
			}
			name := args[0]
			canonical, exists, err := canonicalizeMountArg(args[1])
			if err != nil {
				return err
			}
			scfg, err := loadSpaceForMount(name)
			if err != nil {
				return err
			}
			if err := rejectSpaceHostPath(canonical, scfg); err != nil {
				return err
			}
			if !scfg.AddMount(canonical, !readOnly) {
				cliInfo(logging.Host, "%s is already granted to space %s", canonical, name)
				return nil
			}
			if err := scfg.Validate(); err != nil {
				return asUserError(fmt.Errorf("cannot add mount: %w", err))
			}
			restart, err := confirmSpaceMountRestart(cmd, name, yes)
			if err != nil {
				return err
			}
			if !exists {
				if err := os.MkdirAll(canonical, 0o755); err != nil {
					return asUserError(fmt.Errorf("create %s: %w", canonical, err))
				}
				cliNotice(logging.Host, "created %s", canonical)
			}
			if err := scfg.Save(); err != nil {
				return fmt.Errorf("save space config: %w", err)
			}
			cliInfo(logging.Host, "granted %s (%s) to space %s", canonical, accessLabel(!readOnly), name)
			return applyMountChange(cmd, name, restart)
		},
	}
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Grant a read-only share (default: writable)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Restart a running space without confirmation")
	return cmd
}

func newSpaceMountRemoveCmd() *cobra.Command {
	var (
		yes bool
	)
	cmd := &cobra.Command{
		Use:     "remove SPACE PATH",
		Aliases: []string{"rm"},
		Short:   "Remove a host→VM mount grant from a space",
		Long: `Removes a mount grant (matched by its canonical path). The grant
covering the space's default workdir cannot be removed unless another grant
still covers that workdir. The space controller must already be running to
validate dependencies. Removal is refused while a declared box still needs
that grant; change its workdir or mounts first. yoloctl asks to restart the
space and applies the removal immediately; declining leaves everything
unchanged. Start boxes explicitly after the restart.

  yoloctl space mount remove default ~/datasets
  yoloctl space mount remove default ~/scratch --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSpaceName(args[0]); err != nil {
				return err
			}
			name := args[0]
			canonical, _, err := canonicalizeMountArg(args[1])
			if err != nil {
				return err
			}
			scfg, err := loadSpaceForMount(name)
			if err != nil {
				return err
			}
			if !scfg.RemoveMount(canonical) {
				return asUserError(fmt.Errorf("%s is not a mount grant of space %s", canonical, name))
			}
			if err := scfg.Validate(); err != nil {
				return asUserError(fmt.Errorf("cannot remove mount: %w", err))
			}
			restart, err := confirmSpaceMountRestart(cmd, name, yes)
			if err != nil {
				return err
			}
			if err := scfg.Save(); err != nil {
				return fmt.Errorf("save space config: %w", err)
			}
			cliInfo(logging.Host, "removed grant %s from space %s", canonical, name)
			return applyMountChange(cmd, name, restart)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Restart a running space without confirmation")
	return cmd
}

// loadSpaceForMount loads a space config for a mount mutation, mapping a
// missing space to a clear "create it first" user-error.
func loadSpaceForMount(name string) (*spaceconfig.Config, error) {
	scfg, err := spaceconfig.Load(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, asUserError(fmt.Errorf(
			"space %q is not configured — create it: yoloctl space create %s --workdir PATH --account NAME", name, name))
	}
	if err != nil {
		return nil, err
	}
	return scfg, nil
}

// canonicalizeMountArg expands + canonicalizes a mount PATH argument without
// changing the host filesystem. The caller creates a missing source only
// after any required restart confirmation, so declining a mount add is a
// no-op. Mount remove never materializes host directories.
func canonicalizeMountArg(arg string) (string, bool, error) {
	expanded, err := expandUserPath(arg)
	if err != nil {
		return "", false, asUserError(err)
	}
	canonical, exists, err := agentboxcfg.CanonicalizeWorkdir(expanded)
	if err != nil {
		return "", false, asUserError(err)
	}
	return canonical, exists, nil
}

var (
	spaceMountFindInstance = findSpaceInstance
	spaceMountListBoxes    = func(ctx context.Context, name string) ([]container.Container, error) {
		return container.New(name).List(ctx, agentboxcfg.LabelAgentbox)
	}
	spaceMountStop = func(ctx context.Context, name string) error {
		return stopSpaceRuntime(ctx, name, os.Stderr)
	}
	spaceMountStart = func(ctx context.Context, name string) error {
		return ensureSpaceRunning(ctx, name)
	}
	spaceMountRefresh = refreshLimaConfig
)

// confirmSpaceMountRestart determines whether a mount mutation needs a VM
// restart and, when it does, obtains confirmation BEFORE the caller changes
// the config or host filesystem. --yes is the explicit non-interactive path.
func confirmSpaceMountRestart(cmd *cobra.Command, name string, yes bool) (bool, error) {
	ctx := cmd.Context()
	inst, found, err := spaceMountFindInstance(ctx, name)
	if err != nil {
		return false, err
	}
	if !found || inst.Status != "Running" {
		return false, nil
	}

	if affected, lerr := spaceMountListBoxes(ctx, name); lerr == nil {
		running := make([]container.Container, 0, len(affected))
		for _, box := range affected {
			if containerStatusRunning(box.Status) {
				running = append(running, box)
			}
		}
		if len(running) > 0 {
			cliInfo(logging.Host, "restarting space %s will stop %d running box(es) and terminate their agent sessions; declarations are preserved:", name, len(running))
			for _, box := range running {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  - %s (%s)\n", box.Name, box.Status)
			}
		}
	}
	if yes {
		return true, nil
	}
	ok, err := promptYesNo(
		fmt.Sprintf("restart space %s and apply the mount change? [y/N]: ", name),
		cmd.InOrStdin(), cmd.ErrOrStderr())
	if err != nil {
		return false, err
	}
	if !ok {
		return false, asUserError(errors.New("mount change declined; nothing changed"))
	}
	return true, nil
}

// applyMountChange makes an already-approved and persisted mount mutation
// live. A running VM is restarted immediately. For a stopped or absent VM,
// only its stored Lima config needs refreshing; the mount set is naturally
// applied on the next start.
func applyMountChange(cmd *cobra.Command, name string, restart bool) error {
	ctx := cmd.Context()
	if !restart {
		if err := spaceMountRefresh(name); err != nil {
			return err
		}
		cliNotice(logging.Host, "space %s is not running; the mount change will be live on its next start", name)
		return nil
	}
	cliInfo(logging.Host, "restarting space %s...", name)
	if err := spaceMountStop(ctx, name); err != nil {
		return err
	}
	if err := spaceMountRefresh(name); err != nil {
		return err
	}
	return spaceMountStart(ctx, name)
}

func newSpaceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start [SPACE]",
		Short: "Start a yoloctl space (recreating the VM from its bundle if needed)",
		Long: `Boots the space's VM, first refreshing its host grants from
the space config (so a grant added since the last boot takes effect). If the
VM is missing but its bootstrap and data disk remain, it is recreated from
space.yaml. The controller must match this CLI build; select a new build
explicitly with --update-controller. Agent sessions started by the previous
build keep running, but this build cannot attach to them; after the update,
start lists those boxes and offers to restart them (default No; scripts only
get the commands, --restart-sessions restarts without asking). A restart ends
each agent's process; resume its conversation explicitly. A restarted chat
server or session_command workload is started again. Box/account commands never boot the VM. 'space destroy' deletes both the bundle and account disk; create
a new space afterward with 'space create --workdir PATH --account NAME'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			// ensureSpaceRunning requires the bundle (space.yaml), refreshes
			// mounts, and uses vmhost.Ensure — create-if-missing, start-if-
			// stopped, no-op-if-running — so it recreates a VM dropped by a
			// runtime-only destroy.
			return ensureSpaceRunning(cmd.Context(), name)
		},
	}
}

// spaceWritableMounts renders only the space's declared host grants.
// Account state lives on its guest disk and never widens host mount authority.
func spaceWritableMounts(spaceName string) ([]vmhost.Mount, error) {
	cfg, err := spaceconfig.Load(spaceName)
	if err != nil {
		return nil, err
	}
	var mounts []vmhost.Mount
	for _, m := range cfg.Mounts {
		if err := rejectExistingSpaceHostPath(m.Source, cfg); err != nil {
			return nil, err
		}
		mounts = append(mounts, vmhost.Mount{Location: m.Source, Writable: m.Writable})
	}
	return dedupeSortMounts(mounts), nil
}

// dedupeSortMounts sorts mounts by location and collapses duplicate locations,
// OR-ing their writable flags (a writable grant subsumes a read-only one at
// the same path — e.g. a user grant coinciding with the managed root). Returns
// nil for empty input.
func dedupeSortMounts(in []vmhost.Mount) []vmhost.Mount {
	if len(in) == 0 {
		return nil
	}
	writable := make(map[string]bool, len(in))
	order := make([]string, 0, len(in))
	for _, m := range in {
		if m.Location == "" {
			continue
		}
		if w, ok := writable[m.Location]; ok {
			writable[m.Location] = w || m.Writable
			continue
		}
		writable[m.Location] = m.Writable
		order = append(order, m.Location)
	}
	sort.Strings(order)
	out := make([]vmhost.Mount, 0, len(order))
	for _, loc := range order {
		out = append(out, vmhost.Mount{Location: loc, Writable: writable[loc]})
	}
	return out
}

// effectiveSpaceConfig is the host grant authority; guest account storage is
// validated independently against the space's persistent disk identity.
func effectiveSpaceConfig(spaceName string) (*spaceconfig.Config, error) {
	return spaceconfig.Load(spaceName)
}

// buildLimaTemplateForSpace renders the full Lima YAML template for env
// creation, with the writable mounts spliced in. Use this only at create
// time: the template selects the pinned guest images during Create.
// On already-created envs, prefer SetInstanceMounts (which
// preserves the resolved fields and rewrites only the mounts section).
func buildLimaTemplateForSpace(spaceName string) ([]byte, error) {
	mounts, err := spaceWritableMounts(spaceName)
	if err != nil {
		return nil, err
	}
	cfg, err := spaceconfig.Load(spaceName)
	if err != nil {
		return nil, err
	}
	return vmhost.TemplateWithAccountDisk(mounts, cfg.AccountDiskID, false), nil
}

// refreshLimaConfig rewrites only the `mounts:` section of an existing
// space's active lima.yaml from its persistent config, so a grant added since the last boot is picked up at next Start.
// Silently no-ops if the space dir doesn't exist yet (Create writes the
// initial config from buildLimaTemplateForSpace).
func refreshLimaConfig(spaceName string) error {
	mounts, err := spaceWritableMounts(spaceName)
	if err != nil {
		return err
	}
	if err := vmhost.New().SetInstanceMounts(spaceName, mounts); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ensureSpaceRunning makes the named space's VM exist and be running, using
// the mounts from its persistent config. It does
// NOT create or mutate the space's mount set — that is `space create` /
// `space mount`'s job (the "space owns Lima mounts" model). A space with no
// config is a hard error pointing at `space create --workdir --account`: a space must
// have an explicit default workdir, which create/run never invent.
//
// This is the entry point `box create` uses before
// interacting with the VM. It never seeds per-agentbox mounts.
func ensureSpaceRunning(ctx context.Context, name string) error {
	if spaceenv.Native() {
		if err := spaceenv.RequireSpace(name); err != nil {
			return err
		}
		cfg, err := spaceconfig.Load(name)
		if err != nil {
			return err
		}
		return prepareSpaceAccounts(ctx, cfg)
	}
	cfg, err := spaceconfig.Load(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("space %q has no declaration; create a new space with space create --workdir PATH --account NAME, or restore its bundle before starting an existing VM: %w", name, err)
		}
		return fmt.Errorf("load space %q: %w", name, err)
	}
	c := vmhost.New()
	if err := c.RequireAccountDisk(ctx, name, cfg.AccountDiskID); err != nil {
		return err
	}

	inst, found, err := findSpaceInstance(ctx, name)
	if err != nil {
		return err
	}
	// No configuration rewrites or account-wide directory creation on reuse.
	// The native prepare still verifies ext4, UUID, marker, and live bind.
	if found && inst.Status == "Running" {
		return prepareSpaceAccounts(ctx, cfg)
	}
	format := false
	if cfg.AccountDiskInitializing && cfg.AccountDiskUUID == "" {
		if found && inst.Status != "Stopped" {
			return fmt.Errorf("space %q must be stopped before resuming account disk initialization", name)
		}
		format, err = c.AccountDiskBlank(ctx, cfg.AccountDiskID)
		if err != nil {
			return fmt.Errorf("check pending account disk initialization: %w", err)
		}
	}
	if err := refreshLimaConfig(name); err != nil {
		return err
	}
	if err := c.SetAccountDisk(name, cfg.AccountDiskID, format); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mounts, err := spaceWritableMounts(name)
	if err != nil {
		return err
	}
	if !found {
		if err := c.Create(ctx, name, vmhost.TemplateWithAccountDisk(mounts, cfg.AccountDiskID, format)); err != nil {
			return err
		}
	}
	if err := c.Start(ctx, name); err != nil {
		return err
	}
	return prepareSpaceAccounts(ctx, cfg)
}

func prepareSpaceAccounts(ctx context.Context, cfg *spaceconfig.Config) error {
	action := "prepare"
	if cfg.AccountDiskUUID == "" {
		action = "initialize"
	}
	result, err := accountStateOperation(ctx, cfg.Name, accountstate.Request{Action: action})
	if err != nil {
		return err
	}

	if cfg.AccountDiskUUID == "" {
		if result.UUID == "" {
			return errors.New("account disk did not report a filesystem identity")
		}
		// Revoke Lima's formatting permission before publishing initialization.
		if err := vmhost.New().SetAccountDisk(cfg.Name, cfg.AccountDiskID, false); err != nil {
			return err
		}
		cfg.AccountDiskUUID = result.UUID
		cfg.AccountDiskInitializing = false
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	// Native directories are created lazily for the selected account/agent.
	return nil
}

// bindGrantStatus is classifyBindGrant's verdict from the space config ALONE
// (the source of truth), decided before any VM probe.
type bindGrantStatus int

const (
	bindNotGranted       bindGrantStatus = iota // no grant covers the path
	bindReadOnlyConflict                        // a grant covers it, but read-only while the bind needs write
	bindGrantOK                                 // a compatible grant covers it (liveness still to be probed)
)

// classifyBindGrant decides, from the space config alone, whether path may be
// bound: not granted, granted read-only while the bind needs write, or OK
// (subject to a separate VM liveness probe). It is PURE — no VM I/O — so the
// grant-authority logic is unit-tested directly; only the live `test -w/-r`
// probe in ensureBindGranted needs a real VM. The most-specific grant governs
// (spaceconfig.GrantFor), so a writable child grant nested under a read-only
// parent is honored.
func classifyBindGrant(spacecfg *spaceconfig.Config, path string, writable bool) bindGrantStatus {
	if spacecfg == nil {
		return bindNotGranted
	}
	grant, ok := spacecfg.GrantFor(path)
	if !ok {
		return bindNotGranted
	}
	if writable && !grant.Writable {
		return bindReadOnlyConflict
	}
	return bindGrantOK
}

// ensureBindGranted verifies that host path — a source an agentbox is about to
// bind into its container — is allowed by the space, then that it's live. It
// NEVER mutates mounts; `space create` / `space mount` own the Lima set.
//
// Grant AUTHORITY is checked FIRST, against the space config (the sole source
// of truth for what an agentbox may bind). A path that merely happens to be
// live in the VM — e.g. a grant removed from the config but not yet unmounted,
// since Lima can't hot-unmount — must NOT slip through: it would vanish on the
// next space restart, leaving a broken agentbox. So config decides; the VM
// probe only confirms a granted, compatible path is actually live yet.
//
// `writable` selects both the read-only-conflict check and the liveness probe
// (test -w vs test -r), so a read-only extra_mount of an unwritable source
// still passes. The liveness probe targets the covering GRANT's source — the
// path Lima actually mounts — not the bind path itself: a bind may point at a
// not-yet-created subdir of a live mount (the lazily-created account-state
// leaf is the standing case), and that must not read as "mount not live".
// On failure it returns a user-error: not granted → `space mount add ...`
// (which confirms and restarts a running space); a writable bind under a
// read-only grant → re-grant it writable; granted but not live → restart the
// space. Either way nothing has been persisted yet — the caller runs this
// BEFORE saving the agentbox YAML or bridging credentials, so a missing mount
// never leaves a partial agentbox.
func ensureBindGranted(ctx context.Context, spaceName string, spacecfg *spaceconfig.Config, path string, writable bool, label string) error {
	switch classifyBindGrant(spacecfg, path, writable) {
	case bindNotGranted:
		return asUserError(fmt.Errorf(
			"%s (%s) is not under any mount grant for space %q — yoloctl shares only granted host paths into the VM.\n"+
				"  grant it (a running space will confirm and restart), then retry:\n"+
				"    yoloctl space mount add %s %s",
			label, path, spaceName, spaceName, path))
	case bindReadOnlyConflict:
		return asUserError(fmt.Errorf(
			"%s (%s) needs write access but its space %q mount grant is read-only.\n"+
				"  re-grant it writable (a running space will confirm and restart), then retry:\n"+
				"    yoloctl space mount add %s %s",
			label, path, spaceName, spaceName, path))
	}
	// Granted and compatible — confirm the covering MOUNT is actually live in
	// the VM (probe the grant source, which is what Lima mounts).
	probe := path
	if grant, ok := spacecfg.GrantFor(path); ok {
		probe = grant.Source
	}
	flag := "-r"
	if writable {
		flag = "-w"
	}
	if vmhost.ShellCommand(ctx, spaceName, "test", flag, probe).Run() == nil {
		return nil
	}
	return asUserError(fmt.Errorf(
		"%s (%s) is granted to space %q but its mount (%s) is not live yet — the VM needs a restart to apply mount changes.\n"+
			"  restart the space to apply the grant, then retry:\n"+
			"    yoloctl space stop %s && yoloctl space start %s",
		label, path, spaceName, probe, spaceName, spaceName))
}

func newSpaceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [SPACE]",
		Short: "Stop a yoloctl space",
		Long: `Stops the VM that backs the space. Running boxes are
shut down with the VM, terminating their agent sessions. Their declarations
and container state survive; start or attach them after the space starts.

  yoloctl space stop                  # stops the current-context space
  yoloctl space stop prod-untrusted`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			return stopSpaceRuntime(cmd.Context(), name, cmd.ErrOrStderr())
		},
	}
}

func newSpaceDestroyCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "destroy [SPACE]", Short: "Delete a space, its accounts and declarations",
		Long: `Deletes the VM, persistent account disk and space bundle, including all
account secrets and native agent state. Host workdirs and other spaces survive.
Use space recreate for a fresh VM that preserves accounts and declarations.
--force skips confirmation; teardown still attempts graceful shutdown first.`,
		Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			cfg, err := spaceconfig.Load(name)
			if errors.Is(err, fs.ErrNotExist) {
				home, homeErr := spaceconfig.LimaHome(name)
				if homeErr != nil {
					return homeErr
				}
				return asUserError(fmt.Errorf("space %q has no declaration; restore its bundle to manage its account disk, or remove only the orphan VM with LIMA_HOME=%s limactl delete %s after stopping it (account disks are preserved)", name, shellQuote(home), vmhost.InstanceName(name)))
			}
			if err != nil {
				return err
			}
			if !force {
				ok, err := promptYesNo(fmt.Sprintf("destroy space %s and ALL its accounts and declarations? Use space recreate to preserve them. [y/N]: ", name), cmd.InOrStdin(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if !ok {
					return asUserError(errors.New("destroy declined"))
				}
			}
			if err := deleteSpaceRuntime(cmd.Context(), name, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if err := vmhost.New().DeleteAccountDisk(cmd.Context(), cfg.AccountDiskID); err != nil {
				return fmt.Errorf("delete account disk (space declarations kept for retry): %w", err)
			}
			return purgeSpaceBundle(name, cmd.ErrOrStderr())
		}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation (deletes this space's accounts)")
	return cmd
}

func newSpaceRecreateCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "recreate [SPACE]", Short: "Replace and start the VM, preserving accounts and declarations",
		Long: `Stops space workloads, deletes its system disk, creates and starts a new
VM from its host bootstrap, and reattaches the preserved data disk. Box declarations
and host workdirs survive; ephemeral workdirs, container contents, images and
sessions do not. Start boxes explicitly afterward. --force skips confirmation.`,
		Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			cfg, err := spaceconfig.Load(name)
			if err != nil {
				return err
			}
			if _, err := buildLimaTemplateForSpace(name); err != nil {
				return err
			}
			if err := vmhost.New().RequireAccountDisk(cmd.Context(), name, cfg.AccountDiskID); err != nil {
				return err
			}
			if cfg.AccountDiskUUID == "" {
				return errors.New("space account disk initialization is incomplete; start the existing space first")
			}
			if inst, found, err := spaceDestroyFindInstance(cmd.Context(), name); err != nil {
				return err
			} else if found && inst.Status == "Running" {
				if _, err := accountStateOperation(cmd.Context(), name, accountstate.Request{Action: "verify"}); err != nil {
					return fmt.Errorf("verify account disk before replacing VM: %w", err)
				}
			}
			if !force {
				ok, err := promptYesNo(fmt.Sprintf("recreate space %s? Accounts and manifests survive; VM-local workdirs and sessions are lost. [y/N]: ", name), cmd.InOrStdin(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if !ok {
					return asUserError(errors.New("recreate declined"))
				}
			}
			if err := deleteSpaceRuntime(cmd.Context(), name, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if err := ensureSpaceRunning(cmd.Context(), name); err != nil {
				return fmt.Errorf("replacement failed; accounts and declarations retained; retry space start %s: %w", name, err)
			}
			cliInfo(logging.Host, "recreated space %s; start its boxes with box start --space %s", name, name)
			return nil
		}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	return cmd
}

var (
	spaceDestroyFindInstance = findSpaceInstance
	spaceDestroyDelete       = func(ctx context.Context, name string, force bool) error {
		return vmhost.New().Delete(ctx, name, force)
	}
)

func purgeSpaceBundle(name string, stderr io.Writer) error {
	bundle, err := layout.SpaceBundleDir(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(bundle); err != nil {
		return fmt.Errorf("remove space bundle %s (retry space destroy): %w", bundle, err)
	} else {
		cliInfo(logging.Host, "purged space bundle %s", bundle)
	}
	// Clear the context pin if it points at the removed space, so a later
	// no-flag command doesn't resolve to a space that no longer exists.
	if ctx, err := config.LoadContext(); err == nil && ctx.Space == name {
		ctx.Space = ""
		if err := ctx.Save(); err != nil {
			return fmt.Errorf("space removed, but clearing context pin failed: %w", err)
		} else {
			cliInfo(logging.Host, "cleared context pin (was %s)", name)
		}
	}
	return nil
}

func newSpaceShellCmd() *cobra.Command {
	var (
		start bool
		root  bool
	)
	cmd := &cobra.Command{
		Use:     "ssh [SPACE] [-- CMD [ARGS...]]",
		Aliases: []string{"shell"},
		Short:   "Open a shell in a space's VM (VM-level access, outside the OpenShell sandbox)",
		Long: `Connects to the VM that backs a space and opens a login shell —
the escape hatch for inspecting the VM, debugging provisioning, and
checking mount and container state.

This is VM-level access OUTSIDE the OpenShell sandbox, for human/admin
debugging. It is NOT a way to launch agents: agents must run through
the sandbox (see 'yoloctl box create' and 'yoloctl box exec').

With no CMD it opens an interactive shell (requires a terminal on
stdin). With '-- CMD [ARGS...]' it runs CMD in the VM and propagates
its stdout / stderr and exit status (works without a TTY). --root runs
the remote side as VM root via sudo.

The space must already exist and be running — this never creates a VM.
Pass --start to refresh the space's mounts and boot a stopped space before
connecting.

  yoloctl space ssh                       # current-context space, interactive
  yoloctl space ssh prod-untrusted
  yoloctl space ssh -- uname -a           # one-shot command
  yoloctl space ssh default --root -- journalctl -u containerd
  yoloctl space ssh stopped-space --start`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spaceArg, remote, err := splitSpaceShellArgs(args, cmd.ArgsLenAtDash())
			if err != nil {
				return asUserError(err)
			}
			name, err := resolveSpacePositional(spaceArg)
			if err != nil {
				return err
			}
			// An interactive shell needs a real terminal; a piped/redirected
			// stdin would just hang. Command mode is fine without one.
			if len(remote) == 0 && !isInteractive(os.Stdin) {
				return asUserError(fmt.Errorf(
					"space ssh needs a terminal for an interactive shell; " +
						"pass '-- CMD [ARGS...]' to run a command instead"))
			}
			if err := ensureSpaceRunningForShell(cmd.Context(), name, start, cmd.ErrOrStderr()); err != nil {
				return err
			}
			// limactl/ssh allocate a remote PTY automatically when stdin is a
			// terminal, so direct stdio wiring (as ssh-proxy does) gives a
			// correct interactive shell and a clean command-mode pipe alike.
			if err := releaseSpaceOperation(cmd.Context()); err != nil {
				return err
			}
			sh := vmhost.CommandForSpace(cmd.Context(), name, spaceShellLimaArgs(name, root, remote)...)
			sh.Stdin = os.Stdin
			sh.Stdout = os.Stdout
			return asPassthroughExitError(vmhost.RunWithFilteredStderr(sh, os.Stderr))
		},
	}
	cmd.Flags().BoolVar(&start, "start", false, "Refresh mounts and start the space first if it is stopped (never creates)")
	cmd.Flags().BoolVar(&root, "root", false, "Run the remote side as VM root via sudo (admin access, outside the sandbox)")
	return cmd
}

// splitSpaceShellArgs separates the optional SPACE positional from the
// remote command for `space ssh`. dash is cmd.ArgsLenAtDash(): -1 when no
// `--` terminator was given, else the count of positionals before it.
// Without `--`, every arg is the (at most one) SPACE positional; with
// `--`, args before the dash are the SPACE positional and args after are
// the command to run in the VM.
func splitSpaceShellArgs(args []string, dash int) (spaceArg, remote []string, err error) {
	if dash < 0 {
		spaceArg = args
	} else {
		spaceArg = args[:dash]
		remote = args[dash:]
	}
	if len(spaceArg) > 1 {
		return nil, nil, fmt.Errorf(
			"got multiple arguments before '--' (%s); to run a command in the VM, "+
				"separate it with '--': yoloctl space ssh [SPACE] -- CMD [ARGS...]",
			strings.Join(spaceArg, " "))
	}
	return spaceArg, remote, nil
}

// spaceShellLimaArgs builds the limactl argv for `space ssh`. With no
// remote command it omits the trailing `--`, so limactl opens an
// interactive login shell; with one, the command runs (limactl/ssh
// allocate a PTY only when stdin is a terminal). --root wraps the remote
// side in sudo: `sudo -i` for an interactive root login shell, `sudo
// CMD...` for a one-shot.
func spaceShellLimaArgs(space string, root bool, remote []string) []string {
	args := []string{"shell", vmhost.InstanceName(space)}
	var tail []string
	switch {
	case root && len(remote) == 0:
		tail = []string{"sudo", "-i"}
	case root:
		tail = append([]string{"sudo"}, remote...)
	default:
		tail = remote
	}
	if len(tail) > 0 {
		args = append(args, "--")
		args = append(args, tail...)
	}
	return args
}

// ensureSpaceRunningForShell verifies the named space exists and is
// running before `space ssh` connects. It never creates a VM (PLAN
// missing space is a hard error. A stopped space errors with a
// `space start` hint unless start=true, in which case its mounts are
// refreshed and it is booted first (mirrors `space start`).
func ensureSpaceRunningForShell(ctx context.Context, name string, start bool, stderr io.Writer) error {
	inst, found, err := findSpaceInstance(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return asUserError(fmt.Errorf(
			"space %s not found — create it with: yoloctl space create %s", name, name))
	}
	if inst.Status == "Running" {
		return nil
	}
	if !start {
		return asUserError(fmt.Errorf(
			"space %s is not running (status: %s) — start it with: "+
				"yoloctl space start %s  (or pass --start)", name, inst.Status, name))
	}
	cliInfo(logging.Host, "starting space %s...", name)
	if err := refreshLimaConfig(name); err != nil {
		return err
	}
	return ensureSpaceRunning(ctx, name)
}

// findSpaceInstance uses the persisted adapter and checks for foreign metadata.
func findSpaceInstance(ctx context.Context, name string) (vmhost.Instance, bool, error) {
	return vmhost.New().Lookup(ctx, name)
}

// spaceShowWorkdir groups a space's configured boxes by their shared workdir.
// Workdir is the canonical host path for host-mode boxes, or an ephemeral
// marker for VM-local ones.
type spaceShowWorkdir struct {
	Workdir    string
	Mode       string
	Agentboxes []string
}

type spaceShowRuntimeAgentbox struct {
	Name   string
	Status string
	Image  string
}

type spaceShowData struct {
	MetadataOnly         bool
	DefaultAccount       string
	AccountDiskID        string
	AccountDiskUUID      string
	Instance             vmhost.Instance
	ConfigPath           string
	HostAgentLogPath     string
	Workdirs             []spaceShowWorkdir
	ConfiguredAgentboxes []string
	RuntimeAgentboxes    []spaceShowRuntimeAgentbox
	RuntimeStatus        string
	RuntimeError         string
	// SavedDisk is the bootstrap's system disk setting and DataDisk the data
	// disk's size and any pending grow, both set by the host.
	SavedDisk     string
	DataDisk      string
	DataDiskBytes uint64
	DataDiskGrow  string
}

const ephemeralWorkdirLabel = "(ephemeral / VM-local)"

func newSpaceShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [SPACE]",
		Short: "Show one space's VM config, workdirs, and box inventory",
		Long: `Shows the VM that backs a yoloctl space plus the yoloctl
metadata attached to it: config/log paths, boxes grouped by the
host workdir they share, configured box YAMLs, and runtime box
containers when the space is running.

  yoloctl space show
  yoloctl space show prod-untrusted
  yoloctl space show default --json   # machine-readable output`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveSpacePositional(args)
			if err != nil {
				return err
			}
			data, err := collectSpaceShow(cmd.Context(), name)
			if err != nil {
				return err
			}
			if asJSON {
				return writeSpaceShowJSON(cmd.OutOrStdout(), data)
			}
			return writeSpaceShow(cmd.OutOrStdout(), data)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the human-readable view")
	return cmd
}

func collectSpaceShow(ctx context.Context, name string) (*spaceShowData, error) {
	cfg, err := spaceconfig.Load(name)
	if err != nil {
		return nil, err
	}
	inst, found, probeErr := findSpaceInstance(ctx, name)
	if !found || probeErr != nil {
		dir, err := vmhost.InstanceDir(name)
		if err != nil {
			return nil, err
		}
		status := "Absent"
		if probeErr != nil {
			status = "Unavailable"
		}
		inst = vmhost.Instance{Name: name, Status: status, Dir: dir}
	}

	configured, err := agentboxcfg.ListNames(name)
	if err != nil {
		return nil, fmt.Errorf("list configured boxes: %w", err)
	}
	workdirs, err := workdirsForSpaceShow(name)
	if err != nil {
		return nil, err
	}
	data := &spaceShowData{
		DefaultAccount: cfg.DefaultAccount, AccountDiskID: cfg.AccountDiskID, AccountDiskUUID: cfg.AccountDiskUUID,
		Instance:             inst,
		ConfigPath:           filepath.Join(inst.Dir, "lima.yaml"),
		HostAgentLogPath:     filepath.Join(inst.Dir, "ha.stdout.log"),
		Workdirs:             workdirs,
		ConfiguredAgentboxes: configured,
		RuntimeStatus:        "skipped",
	}
	if inst.Status != "Running" {
		if probeErr != nil {
			data.RuntimeStatus = "error"
			data.RuntimeError = probeErr.Error()
		}
		return data, nil
	}
	containers, err := container.New(name).List(ctx, agentboxcfg.LabelAgentbox)
	if err != nil {
		data.RuntimeStatus = "error"
		data.RuntimeError = err.Error()
		return data, nil
	}
	data.RuntimeStatus = "ok"
	data.RuntimeAgentboxes = make([]spaceShowRuntimeAgentbox, 0, len(containers))
	for _, c := range containers {
		data.RuntimeAgentboxes = append(data.RuntimeAgentboxes, spaceShowRuntimeAgentbox{
			Name:   c.Name,
			Status: c.Status,
			Image:  c.Image,
		})
	}
	return data, nil
}

// workdirsForSpaceShow derives the workdir grouping from the space's
// configured agentbox YAMLs: one entry per distinct host workdir, plus a
// single ephemeral group for all VM-local boxes.
func workdirsForSpaceShow(spaceName string) ([]spaceShowWorkdir, error) {
	names, err := agentboxcfg.ListNames(spaceName)
	if err != nil {
		return nil, err
	}
	groups := map[string]*spaceShowWorkdir{}
	for _, n := range names {
		cfg, err := agentboxcfg.Load(spaceName, n)
		if err != nil {
			continue
		}
		key := ephemeralWorkdirLabel
		mode := agentboxcfg.WorkdirModeEphemeral
		if cfg.Workdir.IsHost() {
			key = cfg.Workdir.MountSource()
			mode = agentboxcfg.WorkdirModeHost
		}
		g, ok := groups[key]
		if !ok {
			g = &spaceShowWorkdir{Workdir: key, Mode: mode}
			groups[key] = g
		}
		g.Agentboxes = append(g.Agentboxes, n)
	}
	out := make([]spaceShowWorkdir, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Workdir < out[j].Workdir })
	return out, nil
}

func writeSpaceShow(w io.Writer, data *spaceShowData) error {
	inst := data.Instance
	_, _ = fmt.Fprintf(w, "Name:       %s\n", inst.Name)
	_, _ = fmt.Fprintf(w, "Status:     %s\n", inst.Status)
	if inst.StatusDetail != "" {
		_, _ = fmt.Fprintf(w, "Reason:     %s\n", inst.StatusDetail)
	}
	if data.DefaultAccount != "" {
		_, _ = fmt.Fprintf(w, "Default account: %s\nAccount disk: %s\n", data.DefaultAccount, layout.AccountDiskName(data.AccountDiskID))
	}
	if inst.VMType != "" {
		_, _ = fmt.Fprintf(w, "VM type:    %s\n", inst.VMType)
	}
	if inst.Arch != "" {
		_, _ = fmt.Fprintf(w, "Arch:       %s\n", inst.Arch)
	}
	_, _ = fmt.Fprintf(w, "CPUs:       %d\n", inst.CPUs)
	_, _ = fmt.Fprintf(w, "Memory:     %s\n", humanBytes(inst.Memory))
	_, _ = fmt.Fprintf(w, "Disk:       %s\n", diskShown(inst.Disk, data.SavedDisk))
	if data.DataDisk != "" {
		_, _ = fmt.Fprintf(w, "Data disk:  %s\n", data.DataDisk)
	}
	_, _ = fmt.Fprintf(w, "Dir:        %s\n", inst.Dir)
	_, _ = fmt.Fprintf(w, "Config:     %s\n", data.ConfigPath)
	_, _ = fmt.Fprintf(w, "Host log:   %s\n", data.HostAgentLogPath)
	if data.MetadataOnly {
		_, _ = fmt.Fprintf(w, "Declarations: unavailable (%s)\n", data.RuntimeError)
		return nil
	}

	if len(data.Workdirs) == 0 {
		_, _ = fmt.Fprintln(w, "Workdirs:   (none)")
	} else {
		_, _ = fmt.Fprintln(w, "Workdirs:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  WORKDIR\tMODE\tBOXES")
		for _, wd := range data.Workdirs {
			boxes := "-"
			if len(wd.Agentboxes) > 0 {
				boxes = strings.Join(wd.Agentboxes, ",")
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", wd.Workdir, wd.Mode, boxes)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(data.ConfiguredAgentboxes) == 0 {
		_, _ = fmt.Fprintln(w, "Boxes:")
		_, _ = fmt.Fprintln(w, "  Configured: (none)")
	} else {
		_, _ = fmt.Fprintln(w, "Boxes:")
		_, _ = fmt.Fprintf(w, "  Configured: %s\n", strings.Join(data.ConfiguredAgentboxes, ", "))
	}
	switch data.RuntimeStatus {
	case "ok":
		if len(data.RuntimeAgentboxes) == 0 {
			_, _ = fmt.Fprintln(w, "  Runtime:    (none)")
			return nil
		}
		_, _ = fmt.Fprintln(w, "  Runtime:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "    NAME\tSTATUS\tIMAGE")
		for _, a := range data.RuntimeAgentboxes {
			_, _ = fmt.Fprintf(tw, "    %s\t%s\t%s\n", a.Name, a.Status, a.Image)
		}
		return tw.Flush()
	case "error":
		_, _ = fmt.Fprintf(w, "  Runtime:    unavailable (%s)\n", data.RuntimeError)
	default:
		_, _ = fmt.Fprintf(w, "  Runtime:    skipped (space status: %s)\n", inst.Status)
	}
	return nil
}

func writeSpaceShowJSON(w io.Writer, data *spaceShowData) error {
	type workdirJSON struct {
		Workdir    string   `json:"workdir"`
		Mode       string   `json:"mode"`
		Agentboxes []string `json:"boxes"`
	}
	type runtimeJSON struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Image  string `json:"image"`
	}
	type agentboxesJSON struct {
		Configured      []string      `json:"configured"`
		ConfiguredCount int           `json:"configured_count"`
		Runtime         []runtimeJSON `json:"runtime"`
		RuntimeCount    int           `json:"runtime_count"`
		RuntimeStatus   string        `json:"runtime_status"`
		RuntimeError    string        `json:"runtime_error,omitempty"`
	}
	type spaceJSON struct {
		DeclarationsAvailable bool           `json:"declarations_available"`
		DefaultAccount        string         `json:"default_account,omitempty"`
		AccountDiskID         string         `json:"account_disk_id,omitempty"`
		AccountDiskName       string         `json:"account_disk_name,omitempty"`
		AccountDiskUUID       string         `json:"account_disk_uuid,omitempty"`
		Name                  string         `json:"name"`
		Status                string         `json:"status"`
		StatusDetail          string         `json:"status_detail,omitempty"`
		VMType                string         `json:"vm_type,omitempty"`
		Arch                  string         `json:"arch,omitempty"`
		CPUs                  int            `json:"cpus"`
		MemoryBytes           uint64         `json:"memory_bytes"`
		DiskBytes             uint64         `json:"disk_bytes"`
		SavedDiskBytes        uint64         `json:"saved_disk_bytes,omitempty"`
		DataDiskBytes         uint64         `json:"data_disk_bytes,omitempty"`
		DataDiskGrow          string         `json:"data_disk_grow,omitempty"`
		Dir                   string         `json:"dir"`
		ConfigPath            string         `json:"config_path"`
		HostAgentLogPath      string         `json:"host_agent_log_path"`
		Workdirs              []workdirJSON  `json:"workdirs"`
		Agentboxes            agentboxesJSON `json:"boxes"`
	}

	workdirs := make([]workdirJSON, 0, len(data.Workdirs))
	for _, wd := range data.Workdirs {
		boxes := wd.Agentboxes
		if boxes == nil {
			boxes = []string{}
		}
		workdirs = append(workdirs, workdirJSON{
			Workdir:    wd.Workdir,
			Mode:       wd.Mode,
			Agentboxes: boxes,
		})
	}
	configured := data.ConfiguredAgentboxes
	if configured == nil {
		configured = []string{}
	}
	runtimeAgentboxes := make([]runtimeJSON, 0, len(data.RuntimeAgentboxes))
	for _, a := range data.RuntimeAgentboxes {
		runtimeAgentboxes = append(runtimeAgentboxes, runtimeJSON(a))
	}
	out := spaceJSON{
		DeclarationsAvailable: !data.MetadataOnly,
		DefaultAccount:        data.DefaultAccount, AccountDiskID: data.AccountDiskID, AccountDiskUUID: data.AccountDiskUUID,
		AccountDiskName:  layout.AccountDiskName(data.AccountDiskID),
		Name:             data.Instance.Name,
		Status:           data.Instance.Status,
		StatusDetail:     data.Instance.StatusDetail,
		VMType:           data.Instance.VMType,
		Arch:             data.Instance.Arch,
		CPUs:             data.Instance.CPUs,
		MemoryBytes:      data.Instance.Memory,
		DiskBytes:        data.Instance.Disk,
		SavedDiskBytes:   savedDiskBytes(data.SavedDisk),
		DataDiskBytes:    data.DataDiskBytes,
		DataDiskGrow:     data.DataDiskGrow,
		Dir:              data.Instance.Dir,
		ConfigPath:       data.ConfigPath,
		HostAgentLogPath: data.HostAgentLogPath,
		Workdirs:         workdirs,
		Agentboxes: agentboxesJSON{
			Configured:      configured,
			ConfiguredCount: len(configured),
			Runtime:         runtimeAgentboxes,
			RuntimeCount:    len(runtimeAgentboxes),
			RuntimeStatus:   data.RuntimeStatus,
			RuntimeError:    data.RuntimeError,
		},
	}
	if data.MetadataOnly {
		out.Agentboxes.ConfiguredCount = -1
		out.Agentboxes.RuntimeCount = -1
		out.Agentboxes.Configured = nil
		out.Agentboxes.Runtime = nil
		out.Workdirs = nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// newSpaceUseCmd shows or switches the current-context space pin. Context
// tracks only the space, so the verb lives directly under `space`.
func newSpaceUseCmd() *cobra.Command {
	var reset bool
	cmd := &cobra.Command{
		Use:   "use [SPACE]",
		Short: "Show or switch the current-context space",
		Long: `yoloctl maintains a current space — the space a command implicitly
targets when no --space flag is passed. Resolution order:

  --space flag       (per-command override, always wins)
    ↓ otherwise
  YOLOCTL_SPACE      (nonempty per-process default, e.g. the TUI pin)
    ↓ otherwise
  context pin        (~/.config/yoloctl/context.yaml)
    ↓ otherwise
  "default"

Without arguments, prints the currently resolved space and where it came
from. With SPACE, pins that space. Pass --reset (no SPACE) to clear the pin
so resolution uses YOLOCTL_SPACE if set, otherwise "default".

  yoloctl space use            # show the current space + source
  yoloctl space use prod       # switch the pin to "prod"
  yoloctl space use --reset    # clear the pin

space use writes the global default. An open TUI keeps its own default;
use its top-right space menu (or pane prefix then s) to switch that TUI.
Switching asks before closing its tabs and does not rewrite the global pin.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reset && len(args) > 0 {
				return asUserError(errors.New("--reset and SPACE are mutually exclusive"))
			}
			// No SPACE and no --reset: just show what's resolved now.
			if !reset && len(args) == 0 {
				return printResolvedContext(cmd.OutOrStdout())
			}
			ctx, err := config.LoadContext()
			if err != nil {
				return err
			}
			if reset {
				ctx.Space = ""
			} else {
				if err := validateSpaceName(args[0]); err != nil {
					return err
				}
				ctx.Space = args[0]
			}
			if err := ctx.Save(); err != nil {
				return err
			}
			return printResolvedContext(cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false, "Clear the space pin (fall back to \"default\")")
	return cmd
}

// printResolvedContext shows what the current space resolves to right now,
// with the source annotation so a pin is distinguishable from the literal
// default.
func printResolvedContext(w io.Writer) error {
	space, spaceSrc, err := config.ResolveSpace("", false)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "Space:   %s (%s)\n", space, spaceSrc)
	return nil
}

func newSpaceListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List yoloctl spaces",
		Long: `Lists every VM instance yoloctl knows about, with its status,
CPU / memory / disk allocation, and on-disk path.

  yoloctl space list
  yoloctl space list --json   # machine-readable output`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			instances, err := vmhost.New().List(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return writeInstancesJSON(cmd.OutOrStdout(), instances)
			}
			return printInstances(cmd.OutOrStdout(), instances, nil)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the table view")
	return cmd
}

// writeInstancesJSON emits a top-level array of space objects. Memory
// and disk are reported as raw bytes (suffix `_bytes`) for machine
// consumers; the table view formats them with humanBytes.
func writeInstancesJSON(w io.Writer, instances []vmhost.Instance) error {
	type spaceJSON struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		StatusDetail string `json:"status_detail,omitempty"`
		VMType       string `json:"vm_type,omitempty"`
		Arch         string `json:"arch,omitempty"`
		CPUs         int    `json:"cpus"`
		MemoryBytes  uint64 `json:"memory_bytes"`
		DiskBytes    uint64 `json:"disk_bytes"`
		Dir          string `json:"dir"`
	}
	out := make([]spaceJSON, 0, len(instances))
	for _, inst := range instances {
		out = append(out, spaceJSON{
			Name:         inst.Name,
			Status:       inst.Status,
			StatusDetail: inst.StatusDetail,
			VMType:       inst.VMType,
			Arch:         inst.Arch,
			CPUs:         inst.CPUs,
			MemoryBytes:  inst.Memory,
			DiskBytes:    inst.Disk,
			Dir:          inst.Dir,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// printInstances lists spaces; saved maps a space to its saved system disk
// setting, so a size not yet applied is named.
func printInstances(w io.Writer, instances []vmhost.Instance, saved map[string]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tCPUS\tMEMORY\tDISK\tDIR")
	for _, inst := range instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			inst.Name, inst.Status, inst.CPUs,
			humanBytes(inst.Memory), humanBytes(inst.Disk), inst.Dir)
		if inst.StatusDetail != "" {
			_, _ = fmt.Fprintf(tw, "  %s\n", inst.StatusDetail)
		}
		if note := pendingDiskNote(inst.Disk, saved[inst.Name]); note != "" {
			_, _ = fmt.Fprintf(tw, "  %s\n", note)
		}
	}
	return tw.Flush()
}

func humanBytes(n uint64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case n >= GiB:
		return fmt.Sprintf("%.1fGiB", float64(n)/GiB)
	case n >= MiB:
		return fmt.Sprintf("%.1fMiB", float64(n)/MiB)
	case n >= KiB:
		return fmt.Sprintf("%.1fKiB", float64(n)/KiB)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
