package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
				saved, err := spaceconfig.LoadBootstrap(b.Name)
				if err != nil {
					return installmigration.Snapshot{}, err
				}
				return consentSnapshot(saved, "Running")
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
			advice := installSpaceAdvice(t.Context(), "new")
			var got spaceAdvice
			if len(advice) == 1 {
				got = advice[0]
			}
			for _, item := range got.Items {
				if item.Label != "optional" || !slices.Equal(item.Args, []string{"space", "recreate", b.Name, "--update-vm-image"}) {
					t.Fatalf("invented a command: %+v", item)
				}
			}
			switch which {
			case "matching":
				if len(advice) != 0 {
					t.Fatalf("a current space needs nothing: %+v", advice)
				}
			case "pending":
				if !strings.Contains(got.Notice, "pending VM image update") || len(got.Items) != 0 {
					t.Fatalf("%+v", advice)
				}
			case "missing":
				if len(got.Items) != 1 || !strings.Contains(got.Items[0].Note, "install did not change this space's VM image") {
					t.Fatalf("an absent default only says install did not change it: %+v", advice)
				}
			case "different", "old-default-override":
				if len(got.Items) != 1 || !strings.Contains(got.Items[0].Note, "is not this release's default") {
					t.Fatalf("%+v", advice)
				}
			case "corrupt":
				if !strings.Contains(got.Problem, "could not check this space's VM image") || len(got.Items) != 0 {
					t.Fatalf("%+v", advice)
				}
			default:
				if !strings.Contains(got.Problem, "could not") || len(got.Items) != 0 {
					t.Fatalf("%+v", advice)
				}
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

func TestReadOnlyStartupOnlyRecognizesOwnedRoutes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"version", "--json"}, true}, {[]string{"install", "--dry-run"}, true},
		{[]string{"space", "start", "fixture", "--install-precondition=opaque"}, false},
		{[]string{"space", "recreate", "fixture"}, false},
	} {
		if got := readOnlyStartup(tc.args); got != tc.want {
			t.Fatal(tc.args, got)
		}
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
		{"/bin/tcsh", "/usr/bin", "add it in your shell's startup file"},
		{"", "", "add it in your shell's startup file"},
	} {
		text, command := installPathHint(bin, c.path, c.shell, home)
		if hint := text + " " + command; !strings.Contains(hint, c.want) || !strings.HasPrefix(text, bin+" is not on your PATH") {
			t.Errorf("shell %q: hint %q lacks %q", c.shell, hint, c.want)
		}
	}
	if hint, _ := installPathHint(bin, "/usr/bin"+string(os.PathListSeparator)+bin, "/bin/zsh", home); hint != "" {
		t.Errorf("bin already on PATH, got hint %q", hint)
	}
	link := filepath.Join(t.TempDir(), "bin-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	if hint, _ := installPathHint(bin, link, "/bin/zsh", home); hint != "" {
		t.Errorf("bin on PATH through a symlink, got hint %q", hint)
	}
	for _, odd := range []string{filepath.Join(t.TempDir(), "it's bin"), "/tmp/$TOOLS", filepath.Join(home, "$x", "bin")} {
		for _, shell := range []string{"/bin/zsh", "/bin/bash", "/usr/bin/fish"} {
			if text, command := installPathHint(odd, "/usr/bin", shell, home); command != "" || !strings.Contains(text, "is not on your PATH") {
				t.Errorf("%s with %s got a shell command: %q %q", odd, shell, text, command)
			}
		}
	}
	if _, command := installPathHint(bin, "/usr/bin", "/bin/tcsh", home); command != "" {
		t.Errorf("unknown shell got a command: %q", command)
	}
}

// installAdviceFixture is a saved Firecracker space whose controller is the
// "old" build and whose default image matches, observed in state.
func installAdviceFixture(t *testing.T, state string) *spaceconfig.Bootstrap {
	t.Helper()
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
		return consentSnapshot(saved, state)
	}
	defaultInstallImage = func() (string, error) { return b.VM.Image, nil }
	return b
}

// A space on another controller build gets one update command: its note
// says a stopped VM starts, and a running one keeps its agents only for a
// controller-only update. The command runs the install link with the
// explicit YOLOCTL_HOME, and print-only output says nothing is installed yet.
func TestInstallSpaceAdviceUpdate(t *testing.T) {
	for _, state := range []string{"Stopped", "Running"} {
		b := installAdviceFixture(t, state)
		advice := installSpaceAdvice(t.Context(), "new")
		if len(advice) != 1 || len(advice[0].Items) != 1 || advice[0].Header != "uses another controller build" {
			t.Fatalf("%s: %+v", state, advice)
		}
		item := advice[0].Items[0]
		want := map[string]string{"Stopped": "this starts the VM", "Running": "a controller-only update keeps agents running"}[state]
		if item.Label != "update" || !slices.Equal(item.Args, []string{"space", "start", b.Name, "--update-controller"}) || !strings.Contains(item.Note, want) {
			t.Fatalf("%s: %+v", state, item)
		}
		for _, installed := range []bool{true, false} {
			var out bytes.Buffer
			printSpaceAdvice(installOut{w: &out}, advice, "/absolute/install/link", installed)
			text := out.String()
			command := "  update    YOLOCTL_HOME=" + shellArg(os.Getenv("YOLOCTL_HOME")) + " /absolute/install/link space start " + b.Name + " --update-controller\n"
			if !strings.Contains(text, b.Name+" · "+strings.ToLower(state)+" · uses another controller build\n") || !strings.Contains(text, command) ||
				strings.Contains(text, "\x1b") || strings.Contains(text, "Not installed yet") == installed {
				t.Fatalf("%s installed=%v:\n%s", state, installed, text)
			}
		}
	}
}

// archyFable F2: a Lima VM too old for this build gets the recreation that
// fixes it, never a controller update known to fail.
func TestInstallSpaceAdviceRecreateTooOldLimaVM(t *testing.T) {
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
	advice := installSpaceAdvice(t.Context(), "new")
	if len(advice) != 1 || len(advice[0].Items) != 1 || advice[0].Items[0].Label != "recreate" || advice[0].Items[0].Args[1] != "recreate" {
		t.Fatalf("too-old Lima VM advice = %+v", advice)
	}
}

// Color only on a terminal; NO_COLOR and TERM=dumb force plain text, and the
// words never change.
func TestInstallOutputColor(t *testing.T) {
	if newInstallOut(&bytes.Buffer{}).color {
		t.Fatal("a buffer is not a terminal")
	}
	plain, colored := installOut{}, installOut{color: true}
	if plain.paint(roleOK, "✓ ") != "✓ " || colored.paint(roleOK, "✓ ") != "\x1b[38;5;108m✓ \x1b[0m" || colored.paint(roleBold, "v1") != "\x1b[1mv1\x1b[0m" {
		t.Fatal("paint roles")
	}
	t.Setenv("NO_COLOR", "")
	if installColor(os.Stdout) {
		t.Fatal("NO_COLOR, even empty, disables color")
	}
	if got := wrapWords("aa bb cc dd", 5); !slices.Equal(got, []string{"aa bb", "cc dd"}) {
		t.Fatal(got)
	}
	if shellArg("/home/u/.local/bin/yoloctl") != "/home/u/.local/bin/yoloctl" || shellArg("/tmp/it's") != `'/tmp/it'\''s'` || shellArg("$x") != "'$x'" {
		t.Fatal("shellArg")
	}
}
