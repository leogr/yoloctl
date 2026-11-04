package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spacearchive"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"gopkg.in/yaml.v3"
)

func TestArchivePublicationPrivateExclusiveAndFailureAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.tar.gz")
	write := func(w io.Writer) error { _, err := io.WriteString(w, "private backup"); return err }
	if err := writeExclusiveArchive(path, write); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private backup: %v %v", st, err)
	}
	if err := writeExclusiveArchive(path, write); err == nil {
		t.Fatal("replaced backup")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusiveArchive(link, write); err == nil {
		t.Fatal("replaced symlink")
	}
	failed := filepath.Join(dir, "failed")
	if err := writeExclusiveArchive(failed, func(w io.Writer) error { _ = write(w); return errors.New("stream interrupted") }); err == nil {
		t.Fatal("lost stream error")
	}
	if _, err := os.Lstat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published partial backup", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("left temporary backup", entries, err)
	}
}

func TestImportRemapsGrantsAndCreatesIndependentIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Space creation persists canonical grants; reproduce that contract even
	// when the host's temporary directory is a symlink (e.g. /var on macOS).
	old, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	next, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Import also accepts a symlink spelling of the destination workdir.
	nextAlias := filepath.Join(t.TempDir(), "destination")
	if err := os.Symlink(next, nextAlias); err != nil {
		t.Fatal(err)
	}
	legacy, err := spaceconfig.New("original", old, "trial")
	if err != nil {
		t.Fatal(err)
	}
	legacy.AccountDiskUUID = "source-uuid"
	source, settings, err := spaceconfig.Split(legacy, 1000, 1000, "source-generation")
	if err != nil {
		t.Fatal(err)
	}
	source.VM.Backend = "firecracker"
	source.VM.Image = filepath.Join(t.TempDir(), "source-image")
	source.VM.CPUs = 7
	cpuProbes := stubHostCPU(t, resources.HostCPU{Logical: 64}, nil)
	opts := shellOpts("shell", "none")
	opts.Space = "original"
	opts.Features = []string{"yolo.chat.skill"}
	opts.SessionCommand = []string{"aircd", "serve", "--config", "/workdir/chat with spaces,commas/server.toml"}
	opts.workdir, _, err = agentbox.NewHostWorkdir(old)
	if err != nil {
		t.Fatal(err)
	}
	box, _, err := buildAgentboxConfig(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	box.SchemaVersion = agentbox.SchemaVersion
	box.Account = "none"
	boxRaw, _ := yaml.Marshal(box)
	accountRaw, _ := yaml.Marshal(account.New("original", "trial"))
	seed := &controller.StoreBundle{Schema: 1, Settings: *settings, Accounts: map[string][]byte{"trial": accountRaw}, Boxes: map[string][]byte{"shell": boxRaw}}
	cmd := newSpaceImportCmd()
	cmd.SetContext(t.Context())
	if err := cmd.Flags().Set("backend", "lima"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("workdir", nextAlias); err != nil {
		t.Fatal(err)
	}
	boot, err := prepareImportedSpace(cmd, "duplicate", spacearchive.Manifest{Bootstrap: *source}, seed)
	if err != nil {
		t.Fatal(err)
	}
	if boot.DiskID == source.DiskID || boot.DiskUUID != "" || !boot.Initializing || boot.Generation == source.Generation {
		t.Fatal("source disk or generation reused", boot)
	}
	if boot.VM.CPUs != 7 || *cpuProbes != 0 {
		t.Fatalf("import must keep archived CPUs without measuring: %d, %d probes", boot.VM.CPUs, *cpuProbes)
	}
	if boot.Name != "duplicate" || boot.VM.Backend != "lima" || boot.Mounts[0].Source != next || seed.Settings.DefaultWorkdir != next {
		t.Fatal("mapping missing", boot, seed.Settings)
	}
	restored, err := agentbox.ParseManifest(seed.Boxes["shell"])
	if err != nil || restored.Workdir.Canonical != next {
		t.Fatal("box mapping", restored, err)
	}
	if !slices.Equal(restored.Features, opts.Features) || !slices.Equal(restored.SessionCommand, opts.SessionCommand) {
		t.Fatalf("cross-backend import lost AIRC features or literal session argv: %+v", restored)
	}
	argv, err := resolveSessionArgv("duplicate", restored)
	if err != nil || !slices.Equal(argv, opts.SessionCommand) {
		t.Fatalf("restored AIRC session would launch %q: %v", argv, err)
	}
	if _, err := seed.Documents(boot); err != nil {
		t.Fatal("invalid destination declarations", err)
	}
	composed, err := seed.Settings.Compose(boot)
	if err != nil {
		t.Fatal(err)
	}
	restored.Workdir, _, err = agentbox.NewHostWorkdir(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateImportedGrants(restored, composed); err == nil {
		t.Fatal("accepted bind outside remapped grants")
	}
}
