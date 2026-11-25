package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// stubDefaultImage makes this build's published default VM image path (or its
// read error) the given value.
func stubDefaultImage(t *testing.T, path string, err error) {
	t.Helper()
	old := defaultInstallImage
	t.Cleanup(func() { defaultInstallImage = old })
	defaultInstallImage = func() (string, error) { return path, err }
}

// limaStackFixture writes this build's template pins and the space's
// effective lima.yaml; an empty instance value leaves the space unpinned.
func limaStackFixture(t *testing.T, space, desired, instance string) {
	t.Helper()
	old := vmhost.TemplateWithAccountDisk
	t.Cleanup(func() { vmhost.TemplateWithAccountDisk = old })
	vmhost.TemplateWithAccountDisk = func([]vmhost.Mount, string, bool) []byte { return []byte(desired) }
	dir, err := vmhost.InstanceDir(space)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte(instance), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	pinnedStack = "containerd:\n  archives:\n  - arch: x86_64\n    location: https://example.test/nerdctl.tar.gz\n    digest: sha256:aaaa\n"
	otherStack  = "containerd:\n  archives:\n  - arch: x86_64\n    location: https://example.test/nerdctl.tar.gz\n    digest: sha256:bbbb\n"
)

// Only the existing read-only selections decide: Firecracker's recorded image
// against this build's published default, and Lima's effective archives
// against this build's pins. Anything else is reported as not detectable.
func TestSpaceUpgradeVMReplacementUsesTheExistingSelections(t *testing.T) {
	t.Run("firecracker", func(t *testing.T) {
		boot := imageUpdateFixture(t)
		for _, tc := range []struct {
			name, path string
			err        error
			reason     string
			note       string
		}{
			{name: "current", path: boot.VM.Image},
			{name: "other image", path: filepath.Join(t.TempDir(), "new"), reason: "is not this build's default VM image"},
			{name: "not prepared", err: os.ErrNotExist, reason: "has not been prepared on this host"},
			{name: "unreadable", err: errors.New("invalid default image identity"), note: "could not compare this space's VM image with this build's: invalid default image identity"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stubDefaultImage(t, tc.path, tc.err)
				need, note := readSpaceVMReplacement(boot)
				if note != tc.note {
					t.Fatalf("note %q, want %q", note, tc.note)
				}
				if tc.reason == "" {
					if need != nil {
						t.Fatalf("unexpected replacement %+v", need)
					}
					return
				}
				if need == nil || !strings.Contains(need.reason, tc.reason) || need.command() != "yoloctl space recreate fixture --update-vm-image" {
					t.Fatalf("replacement %+v", need)
				}
			})
		}
	})
	t.Run("lima", func(t *testing.T) {
		t.Setenv("YOLOCTL_HOME", t.TempDir())
		boot, _ := resourceSpaceFixture(t)
		for _, tc := range []struct {
			name, desired, instance string
			reason                  string
			note                    string
		}{
			{name: "matching pins", desired: pinnedStack, instance: pinnedStack, note: "not detectable"},
			{name: "other pins", desired: pinnedStack, instance: otherStack, reason: "pinned to other versions"},
			{name: "unpinned space", desired: pinnedStack, instance: "containerd:\n  system: true\n", reason: "unpinned"},
			{name: "unpinned build", desired: "containerd:\n  system: true\n", instance: otherStack, note: "not detectable"},
			{name: "unprovable", desired: pinnedStack, instance: "base: [template:external]\n", note: "could not prove this space's Lima stack"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				limaStackFixture(t, boot.Name, tc.desired, tc.instance)
				need, note := readSpaceVMReplacement(boot)
				if tc.note != "" && !strings.Contains(note, tc.note) || tc.note == "" && note != "" {
					t.Fatalf("note %q, want %q", note, tc.note)
				}
				if tc.reason == "" {
					if need != nil {
						t.Fatalf("unexpected replacement %+v", need)
					}
					return
				}
				if need == nil || !strings.Contains(need.reason, tc.reason) || need.command() != "yoloctl space recreate "+boot.Name {
					t.Fatalf("replacement %+v", need)
				}
			})
		}
	})
}

