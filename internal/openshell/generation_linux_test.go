package openshell

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func generationFixture(t *testing.T) (materialFixture, *NativeGeneration) {
	t.Helper()
	f := newMaterialFixture(t)
	raw, _ := json.Marshal(generationOrigin(f.record))
	if err := os.WriteFile(filepath.Join(f.root.Name(), "origin.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := snapshotGenerationAt(f.root, f.record, f.release, strings.Repeat("a", 64), f.owners)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return f, g
}
func generationTarget(g *NativeGeneration) func(context.Context) (GenerationTarget, error) {
	return func(context.Context) (GenerationTarget, error) {
		return GenerationTarget{ContainerID: g.cid, Status: "exited"}, nil
	}
}

// This effect fixture uses the REAL bootstrap/key minting and selected native
// file schema, with current-user owners. It does not call the root-only SDK,
// start a container or provide joint runtime/namespace proof.
func generationEffectFixture(t *testing.T, g *NativeGeneration, policies ...[]byte) func(context.Context, BootstrapInput, []byte) (PreparedRuntime, error) {
	t.Helper()
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policyByHash := map[string][]byte{sha256Hex(g.OriginalPolicy()): g.OriginalPolicy()}
	for _, policy := range policies {
		policyByHash[sha256Hex(policy)] = bytes.Clone(policy)
	}

	return func(ctx context.Context, input BootstrapInput, rego []byte) (PreparedRuntime, error) {
		bootstrap, err := NewBootstrap(issuer, input, time.Unix(1_800_000_000, 0))
		if err != nil {
			return PreparedRuntime{}, err
		}
		launch, err := NewLaunchConfig(input, g.release, g.current.Launch.HelperSHA256, g.current.Launch.UnconfinedNetwork)
		if err != nil {
			return PreparedRuntime{}, err
		}
		platform := g.current.Artifacts
		activation := make([]byte, 32)
		if _, err := rand.Read(activation); err != nil {
			return PreparedRuntime{}, err
		}
		encode := func(v any) []byte {
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
		files := map[string][]byte{"boundary/bootstrap.json": encode(bootstrap.boundary), "boundary/server.crt": bootstrap.certificate, "boundary/server.key": bootstrap.privateKey, "supervisor/runtime-descriptor.json": encode(bootstrap.descriptor), "supervisor/auth.json": encode(bootstrap.auth), "supervisor/policy.yaml": []byte(""), "supervisor/sandbox-policy.rego": rego, "status/activation.key": activation}
		policy, ok := policyByHash[input.Binding.PolicySHA256]
		if !ok {
			return PreparedRuntime{}, errors.New("effect fixture lacks the requested policy")
		}
		files["supervisor/policy.yaml"] = bytes.Clone(policy)

		record := RuntimeRecord{Launch: launch, Generation: bootstrap.descriptor.Generation, SessionID: bootstrap.descriptor.SessionID, Artifacts: platform, Files: map[string]string{}, SSHHostPublicKey: string(bootstrap.sshPublicKey)}
		directory := filepath.Join(g.root.Name(), "generations", record.Generation)
		for _, leaf := range []string{"boundary", "supervisor", "status"} {
			if err := os.MkdirAll(filepath.Join(directory, leaf), 0o700); err != nil {
				return PreparedRuntime{}, err
			}
		}
		for name, raw := range files {
			record.Files[name] = sha256Hex(raw)
			if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
				return PreparedRuntime{}, err
			}
		}
		raw := encode(record)
		for _, name := range []string{filepath.Join(directory, "record.json"), filepath.Join(g.root.Name(), "current.json")} {
			if err := os.WriteFile(name, raw, 0o600); err != nil {
				return PreparedRuntime{}, err
			}
		}
		return PreparedRuntime{ContainerRoot: g.root.Name(), GenerationRoot: directory, Record: record}, nil
	}
}
func TestNativeGenerationCheckpointPreservesPrivateCreateEnvironmentAndFreshAuthority(t *testing.T) {
	f, g := generationFixture(t)
	t.Setenv("API_KEY", "changed-account-or-ambient-secret")
	policy := g.OriginalPolicy()
	if err := g.ValidatePolicy(policy); err != nil {
		t.Fatal(err)
	}
	input, err := g.nextInput("current-controller", policy)
	if err != nil {
		t.Fatal(err)
	}
	if input.Environment["API_KEY"] != "test-secret" || input.Environment["HOME"] != "/home/sandbox" || input.Workdir != f.record.Launch.Workdir || input.ReadOnlyWorkdir != f.record.Launch.WorkspaceReadOnly || input.UID != f.record.Launch.UID || input.Binding.ControllerGeneration != "current-controller" {
		t.Fatal("generation refreshed secrets or changed identity/cwd/intent")
	}
	input.Environment["API_KEY"] = "caller-mutated-copy"
	old := cloneRuntimeRecord(g.current)
	changedPolicy := append(bytes.Clone(policy), []byte("\n# distinct policy checkpoint\n")...)
	prepare := generationEffectFixture(t, g, changedPolicy)
	first, err := g.prepareNext(t.Context(), "current-controller", changedPolicy, generationTarget(g), prepare)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.prepareNext(t.Context(), "current-controller", policy, generationTarget(g), prepare)
	if err != nil {
		t.Fatal(err)
	}
	if first.Record.Launch.Binding.PolicySHA256 != sha256Hex(changedPolicy) || second.Record.Launch.Binding.PolicySHA256 != sha256Hex(policy) {
		t.Fatal("new policy/rollback hashes changed")
	}
	if first.Record.Generation == old.Generation || first.Record.Generation == second.Record.Generation || first.Record.SessionID == second.Record.SessionID || first.Record.SSHHostPublicKey == second.Record.SSHHostPublicKey || first.Record.Files["status/activation.key"] == second.Record.Files["status/activation.key"] {
		t.Fatal("restart replayed generation/session/SSH/activation authority")
	}
	material, err := readRuntimeMaterialAt(g.root, g.release, second.Record.Launch.HelperLaunch(), g.owners)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := generationEnvironment(material.record, material.files["boundary/bootstrap.json"])
	if err != nil || environment["API_KEY"] != "test-secret" {
		t.Fatal("new bootstrap lost original private environment", err)
	}
	if got := fmt.Sprintf("%v %#v %v %#v", g, g, *g, *g); strings.Contains(got, "test-secret") {
		t.Fatal("checkpoint formatting exposed environment")
	}
	if !bytes.Equal(g.OriginalPolicy(), policy) {
		t.Fatal("new generation overwrote rollback checkpoint")
	}
	second.Record.Files["status/activation.key"] = "caller mutation"
	if g.current.Files["status/activation.key"] == "caller mutation" {
		t.Fatal("returned record aliases checkpoint")
	}
}
func TestNativeGenerationRefusesChangedSelectionIdentityAndEnvironmentBeforeEffect(t *testing.T) {
	for _, change := range []string{"running", "pid", "cid", "unknown", "selection", "origin", "policy", "directory"} {
		t.Run(change, func(t *testing.T) {
			_, g := generationFixture(t)
			policy := g.OriginalPolicy()
			target := GenerationTarget{ContainerID: g.cid, Status: "exited"}
			switch change {
			case "running":
				target.Running = true
			case "pid":
				target.PID = 22
			case "cid":
				target.ContainerID = strings.Repeat("b", 64)
			case "unknown":
				target.Status = "unknown"
			case "selection":
				if err := os.WriteFile(filepath.Join(g.root.Name(), "current.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "origin":
				if err := os.WriteFile(filepath.Join(g.root.Name(), "origin.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "policy":
				policy = []byte("filesystem_policy: {}\n")
			case "directory":
				if err := os.Rename(g.root.Name(), g.root.Name()+"-old"); err != nil {
					t.Fatal(err)
				}
				oldPath := g.root.Name() + "-old"
				t.Cleanup(func() { _ = os.RemoveAll(oldPath) })
				if err := os.Mkdir(g.root.Name(), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			_, err := g.prepareNext(t.Context(), "current-controller", policy, func(context.Context) (GenerationTarget, error) { return target, nil }, func(context.Context, BootstrapInput, []byte) (PreparedRuntime, error) {
				called = true
				return PreparedRuntime{}, nil
			})
			if err == nil || called {
				t.Fatal("stale/mixed preparation reached effect", err, called)
			}
		})
	}
}
func TestNativeGenerationPreparationUncertaintyPoisonsCheckpointWithoutRetry(t *testing.T) {
	_, g := generationFixture(t)
	failure := errors.New("selection fsync result lost")
	calls := 0
	effect := func(context.Context, BootstrapInput, []byte) (PreparedRuntime, error) {
		calls++
		return PreparedRuntime{}, failure
	}
	_, err := g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), generationTarget(g), effect)
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || !errors.Is(err, failure) || calls != 1 {
		t.Fatal("preparation lost its unknown effect", err, calls)
	}
	_, err = g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), generationTarget(g), effect)
	if !errors.As(err, &uncertain) || !errors.Is(err, failure) || calls != 1 {
		t.Fatal("uncertain preparation retried or erased its marker", err, calls)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = g.PrepareNext(t.Context(), nil, generationTarget(g))
	if !errors.As(err, &uncertain) {
		t.Fatal("closing checkpoint erased unresolved history", err)
	}
}
func TestNativeGenerationRejectsReplayedOrUnpublishedPreparation(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(fmt.Sprintf("publish%v", publish), func(t *testing.T) {
			_, g := generationFixture(t)
			prepare := generationEffectFixture(t, g)
			_, err := g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), generationTarget(g), func(ctx context.Context, input BootstrapInput, rego []byte) (PreparedRuntime, error) {
				if publish {
					return PreparedRuntime{ContainerRoot: g.root.Name(), GenerationRoot: filepath.Join(g.root.Name(), "generations", g.current.Generation), Record: g.current}, nil
				}
				result, err := prepare(ctx, input, rego)
				if err != nil {
					return result, err
				}
				// An ACK without its selected readback is not complete preparation.
				old, _ := json.Marshal(g.current)
				if err := os.WriteFile(filepath.Join(g.root.Name(), "current.json"), old, 0o600); err != nil {
					t.Fatal(err)
				}
				return result, nil
			})
			var uncertain *NativeEffectsUncertain
			if !errors.As(err, &uncertain) {
				t.Fatal("replayed/unpublished state returned usable authority", err)
			}
		})
	}
}
func TestNativeGenerationRootBoundaryAndClose(t *testing.T) {
	f, g := generationFixture(t)
	if _, err := SnapshotNativeGeneration(t.Context(), f.record, f.release, g.cid); err == nil {
		t.Fatal("ordinary executor obtained native checkpoint")
	}
	if _, err := g.PrepareNext(t.Context(), g.OriginalPolicy(), generationTarget(g)); err == nil {
		t.Fatal("ordinary executor prepared native generation")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.nextInput("new-controller", nil); err == nil {
		t.Fatal("closed checkpoint retained preparation")
	}
}

func TestNativeGenerationRejectsRefreshedEnvironmentOnAcknowledgedReadback(t *testing.T) {
	_, g := generationFixture(t)
	prepare := generationEffectFixture(t, g)
	_, err := g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), generationTarget(g), func(ctx context.Context, input BootstrapInput, rego []byte) (PreparedRuntime, error) {
		input.Environment["API_KEY"] = "unexpected-refreshed-value"
		return prepare(ctx, input, rego)
	})
	var uncertain *NativeEffectsUncertain
	if !errors.As(err, &uncertain) || g.environment["API_KEY"] != "test-secret" {
		t.Fatal("acknowledged preparation silently refreshed original secrets", err)
	}
}

func TestNativeGenerationCheckpointRejectsForeignBootstrapEvenWithMatchingFileHash(t *testing.T) {
	f, g := generationFixture(t)
	_ = g.Close()
	name := f.path("boundary/bootstrap.json")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var boundary boundaryConfig
	if err := json.Unmarshal(raw, &boundary); err != nil {
		t.Fatal(err)
	}
	boundary.BoundaryID = "foreign-box"
	raw, _ = json.Marshal(boundary)
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.record.Files["boundary/bootstrap.json"] = sha256Hex(raw)
	f.writeRecords(t)
	if checkpoint, err := snapshotGenerationAt(f.root, f.record, f.release, strings.Repeat("a", 64), f.owners); err == nil {
		_ = checkpoint.Close()
		t.Fatal("foreign private bootstrap selected")
	}
}

func TestNativeGenerationExplicitEmptyTaskPermitsFreshCredentialsWithoutInferringHistory(t *testing.T) {
	_, g := generationFixture(t)
	previous := cloneRuntimeRecord(g.current)
	target := GenerationTarget{ContainerID: g.cid, EmptyTaskStateObserved: true}
	prepared, err := g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), func(context.Context) (GenerationTarget, error) { return target, nil }, generationEffectFixture(t, g))
	if err != nil {
		t.Fatal("explicit empty-task observation blocked fresh generation", err)
	}
	if prepared.Record.Generation == previous.Generation || prepared.Record.SessionID == previous.SessionID || prepared.Record.SSHHostPublicKey == previous.SSHHostPublicKey || prepared.Record.Files["status/activation.key"] == previous.Files["status/activation.key"] {
		t.Fatal("explicit current inactivity reused old launch credentials")
	}
	if target.Status != "" || target.PID != 0 || target.Running {
		t.Fatal("empty task was rewritten into historical status")
	}
}

func TestNativeGenerationRefusesUnprovedEmptyTaskAndUnknownEvenWithObservationBit(t *testing.T) {
	for _, change := range []string{"missing", "unknown", "running", "pid", "cid"} {
		t.Run(change, func(t *testing.T) {
			_, g := generationFixture(t)
			target := GenerationTarget{ContainerID: g.cid, EmptyTaskStateObserved: true}
			switch change {
			case "missing":
				target.EmptyTaskStateObserved = false
			case "unknown":
				target.Status = "unknown"
			case "running":
				target.Running = true
			case "pid":
				target.PID = 42
			case "cid":
				target.ContainerID = strings.Repeat("b", 64)
			}
			calls := 0
			_, err := g.prepareNext(t.Context(), "current-controller", g.OriginalPolicy(), func(context.Context) (GenerationTarget, error) { return target, nil }, func(context.Context, BootstrapInput, []byte) (PreparedRuntime, error) {
				calls++
				return PreparedRuntime{}, nil
			})
			if err == nil || calls != 0 {
				t.Fatal("unproved task observation reached generation preparation", err, calls)
			}
		})
	}
}
