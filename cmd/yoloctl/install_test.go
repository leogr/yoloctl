package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/installer"
	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/releasebundle"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

func TestInstallDecisionDoesNotTurnYesIntoConflictOrMigrationConsent(t *testing.T) {
	m := releasebundle.Manifest{Version: "v1.0.0", OS: "linux", Arch: "arm64", Bundle: strings.Repeat("a", 32)}
	other := m
	other.Bundle = strings.Repeat("b", 32)
	for _, tc := range []struct {
		name                                    string
		existing                                installer.Existing
		tty, yes, dry, printOnly, confirm, fail bool
	}{
		{name: "script print", printOnly: true},
		{name: "script install", yes: true},
		{name: "script conflict", existing: installer.Existing{Unknown: true}, yes: true, fail: true},
		{name: "script conflict dry-run prints", existing: installer.Existing{Unknown: true}, dry: true, printOnly: true},
		{name: "tty conflict yes still asks", existing: installer.Existing{Unknown: true}, tty: true, yes: true, confirm: true},
		{name: "tty upgrade asks", existing: installer.Existing{Manifest: &other}, tty: true, confirm: true},
		{name: "managed yes", existing: installer.Existing{Manifest: &other}, yes: true},
		{name: "identical tty", existing: installer.Existing{Manifest: &m}, tty: true},
		{name: "dry", tty: true, yes: true, dry: true, printOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, c, err := installDecision(tc.existing, m, tc.tty, tc.yes, tc.dry)
			if p != tc.printOnly || c != tc.confirm || (err != nil) != tc.fail {
				t.Fatal(p, c, err)
			}
		})
	}
}

func TestInstallConfirmDoesNotConsumeLaterInput(t *testing.T) {
	in := strings.NewReader("yes\nnext command\n")
	ok, err := confirmInstall("", in, &bytes.Buffer{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	remaining := make([]byte, in.Len())
	_, _ = in.Read(remaining)
	if string(remaining) != "next command\n" {
		t.Fatal(string(remaining))
	}
}

func TestInstallProposalsAreReadOnlyAndImageOriginIsNeverInvented(t *testing.T) {
	for _, which := range []string{"matching", "different", "missing", "corrupt", "old-default-override", "pending", "unavailable", "bad-bootstrap"} {
		t.Run(which, func(t *testing.T) {
			t.Setenv("YOLOCTL_HOME", t.TempDir())
			b := imageUpdateFixture(t)
			// imageUpdateFixture selects XDG, while the deliberately explicit home
			// remains authoritative; its source save is part of fixture setup only.
			b.Bundle = "new"
			if which == "pending" {
				b.ImageUpdate = &spaceconfig.VMImageUpdate{Previous: b.VM.Image, Target: filepath.Join(t.TempDir(), "target"), Bundle: "new", Phase: "prepared"}
			}
			if err := b.Save(); err != nil {
				t.Fatal(err)
			}
			oldObserve, oldDefault := observeInstallConsent, defaultInstallImage
			t.Cleanup(func() { observeInstallConsent, defaultInstallImage = oldObserve, oldDefault })
			observeInstallConsent = func(context.Context, string) (installmigration.Snapshot, error) {
				if which == "pending" {
					t.Fatal("pending recovery probed VM")
				}
				if which == "unavailable" {
					return installmigration.Snapshot{}, errors.New("backend unavailable")
				}
				return consentSnapshot(b, "Running")
			}
			defaultInstallImage = func() (string, error) {
				switch which {
				case "matching":
					return b.VM.Image, nil
				case "missing":
					return "", os.ErrNotExist
				case "corrupt":
					return "", errors.New("corrupt cache")
				}
				return filepath.Join(filepath.Dir(b.VM.Image), "different-default"), nil
			}
			path, err := layout.SpaceConfigPath(b.Name)
			if err != nil {
				t.Fatal(err)
			}
			if which == "bad-bootstrap" {
				if err := os.WriteFile(path, []byte("schema_version: [broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			p := installProposals(t.Context(), "new", "/absolute/install/link", true, &out)
			if len(p) != 0 {
				t.Fatalf("invented migration: %+v", p)
			}
			if which != "matching" && which != "pending" && !strings.Contains(out.String(), "UNKNOWN") {
				t.Fatal(out.String())
			}
			if which == "pending" && !strings.Contains(out.String(), "pending VM image update") {
				t.Fatal(out.String())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("proposal changed bootstrap")
			}
			root, err := layout.ConfigRoot()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"locks", "vm-images", "lima", "cache"} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("proposal created %s", name)
				}
			}
		})
	}
}

func TestInstallProposalOrderingAndBootWarnings(t *testing.T) {
	s := installmigration.Snapshot{Space: "fixture", Bundle: "old", State: "Stopped"}
	optional := installmigration.Command{Args: []string{"space", "recreate", "fixture"}, Recreate: true}
	p := proposeInstallMigration(s, "new", &optional, "")
	if len(p.Commands) != 2 || p.Commands[0].Args[1] != "start" || !p.Commands[1].Recreate || !strings.Contains(p.Commands[0].Warning, "BOOTS") {
		t.Fatal(p)
	}
	s.State = "Running"
	p = proposeInstallMigration(s, "new", nil, "")
	if !strings.Contains(p.Commands[0].Warning, "boot is forbidden") {
		t.Fatal(p)
	}
	s.Bundle = "new"
	p = proposeInstallMigration(s, "new", &optional, "")
	if len(p.Commands) != 1 || !p.Commands[0].Recreate {
		t.Fatal(p)
	}
}

func TestInstallMigrationUsesPrivateReceiptAndPreservesSkipStatus(t *testing.T) {
	before := installmigration.Snapshot{Space: "fixture", Bundle: "old", State: "Running", DiskID: "disk", DiskUUID: "uuid", Generation: "gen", Bootstrap: strings.Repeat("a", 64)}
	after := before
	after.Bundle = "new"
	receipt := installmigration.Receipt{Before: before, After: after}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"private", "stdout-only", "skip", "fail"} {
		t.Run(kind, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "runner")
			script := "#!/bin/sh\nprintf '%s\\n' '" + string(raw) + "'\n"
			switch kind {
			case "private":
				script += "printf '%s\\n' '" + string(raw) + "' >&3\n"
			case "skip":
				script += "exit 76\n"
			case "fail":
				script += "exit 1\n"
			}
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := newRootCmd()
			cmd.SetIn(strings.NewReader(""))
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			actual, err := runInstallMigration(t.Context(), cmd, executable, before, installmigration.Command{Args: []string{"space", "start", "fixture"}})
			if kind == "private" {
				if err != nil || actual != receipt {
					t.Fatal(actual, err)
				}
			} else if err == nil {
				t.Fatal("stdout impersonated private receipt")
			}
			if kind == "skip" && !errors.Is(err, installmigration.ErrChanged) {
				t.Fatal(err)
			}
		})
	}
}