// The question is asked once, default No, only of a person at the terminal.
// Only an explicit yes runs the ordinary recreate; a no is a decline; no
// terminal, EOF or another answer leave it outstanding.
func TestSpaceUpgradeVMRecreateConsent(t *testing.T) {
	for _, tc := range []struct {
		name               string
		interactive        bool
		answer             string
		recreated          bool
		declined, unsolved bool
	}{
		{name: "yes", interactive: true, answer: "y\n", recreated: true},
		{name: "blank defaults to no", interactive: true, answer: "\n", declined: true},
		{name: "no", interactive: true, answer: "no\n", declined: true},
		{name: "EOF", interactive: true, answer: "", unsolved: true},
		{name: "other answer", interactive: true, answer: "maybe\n", unsolved: true},
		{name: "no terminal", answer: "y\n", unsolved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boot := imageUpdateFixture(t)
			stubDefaultImage(t, filepath.Join(t.TempDir(), "new"), nil)
			oldRecreate, oldInteractive := recreateCompatibleSpace, spaceUpgradeVMInteractive
			t.Cleanup(func() { recreateCompatibleSpace, spaceUpgradeVMInteractive = oldRecreate, oldInteractive })
			spaceUpgradeVMInteractive = func(*cobra.Command) bool { return tc.interactive }
			recreates := 0
			recreateCompatibleSpace = func(_ *cobra.Command, got *spaceconfig.Bootstrap) error {
				recreates++
				if got.Name != boot.Name {
					t.Fatalf("recreated %s", got.Name)
				}
				return nil
			}
			var prompt, logs bytes.Buffer
			t.Cleanup(bindCLITestLogger(t, &logs))
			in := strings.NewReader(tc.answer + "later box answer\n")
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetIn(in)
			cmd.SetErr(&prompt)
			recreated, result, err := offerSpaceUpgradeVMRecreate(cmd, boot.Name)
			if err != nil || recreated != tc.recreated || recreates != map[bool]int{true: 1}[tc.recreated] {
				t.Fatalf("recreated=%v recreates=%d err=%v", recreated, recreates, err)
			}
			if (len(result.declined) == 1) != tc.declined || (len(result.outstanding) == 1) != tc.unsolved {
				t.Fatalf("result %+v", result)
			}
			for _, line := range append(result.declined, result.outstanding...) {
				if !strings.Contains(line, "yoloctl space recreate fixture --update-vm-image") {
					t.Fatalf("missing follow-up command: %q", line)
				}
			}
			asked := strings.Contains(prompt.String(), "Recreate VM fixture now to match this build? The box review continues afterwards. [y/N]: ")
			if asked != tc.interactive {
				t.Fatalf("prompt %q", prompt.String())
			}
			if !strings.Contains(logs.String(), "Space fixture needs a VM recreate to match this build: this space's VM image is not this build's default VM image.") {
				t.Fatalf("reason not shown:\n%s", logs.String())
			}
			// The answer is read byte by byte: later box answers stay unread.
			if tc.interactive && tc.answer != "" {
				rest, _ := io.ReadAll(in)
				if string(rest) != "later box answer\n" {
					t.Fatalf("consumed later input, left %q", rest)
				}
			}
		})
	}
}

// A matching VM asks nothing; a pending image update and a canceled
// invocation stop before any question or recreate.
func TestSpaceUpgradeVMRecreateAsksOnlyWhenProvenNeeded(t *testing.T) {
	oldRecreate, oldInteractive := recreateCompatibleSpace, spaceUpgradeVMInteractive
	t.Cleanup(func() { recreateCompatibleSpace, spaceUpgradeVMInteractive = oldRecreate, oldInteractive })
	spaceUpgradeVMInteractive = func(*cobra.Command) bool { return true }
	recreateCompatibleSpace = func(*cobra.Command, *spaceconfig.Bootstrap) error {
		t.Fatal("unapproved recreation")
		return nil
	}
	boot := imageUpdateFixture(t)
	var prompt bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetErr(&prompt)
	stubDefaultImage(t, boot.VM.Image, nil)
	if recreated, result, err := offerSpaceUpgradeVMRecreate(cmd, boot.Name); err != nil || recreated || len(result.declined)+len(result.outstanding) != 0 || prompt.Len() != 0 {
		t.Fatalf("matching VM: recreated=%v result=%+v err=%v prompt=%q", recreated, result, err, prompt.String())
	}
	stubDefaultImage(t, filepath.Join(t.TempDir(), "new"), nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd.SetContext(ctx)
	if _, _, err := offerSpaceUpgradeVMRecreate(cmd, boot.Name); !errors.Is(err, context.Canceled) || prompt.Len() != 0 {
		t.Fatalf("canceled: %v, prompt %q", err, prompt.String())
	}
	cmd.SetContext(t.Context())
	boot.ImageUpdate = &spaceconfig.VMImageUpdate{Previous: boot.VM.Image, Target: filepath.Join(t.TempDir(), "target"), Bundle: "new", Phase: "prepared"}
	if err := boot.Save(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := offerSpaceUpgradeVMRecreate(cmd, boot.Name); err == nil || prompt.Len() != 0 {
		t.Fatalf("pending image update: %v, prompt %q", err, prompt.String())
	}
}

// A declined VM recreate is a warning and keeps success; an unanswered one is
// outstanding and gives a nonzero result. Both name the recreate command.
func TestSpaceUpgradeVMResultExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		vm      spaceUpgradeVMResult
		title   string
		wantErr bool
	}{
		{"declined", spaceUpgradeVMResult{declined: []string{"space test VM (declined): yoloctl space recreate test --update-vm-image"}}, "Space VM left as you chose", false},
		{"outstanding", spaceUpgradeVMResult{outstanding: []string{"space test VM (needs terminal consent): yoloctl space recreate test --update-vm-image"}}, "Outstanding space action", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := upgradeTestBox("idle", boxstate.RuntimeStopped, "")
			drift := upgradeTestDrift(box, boxstate.DriftInSync)
			var logs bytes.Buffer
			t.Cleanup(bindCLITestLogger(t, &logs))
			ops := spaceUpgradeOps{
				sessionRestartOffer: sessionRestartOffer{
					in: strings.NewReader(""), errw: io.Discard,
					inventory: func(context.Context) ([]boxstate.Box, error) { return []boxstate.Box{box}, nil },
					drift:     func(context.Context, []string) ([]boxDriftView, error) { return []boxDriftView{drift}, nil },
				},
				room: upgradeNoRoom,
				vm:   tc.vm,
			}
			err := runSpaceUpgrade(t.Context(), "test", ops)
			if (err != nil) != tc.wantErr || tc.wantErr && !IsUserError(err) {
				t.Fatalf("err %v", err)
			}
			out := logs.String()
			if !strings.Contains(out, tc.title) || !strings.Contains(out, "yoloctl space recreate test --update-vm-image") {
				t.Fatalf("missing VM follow-up:\n%s", out)
			}
		})
	}
}

