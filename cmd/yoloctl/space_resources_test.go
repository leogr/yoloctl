package main

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/spf13/cobra"
)

func resourceSpaceFixture(t *testing.T) (*spaceconfig.Bootstrap, *[]vmapi.Resources) {
	t.Helper()
	stubOperatorClient(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg, err := spaceconfig.New("dev", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccountDiskUUID = "resource-test-disk"
	b, _, err := spaceconfig.Split(cfg, 1000, 1000, "resource-test-generation")
	if err != nil {
		t.Fatal(err)
	}
	b.Bundle = "selected-controller"
	b.VM.Memory, b.VM.CPUs = "4GiB", 4
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	oldLookup, oldApply, oldSelect := lookupResourceSpace, applyVMResources, selectResourceMemory
	t.Cleanup(func() { lookupResourceSpace, applyVMResources, selectResourceMemory = oldLookup, oldApply, oldSelect })
	lookupResourceSpace = func(context.Context, string) (vmapi.Instance, bool, error) {
		return vmapi.Instance{Name: "dev", Status: "Stopped"}, true, nil
	}
	var applied []vmapi.Resources
	applyVMResources = func(_ context.Context, name string, r vmapi.Resources) error {
		saved, err := spaceconfig.LoadBootstrap(name)
		if err != nil || saved.VM.CPUs != r.CPUs || saved.VM.Memory != r.Memory {
			t.Fatalf("backend called before desired settings were durable: %+v, %v", saved, err)
		}
		applied = append(applied, r)
		return nil
	}
	selectResourceMemory = func(ctx context.Context, value string, out io.Writer) (string, error) {
		if value == "auto" {
			return "12GiB", nil
		}
		return resolveSpaceMemory(ctx, value, out)
	}
	return b, &applied
}

func resourceCommand(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newClientRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(t.Context())
}

func TestSpaceSetResourcesPreservesIdentityAndRepairsBackend(t *testing.T) {
	before, applied := resourceSpaceFixture(t)
	if err := resourceCommand(t, "space", "set", "dev", "--memory", "auto", "--cpus", "8"); err != nil {
		t.Fatal(err)
	}
	after, err := spaceconfig.LoadBootstrap("dev")
	if err != nil {
		t.Fatal(err)
	}
	want := *before
	want.VM.CPUs, want.VM.Memory = 8, "12GiB"
	if !reflect.DeepEqual(&want, after) {
		t.Fatalf("changed more than resources: want %+v, got %+v", want, after)
	}
	if len(*applied) != 1 || (*applied)[0].Memory != "12GiB" {
		t.Fatal(*applied)
	}
	// The same desired values must still repair an interrupted adapter update.
	if err := resourceCommand(t, "space", "set", "dev", "--cpus", "8"); err != nil {
		t.Fatal(err)
	}
	if len(*applied) != 2 || (*applied)[1].Memory != "12GiB" {
		t.Fatalf("omitted memory changed, or backend repair skipped: %v", *applied)
	}
}

func TestSpaceSetResourcesRefusesInvalidOrLiveState(t *testing.T) {
	for _, scenario := range []string{"Running", "Paused", "Starting", "unknown", "missing", "lookup-error", "invalid-cpu", "auto-cpu", "invalid-memory", "empty", "pending-import", "pending-init"} {
		t.Run(scenario, func(t *testing.T) {
			boot, applied := resourceSpaceFixture(t)
			args := []string{"space", "set", "dev", "--memory", "8GiB"}
			switch scenario {
			case "invalid-cpu":
				args = append(args, "--cpus", "0")
			case "auto-cpu":
				// Automatic CPU selection is creation-only.
				args = append(args, "--cpus", "auto")
			case "invalid-memory":
				args = []string{"space", "set", "dev", "--memory", "wrong"}
			case "empty":
				args = []string{"space", "set", "dev"}
			case "pending-import":
				boot.Import = &spaceconfig.ImportState{SHA256: strings.Repeat("a", 64), Size: 1}
				if err := boot.Save(); err != nil {
					t.Fatal(err)
				}
			case "pending-init":
				boot.Initializing = true
				if err := boot.Save(); err != nil {
					t.Fatal(err)
				}
			default:
				lookupResourceSpace = func(context.Context, string) (vmapi.Instance, bool, error) {
					if scenario == "lookup-error" {
						return vmapi.Instance{}, false, errors.New("inventory unavailable")
					}
					return vmapi.Instance{Status: scenario}, scenario != "missing", nil
				}
			}
			before, err := readSpaceResourceSnapshot("dev")
			if err != nil {
				t.Fatal(err)
			}
			if err := resourceCommand(t, args...); err == nil {
				t.Fatal("accepted invalid or uncertain resource change")
			}
			after, err := readSpaceResourceSnapshot("dev")
			if err != nil || !reflect.DeepEqual(before.data, after.data) || len(*applied) != 0 {
				t.Fatalf("refusal mutated state: %v, %v", err, *applied)
			}
		})
	}
}

func TestSpaceSetResourcesRetainsDesiredAfterApplyFailure(t *testing.T) {
	_, applied := resourceSpaceFixture(t)
	apply := applyVMResources
	applyVMResources = func(context.Context, string, vmapi.Resources) error { return errors.New("write failed") }
	err := resourceCommand(t, "space", "set", "dev", "--memory", "8GiB")
	if err == nil || !strings.Contains(err.Error(), "saved but not applied") {
		t.Fatal(err)
	}
	b, err := spaceconfig.LoadBootstrap("dev")
	if err != nil || b.VM.Memory != "8GiB" {
		t.Fatalf("lost recoverable desired state: %v, %v", b, err)
	}
	applyVMResources = apply
	if err := resourceCommand(t, "space", "set", "dev", "--memory", "8GiB"); err != nil || len(*applied) != 1 {
		t.Fatalf("explicit retry failed: %v", err)
	}
}

func TestSpaceSetResourcesSaveFailureDoesNotChangeBackend(t *testing.T) {
	resourceSpaceFixture(t)
	called := false
	applyVMResources = func(context.Context, string, vmapi.Resources) error {
		called = true
		return nil
	}
	selectResourceMemory = func(context.Context, string, io.Writer) (string, error) {
		// A host-side filesystem failure after validation must not leave the
		// backend changed without durable desired settings.
		dir, err := layout.SpaceBundleDir("dev")
		if err != nil {
			return "", err
		}
		if err := os.Rename(dir, dir+".saved"); err != nil {
			return "", err
		}
		if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
			return "", err
		}
		return "8GiB", nil
	}
	if err := resourceCommand(t, "space", "set", "dev", "--memory", "8GiB"); err == nil || called {
		t.Fatalf("backend changed after bootstrap save failed: %v, called=%v", err, called)
	}
}

func TestSpaceEditResourcesRechecksAfterUnlockedEditor(t *testing.T) {
	for _, scenario := range []string{"valid-auto", "unknown-field", "missing-cpu", "trailing-document", "replaced-bootstrap", "concurrent-start"} {
		t.Run(scenario, func(t *testing.T) {
			_, applied := resourceSpaceFixture(t)
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := editSpaceResources(cmd, "dev", func(ctx context.Context, path string) error {
				t.Cleanup(func() { _ = os.Remove(path) })
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("unsafe editor temporary file: %v", err)
				}
				ctx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				release, err := spacelock.AcquireWithWait(ctx, "dev", true, nil)
				if err != nil {
					t.Fatalf("editor held lifecycle lock: %v", err)
				}
				release()
				raw := "cpus: 6\nmemory: auto\n"
				switch scenario {
				case "unknown-field":
					raw += "disk_id: other\n"
				case "missing-cpu":
					raw = "memory: auto\n"
				case "trailing-document":
					raw += "---\ncpus: 8\nmemory: 16GiB\n"
				case "replaced-bootstrap":
					before, err := readSpaceResourceSnapshot("dev")
					if err != nil {
						t.Fatal(err)
					}
					dir, _ := layout.SpaceBundleDir("dev")
					if err := safepath.WriteFile(dir, "space.yaml", before.data, 0600); err != nil {
						t.Fatal(err)
					}
				case "concurrent-start":
					lookupResourceSpace = func(context.Context, string) (vmapi.Instance, bool, error) {
						return vmapi.Instance{Status: "Running"}, true, nil
					}
				}
				return os.WriteFile(path, []byte(raw), 0600)
			})
			if scenario == "valid-auto" {
				if err != nil || len(*applied) != 1 || (*applied)[0] != (vmapi.Resources{CPUs: 6, Memory: "12GiB"}) {
					t.Fatalf("valid edit: %v, %v", err, *applied)
				}
			} else if err == nil || !strings.Contains(err.Error(), "edits kept at") || len(*applied) != 0 {
				t.Fatalf("invalid edit: %v, %v", err, *applied)
			}
		})
	}
}

func TestSpaceResourceCommandsAreHostOnlyAndComplete(t *testing.T) {
	resourceSpaceFixture(t)
	for _, verb := range []string{"set", "edit"} {
		root := newClientRootCmd()
		cmd, _, err := root.Find([]string{"space", verb})
		if err != nil || cmd.Name() != verb || cmd.ValidArgsFunction == nil {
			t.Fatalf("missing command/completion for %s: %v", verb, err)
		}
		if verb == "set" {
			fn, ok := cmd.GetFlagCompletionFunc("memory")
			if !ok {
				t.Fatal("missing memory autocomplete")
			}
			choices, _ := fn(cmd, nil, "")
			if !strings.Contains(strings.Join(choices, " "), "auto") {
				t.Fatal(choices)
			}
			if _, ok := cmd.GetFlagCompletionFunc("cpus"); !ok {
				t.Fatal("missing CPU autocomplete")
			}
		}
	}
	stubOperatorClient(t, &controller.OperatorClient{Space: "dev"})
	for _, verb := range []string{"set", "edit"} {
		if err := resourceCommand(t, "space", verb, "dev"); err == nil || !strings.Contains(err.Error(), "operator") {
			t.Fatalf("operator could run space %s: %v", verb, err)
		}
	}
}
