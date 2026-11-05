package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spacearchive"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newSpaceExportCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "export SPACE ARCHIVE.tar.gz", Short: "Back up declarations and native account state", Long: `Create a private, versioned tar.gz backup containing box/account declarations,
account secrets, native agent files and declared managed chat data. Managed room
credentials are regenerated on import. Host projects, global catalogs, VM images,
container contents, ephemeral workdirs and sessions are excluded.
Native sockets and profile-declared process artifacts are omitted; saved agent
history and settings remain. Absolute links within an agent's native state
become relative archive links. Other external links fail with their entry name.
Boxes must be stopped, or pass --stop to stop them. Boxes stay stopped. A stopped
VM is temporarily started for a verified export and stopped again afterward.
The archive contains credentials in plaintext (gzip is not encryption). Existing
output files are never overwritten. A leading ~/ in the archive path expands
to your home directory.`, Args: cobra.ExactArgs(2), RunE: exportSpace}
	cmd.Flags().Bool("stop", false, "Stop running boxes before taking the consistent backup")
	return cmd
}
func newSpaceImportCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "import SPACE ARCHIVE.tar.gz", Short: "Restore a backup into a new space", Long: `Validate a space archive, allocate a fresh native disk and restore declarations
and account files. Managed chat data and desired membership are retained with
new room identities and credentials; runtime application remains pending.
SPACE must not exist and its name must use 1–16 ASCII characters.
Choose the destination VM backend and current host's new-space Lima home;
boxes stay stopped and their images are rebuilt/pulled on explicit box start.
Host projects and global catalogs must already exist on the destination host.
Recorded host grants are reused; --workdir remaps the original default workdir,
and --mount-map OLD=NEW remaps other host paths. Native file ownership is adjusted
to the importing developer. Partial imports retain private recovery state; use
space start to resume or space destroy to discard that new space. A leading ~/
in the archive path expands to your home directory.`, Args: cobra.ExactArgs(2), RunE: importSpace}
	cmd.Flags().String("backend", vmhost.DefaultBackend(), "Destination VM backend: lima or firecracker")
	cmd.Flags().String("vm-image", "", "Optional prepared Firecracker image override (otherwise prepared locally)")
	cmd.Flags().String("workdir", "", "Replacement path for the archived default host workdir")
	cmd.Flags().StringArray("mount-map", nil, "Remap a host path prefix: OLD=NEW (repeatable)")
	cmd.Flags().Bool("no-start", false, "Stop the restored VM after importing (boxes always remain stopped)")
	return cmd
}

func exportSpace(cmd *cobra.Command, args []string) (result error) {
	name := args[0]
	release, err := lockHostLifecycle(cmd, name)
	if err != nil {
		return err
	}
	defer release()
	boot, err := spaceconfig.LoadBootstrap(name)
	if err != nil {
		return err
	}
	if boot.Initializing || boot.Import != nil {
		return errors.New("finish space initialization before exporting")
	}
	if g := boot.DataDiskGrow; g != nil {
		return fmt.Errorf("a data disk grow %s → %s is pending; start the space to finish it, then stop it, before exporting", diskBytesLabel(g.From), diskBytesLabel(g.To))
	}
	target, err := expandUserPath(args[1])
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("archive output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	inst, found, err := findSpaceInstance(cmd.Context(), name)
	if err != nil {
		return err
	}
	wasRunning := found && inst.Status == "Running"
	if err := startControllerSpace(cmd.Context(), boot, false, nil, cmd.ErrOrStderr()); err != nil {
		return err
	}
	// A host-only resume reopens admission without restarting the controller or
	// interrupting unrelated read/attach streams. A previously stopped VM is
	// returned to its original stopped state through the shared lifecycle.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var recovery error
		if wasRunning {
			recovery = controllerControl(ctx, boot, "control.resume", cmd.ErrOrStderr())
		} else {
			recovery = stopControllerSpace(ctx, boot, cmd.ErrOrStderr())
		}
		if recovery != nil {
			result = errors.Join(result, fmt.Errorf("restore VM state after export: %w", recovery))
		}
	}()
	if err := controllerControl(cmd.Context(), boot, "control.drain", cmd.ErrOrStderr()); err != nil {
		return err
	}
	stopBoxes, _ := cmd.Flags().GetBool("stop")
	if stopBoxes {
		if err := nativeInfrastructure(cmd.Context(), name, "stop-workloads", nil, io.Discard, cmd.ErrOrStderr()); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "yoloctl-export-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	state, err := os.OpenFile(filepath.Join(dir, "state.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	if err := nativeInfrastructure(cmd.Context(), name, "export-store", nil, state, cmd.ErrOrStderr()); err != nil {
		return err
	}
	if err := writeExclusiveArchive(target, func(w io.Writer) error { return spacearchive.Write(cmd.Context(), w, *boot, state) }); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "exported space %s to %s; archive includes account credentials\n", name, target)
	return err
}

func writeExclusiveArchive(path string, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".yoloctl-export-")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err := write(f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Link publishes atomically without replacing an existing backup or symlink.
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	if err := atomicfile.SyncDir(filepath.Dir(path)); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	return nil
}

func importSpace(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := layout.ValidateName("space", name); err != nil {
		return asUserError(err)
	}
	release, err := lockHostLifecycle(cmd, name)
	if err != nil {
		return err
	}
	defer release()
	if exists, err := spaceconfig.Exists(name); err != nil {
		return err
	} else if exists {
		return errors.New("import destination space already exists")
	}
	backend, _ := cmd.Flags().GetString("backend")
	if err := vmhost.ValidateNewInstancePaths(backend, name); err != nil {
		return asUserError(err)
	}
	if _, found, err := vmhost.New().LookupBackend(cmd.Context(), name, backend); err != nil {
		return err
	} else if found {
		return errors.New("import destination has an orphan VM")
	}
	sourcePath := args[1]
	if sourcePath == "~" || strings.HasPrefix(sourcePath, "~/") {
		sourcePath, err = expandUserPath(sourcePath)
		if err != nil {
			return err
		}
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("space archive must be a regular file")
	}
	scratch, err := os.MkdirTemp("", "yoloctl-import-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	state, err := os.OpenFile(filepath.Join(scratch, "state.tar"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	manifest, seed, err := spacearchive.Read(cmd.Context(), source, state)
	if err != nil {
		return err
	}
	boot, err := prepareImportedSpace(cmd, name, manifest, seed)
	if err != nil {
		return err
	}
	// Validate all archive content, identities and grants before publishing any
	// owned destination state. Native image selection is explicit lifecycle work.
	boot.Import = &spaceconfig.ImportState{SHA256: manifest.StateSHA256, Size: manifest.StateSize}
	if err := saveControllerSeed(name, seed); err != nil {
		return err
	}
	dir, err := layout.SpaceBundleDir(name)
	if err != nil {
		return err
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return err
	}
	pending, err := os.OpenFile(filepath.Join(dir, "import-state.tar"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(pending, state)
	syncErr := pending.Sync()
	closeErr := pending.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := atomicfile.SyncDir(dir); err != nil {
		return err
	}
	if err := boot.Save(); err != nil {
		return err
	}
	if err := vmhost.New().CreateAccountDisk(cmd.Context(), boot.DiskID); err != nil {
		return fmt.Errorf("allocate import disk; recovery bootstrap retained: %w", err)
	}
	if err := startControllerSpace(cmd.Context(), boot, false, seed, cmd.ErrOrStderr()); err != nil {
		return fmt.Errorf("import incomplete; new space retained; inspect then use space start %s or space destroy %s: %w", name, name, err)
	}
	if err := retireControllerSeed(name); err != nil {
		return err
	}
	if noStart, _ := cmd.Flags().GetBool("no-start"); noStart {
		if err := stopControllerSpace(cmd.Context(), boot, cmd.ErrOrStderr()); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "imported space %s using %s; accounts and declarations restored; start boxes explicitly\n", name, boot.VM.Backend)
	return err
}

func prepareImportedSpace(cmd *cobra.Command, name string, m spacearchive.Manifest, seed *controller.StoreBundle) (*spaceconfig.Bootstrap, error) {
	mappings := map[string]string{}
	requested, _ := cmd.Flags().GetStringArray("mount-map")
	if workdir, _ := cmd.Flags().GetString("workdir"); workdir != "" {
		requested = append(requested, seed.Settings.DefaultWorkdir+"="+workdir)
	}
	for _, value := range requested {
		old, next, ok := strings.Cut(value, "=")
		if !ok || !filepath.IsAbs(old) || filepath.Clean(old) != old {
			return nil, errors.New("mount map must be an absolute OLD=NEW path pair")
		}
		if _, exists := mappings[old]; exists {
			return nil, errors.New("duplicate import path mapping")
		}
		canonical, exists, err := canonicalizeMountArg(next)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("import destination path %s does not exist", canonical)
		}
		mappings[old] = canonical
	}
	keys := make([]string, 0, len(mappings))
	for k := range mappings {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	remap := func(p string) string {
		for _, old := range keys {
			if p == old || strings.HasPrefix(p, old+string(filepath.Separator)) {
				return mappings[old] + strings.TrimPrefix(p, old)
			}
		}
		return p
	}
	workdir := remap(seed.Settings.DefaultWorkdir)
	legacy, err := spaceconfig.New(name, workdir, seed.Settings.DefaultAccount)
	if err != nil {
		return nil, err
	}
	legacy.AccountDiskInitializing = true
	boot, _, err := spaceconfig.Split(legacy, os.Getuid(), os.Getgid(), rand.Text())
	if err != nil {
		return nil, err
	}
	boot.VM = m.Bootstrap.VM
	boot.VM.Backend, _ = cmd.Flags().GetString("backend")
	if err := boot.SelectNewLimaHome(); err != nil {
		return nil, err
	}
	image, _ := cmd.Flags().GetString("vm-image")
	boot.VM.Image, err = vmhost.ResolveImage(cmd.Context(), boot.VM.Backend, image)
	if err != nil {
		return nil, err
	}
	boot.Mounts = nil
	for _, mount := range m.Bootstrap.Mounts {
		mount.Source = remap(mount.Source)
		canonical, exists, err := canonicalizeMountArg(mount.Source)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("host grant %s is missing; use --mount-map OLD=NEW", mount.Source)
		}
		if canonical != mount.Source {
			return nil, errors.New("import grant resolves through a different host alias; map it explicitly")
		}
		if err := rejectSpaceHostPath(canonical, legacy); err != nil {
			return nil, err
		}
		boot.Mounts = append(boot.Mounts, mount)
	}
	seed.Settings.Name = name
	seed.Settings.DefaultWorkdir = workdir
	imported, err := seed.Settings.Compose(boot)
	if err != nil {
		return nil, err
	}
	for key, raw := range seed.Boxes {
		cfg, err := agentbox.ParseManifest(raw)
		if err != nil {
			return nil, err
		}
		cfg.Space = name
		if cfg.Workdir.Mode == agentbox.WorkdirModeHost {
			cfg.Workdir.Source = remap(cfg.Workdir.Canonical)
			cfg.Workdir.Canonical = cfg.Workdir.Source
		}
		for i := range cfg.Mounts {
			cfg.Mounts[i].Source = remap(cfg.Mounts[i].Source)
		}
		if err := validateImportedGrants(cfg, imported); err != nil {
			return nil, err
		}
		if err := requireResolvableManifest(cmd.Context(), cfg); err != nil {
			return nil, fmt.Errorf("box %s needs destination catalogs: %w", key, err)
		}
		if err := rejectAccountRootBinds(cfg); err != nil {
			return nil, err
		}
		raw, err = yaml.Marshal(cfg)
		if err != nil {
			return nil, err
		}
		seed.Boxes[key] = raw
	}
	if err := seed.ReidentifyImportedChat(boot.DiskID); err != nil {
		return nil, err
	}
	boot.Bundle, err = controller.ExecutableIdentity()
	if err != nil {
		return nil, err
	}
	if err := boot.Validate(); err != nil {
		return nil, err
	}
	if _, err := seed.Documents(boot); err != nil {
		return nil, err
	}
	return boot, nil
}

type restoreRequest struct {
	Context spaceenv.Context        `json:"context"`
	State   spaceconfig.ImportState `json:"state"`
}

func restoreControllerSpace(ctx context.Context, boot *spaceconfig.Bootstrap, c spaceenv.Context, errw io.Writer) error {
	dir, err := layout.SpaceBundleDir(boot.Name)
	if err != nil {
		return err
	}
	// Do not follow a substituted staging file. The digest is verified again in
	// the guest before any account directory is published.
	f, err := openImportState(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := json.Marshal(restoreRequest{Context: c, State: *boot.Import})
	if err != nil {
		return err
	}
	command := vmhost.ShellCommand(ctx, boot.Name, "sudo", session.VMBinaryPath, "controller-internal", "restore-store")
	command.Stdin = io.MultiReader(bytes.NewReader(append(raw, '\n')), f)
	command.Stderr = errw
	return command.Run()
}

func validateImportedGrants(cfg *agentbox.Config, space *spaceconfig.Config) error {
	check := func(p string, writable bool) error {
		canonical, _, err := canonicalizeMountArg(p)
		if err != nil {
			return err
		}
		if canonical != p {
			return fmt.Errorf("imported bind %s changed its host alias; remap it explicitly", p)
		}
		if err := rejectSpaceHostPath(p, space); err != nil {
			return err
		}
		grant, ok := space.GrantFor(p)
		if !ok || writable && !grant.Writable {
			return fmt.Errorf("imported box %s bind is outside its space grants", cfg.Name)
		}
		return nil
	}
	if cfg.Workdir.IsHost() {
		if err := check(cfg.Workdir.Canonical, cfg.WorkdirWritable()); err != nil {
			return err
		}
	}
	for _, m := range cfg.Mounts {
		if isManagedStateDest(cfg, m.Target) {
			continue
		}
		if err := check(m.Source, !m.ReadOnly); err != nil {
			return err
		}
	}
	return nil
}