// Through the command: the VM question comes before the controller update. A
// yes runs the recreate and continues with the repaired-upgrade box review; a
// no continues with the ordinary controller update.
func TestSpaceUpgradeAsksAboutTheVMBeforeTheControllerUpdate(t *testing.T) {
	for _, answer := range []string{"y\n", "n\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			shortYoloctlHome(t)
			boot := imageUpdateFixture(t)
			// Lima, so the backend exists on every host that runs the tests.
			boot.VM.Backend, boot.VM.Image = "lima", ""
			if err := boot.Save(); err != nil {
				t.Fatal(err)
			}
			// The real template (its OpenShell provisioning passes the deploy
			// check), with one container-stack pin changed.
			template := string(vmhost.TemplateWithAccountDisk(nil, "", false))
			pin := "sha256:1f12c1a5840744fdf7029483c55a69813d44aa2ec1ef879d52d81ff6a3049ecd"
			if !strings.Contains(template, pin) {
				t.Fatal("template pin not found; update this fixture")
			}
			dir, err := vmhost.InstanceDir(boot.Name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte(strings.Replace(template, pin, "sha256:"+strings.Repeat("0", 64), 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			stubOperatorClient(t, nil)
			oldDeploy, oldCheck, oldRecreate, oldInteractive, oldInstance := newDeploymentVM, checkCommandVMCompatibility, recreateCompatibleSpace, spaceUpgradeVMInteractive, spaceOpenShellInstance
			t.Cleanup(func() {
				newDeploymentVM, checkCommandVMCompatibility = oldDeploy, oldCheck
				recreateCompatibleSpace, spaceUpgradeVMInteractive, spaceOpenShellInstance = oldRecreate, oldInteractive, oldInstance
			})
			// The fixture VM is not running: no live OpenShell probe.
			spaceOpenShellInstance = func(context.Context, string) (vmhost.Instance, bool, error) { return vmhost.Instance{}, false, nil }
			checkCommandVMCompatibility = func(context.Context, *spaceconfig.Bootstrap) error { return nil }
			spaceUpgradeVMInteractive = func(*cobra.Command) bool { return true }
			var events []string
			recreateProbe := errors.New("stop after the approved recreate")
			recreateCompatibleSpace = func(*cobra.Command, *spaceconfig.Bootstrap) error {
				events = append(events, "recreate")
				return recreateProbe
			}
			deployProbe := errors.New("stop at the controller update")
			newDeploymentVM = func() controllerDeploymentVM {
				return upgradeDeploymentProbe{require: func(string) error {
					events = append(events, "controller update")
					return deployProbe
				}}
			}
			root := newClientRootCmd()
			root.SetIn(strings.NewReader(answer))
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"space", "upgrade", boot.Name})
			err = root.ExecuteContext(t.Context())
			want, probe := "recreate", recreateProbe
			if answer == "n\n" {
				want, probe = "controller update", deployProbe
			}
			if !errors.Is(err, probe) || strings.Join(events, ",") != want {
				t.Fatalf("events %v, err %v", events, err)
			}
		})
	}
}