func TestReadOnlyStartupOnlyRecognizesOwnedRoutes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"version", "--json"}, true}, {[]string{"install", "--dry-run"}, true},
		{[]string{"space", "start", "fixture", "--install-precondition=opaque"}, true},
		{[]string{"space", "recreate", "fixture", "--install-precondition", "opaque"}, true},
		{[]string{"space", "ssh", "fixture", "--", "--install-precondition"}, false},
		{[]string{"space", "start", "fixture", "--", "--install-precondition"}, false},
	} {
		if got := readOnlyStartup(tc.args); got != tc.want {
			t.Fatal(tc.args, got)
		}
	}
}

// Print-only/--dry-run proposals say that nothing is installed yet, because
// the printed commands run the link that still selects the old build.
func TestInstallProposalsNotInstalledNote(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	b := imageUpdateFixture(t)
	b.Bundle = "old"
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	oldObserve, oldDefault := observeInstallConsent, defaultInstallImage
	t.Cleanup(func() { observeInstallConsent, defaultInstallImage = oldObserve, oldDefault })
	observeInstallConsent = func(_ context.Context, space string) (installmigration.Snapshot, error) {
		saved, err := spaceconfig.LoadBootstrap(space)
		if err != nil {
			return installmigration.Snapshot{}, err
		}
		return consentSnapshot(saved, "Running")
	}
	defaultInstallImage = func() (string, error) { return b.VM.Image, nil }
	for _, installing := range []bool{true, false} {
		var out bytes.Buffer
		if p := installProposals(t.Context(), "new", "/absolute/install/link", installing, &out); len(p) != 1 {
			t.Fatalf("proposals = %+v\n%s", p, out.String())
		}
		if got := strings.Contains(out.String(), "Not installed yet"); got == installing {
			t.Fatalf("installing=%v note=%v\n%s", installing, got, out.String())
		}
	}
}

// The installer passes --force to a guarded recreate (its consent is bound to
// the snapshot), never to a start.
func TestInstallMigrationRecreateRunsWithoutSecondPrompt(t *testing.T) {
	before := installmigration.Snapshot{Space: "fixture", Bundle: "new", State: "Running", DiskID: "disk", DiskUUID: "uuid", Generation: "gen", Bootstrap: strings.Repeat("a", 64)}
	for _, recreate := range []bool{true, false} {
		dir := t.TempDir()
		executable := filepath.Join(dir, "runner")
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + filepath.Join(dir, "args") + "\nexit 76\n"
		if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := newRootCmd()
		cmd.SetIn(strings.NewReader(""))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		args := []string{"space", "start", "fixture"}
		if recreate {
			args = []string{"space", "recreate", "fixture"}
		}
		_, _ = runInstallMigration(t.Context(), cmd, executable, before, installmigration.Command{Args: args, Recreate: recreate})
		raw, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "--force") != recreate {
			t.Fatalf("recreate=%v args %q", recreate, raw)
		}
	}
}

