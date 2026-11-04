package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePolicyReadUsesSelectedPolicyWithoutCredentialsOrCurrentSourceRequirement(t *testing.T) {
	f, checkpoint := generationFixture(t)
	prepared, err := checkpoint.prepareNext(t.Context(), "historical-controller", checkpoint.OriginalPolicy(), generationTarget(checkpoint), generationEffectFixture(t, checkpoint))
	if err != nil {
		t.Fatal(err)
	}
	f.record = prepared.Record
	expected, err := os.ReadFile(f.path("supervisor/policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Historical qualification/controller identity is observation only. These
	// fixtures never qualify the current compiled helper's source for mutation.
	if err := f.release.ValidateHelperSource(); err == nil {
		t.Fatal("fixture unexpectedly qualifies current mutation source")
	}
	for _, name := range []string{"boundary/bootstrap.json", "boundary/server.key", "supervisor/auth.json", "status/activation.key"} {
		if err := os.Remove(f.path(name)); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := readNativePolicyAt(t.Context(), f.root, f.record, f.release, f.owners)
	if err != nil || !bytes.Equal(policy, expected) {
		t.Fatal("read-only selected policy depended on credentials/current mutation source", err)
	}
	if strings.Contains(string(policy), "test-secret") {
		t.Fatal("policy view exposed private bootstrap environment")
	}
	policy[0] = 'X'
	again, err := readNativePolicyAt(t.Context(), f.root, f.record, f.release, f.owners)
	if err != nil || !bytes.Equal(again, expected) {
		t.Fatal("returned policy aliased retained state", err)
	}
}

func TestNativePolicyReadRefusesChangedUnsafeOrMissingSelectedFiles(t *testing.T) {
	for _, change := range []string{"origin", "current", "immutable-record", "policy", "policy-mode", "policy-link", "policy-symlink", "supervisor-link", "supervisor-owner", "missing-policy", "file-hash", "launch-hash", "oversize", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f, _ := generationFixture(t)
			ctx := t.Context()
			owners := f.owners
			original, err := os.ReadFile(f.path("supervisor/policy.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "origin":
				err = os.WriteFile(filepath.Join(f.root.Name(), "origin.json"), []byte("{}"), 0o600)
			case "current":
				err = os.WriteFile(filepath.Join(f.root.Name(), "current.json"), []byte("{}"), 0o600)
			case "immutable-record":
				err = os.WriteFile(f.path("record.json"), []byte("{}"), 0o600)
			case "policy":
				err = os.WriteFile(f.path("supervisor/policy.yaml"), append(bytes.Clone(original), []byte("\n# changed\n")...), 0o600)
			case "policy-mode":
				err = os.Chmod(f.path("supervisor/policy.yaml"), 0o644)
			case "policy-link":
				err = os.Link(f.path("supervisor/policy.yaml"), f.path("supervisor/extra"))
			case "policy-symlink":
				err = os.Rename(f.path("supervisor/policy.yaml"), f.path("supervisor/policy-old"))
				if err == nil {
					err = os.Symlink("policy-old", f.path("supervisor/policy.yaml"))
				}
			case "supervisor-link":
				err = os.Rename(f.path("supervisor"), f.path("supervisor-old"))
				if err == nil {
					err = os.Symlink("supervisor-old", f.path("supervisor"))
				}
			case "supervisor-owner":
				owners.supervisorUID++
			case "missing-policy":
				err = os.Remove(f.path("supervisor/policy.yaml"))
			case "file-hash":
				f.record.Files["supervisor/policy.yaml"] = strings.Repeat("b", 64)
				f.writeRecords(t)
			case "launch-hash":
				f.record.Launch.Binding.PolicySHA256 = strings.Repeat("b", 64)
				f.writeRecords(t)
			case "oversize":
				err = os.WriteFile(f.path("supervisor/policy.yaml"), bytes.Repeat([]byte("x"), (1<<20)+1), 0o600)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal("fixture setup", err)
			}
			policy, err := readNativePolicyAt(ctx, f.root, f.record, f.release, owners)
			if err == nil || policy != nil {
				t.Fatal("unsafe selected policy was read or silently replaced", change, err)
			}
			// A read never repairs the selected metadata or creates a legacy fallback.
			if change == "current" {
				raw, _ := os.ReadFile(filepath.Join(f.root.Name(), "current.json"))
				if string(raw) != "{}" {
					t.Fatal("view repaired metadata")
				}
			}
		})
	}
}

func TestNativePolicyReadRequiresNativeIdentityBeforeFilesystemAccess(t *testing.T) {
	f, _ := generationFixture(t)
	if _, err := ReadNativePolicy(t.Context(), f.record, f.release); err == nil {
		t.Fatal("policy observation bypassed admitted native-root guard")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadNativePolicy(ctx, f.record, f.release); !errors.Is(err, context.Canceled) {
		t.Fatal("policy read ignored cancellation", err)
	}
	// Decoder/record verification is unchanged by exposing this narrower read.
	raw, _ := json.Marshal(f.record)
	if _, err := DecodeRuntimeRecord(raw, f.release, f.record.Launch.HelperLaunch()); err != nil {
		t.Fatal(err)
	}
}