// archyFable F1: an interrupted migration child gets SIGINT (cooperative
// cancellation), and its exit is reported as an interruption, not a skip.
func TestInstallMigrationInterruptIsCooperative(t *testing.T) {
	before := installmigration.Snapshot{Space: "fixture", Bundle: "old", State: "Running", DiskID: "disk", DiskUUID: "uuid", Generation: "gen", Bootstrap: strings.Repeat("a", 64)}
	dir := t.TempDir()
	marker := filepath.Join(dir, "interrupted")
	executable := filepath.Join(dir, "runner")
	script := "#!/bin/sh\ntrap 'touch " + marker + "; exit 76' INT\n: > " + filepath.Join(dir, "started") + "\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCmd()
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	_, err := runInstallMigration(ctx, cmd, executable, before, installmigration.Command{Args: []string{"space", "start", "fixture"}})
	if errors.Is(err, installmigration.ErrChanged) || !errors.Is(err, context.Canceled) {
		t.Fatal("interrupted child reported as", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatal("child was not interrupted cooperatively (no SIGINT)", statErr)
	}
}

// archyFable F2: a Lima VM too old for this build gets the recreation that
// fixes it, never a controller update known to fail.
func TestInstallProposalsRecreateTooOldLimaVM(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	boot, _ := resourceSpaceFixture(t)
	if boot.VM.Backend == "firecracker" {
		t.Skip("fixture is not a Lima space")
	}
	oldObserve, oldCheck := observeInstallConsent, limaOpenShellCheck
	t.Cleanup(func() { observeInstallConsent, limaOpenShellCheck = oldObserve, oldCheck })
	observeInstallConsent = func(_ context.Context, space string) (installmigration.Snapshot, error) {
		saved, err := spaceconfig.LoadBootstrap(space)
		if err != nil {
			return installmigration.Snapshot{}, err
		}
		return consentSnapshot(saved, "Stopped")
	}
	limaOpenShellCheck = func(string) error { return lima.ErrOpenShellVMTooOld }
	var out bytes.Buffer
	p := installProposals(t.Context(), "new", "/absolute/install/link", true, &out)
	if len(p) != 1 || len(p[0].Commands) != 1 || !p[0].Commands[0].Recreate || p[0].Commands[0].Args[1] != "recreate" {
		t.Fatalf("too-old Lima VM proposals = %+v\n%s", p, out.String())
	}
}

func TestInstallPathHintNamesTheLineForTheUsersShell(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ shell, path, want string }{
		{"/bin/zsh", "/usr/bin", `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc`},
		{"/usr/bin/bash", "/usr/bin", `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc`},
		{"/usr/local/bin/fish", "/usr/bin", `fish_add_path "$HOME/.local/bin"`},
		{"/bin/tcsh", "/usr/bin", "add it to PATH in your shell's startup file"},
		{"", "", "add it to PATH in your shell's startup file"},
	} {
		hint := installPathHint(bin, c.path, c.shell, home)
		if !strings.Contains(hint, c.want) || !strings.HasPrefix(hint, bin+" is not on PATH") {
			t.Errorf("shell %q: hint %q lacks %q", c.shell, hint, c.want)
		}
	}
	if hint := installPathHint(bin, "/usr/bin"+string(os.PathListSeparator)+bin, "/bin/zsh", home); hint != "" {
		t.Errorf("bin already on PATH, got hint %q", hint)
	}
	link := filepath.Join(t.TempDir(), "bin-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	if hint := installPathHint(bin, link, "/bin/zsh", home); hint != "" {
		t.Errorf("bin on PATH through a symlink, got hint %q", hint)
	}
	for _, odd := range []string{filepath.Join(t.TempDir(), "it's bin"), "/tmp/$TOOLS", filepath.Join(home, "$x", "bin")} {
		for _, shell := range []string{"/bin/zsh", "/bin/bash", "/usr/bin/fish"} {
			if hint := installPathHint(odd, "/usr/bin", shell, home); strings.Contains(hint, "echo") || strings.Contains(hint, "fish_add_path") || !strings.Contains(hint, "is not on PATH") {
				t.Errorf("%s with %s got a shell command: %q", odd, shell, hint)
			}
		}
	}
	if hint := installPathHint(bin, "/usr/bin", "/bin/tcsh", home); strings.Contains(hint, "export") {
		t.Errorf("unknown shell got an export line: %q", hint)
	}
}
