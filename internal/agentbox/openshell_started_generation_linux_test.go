package agentbox

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/openshell"
)

// Data/protocol fixture only. These flags are not actual Start/preparation
// acknowledgements and cannot bypass the production native-root entry guard.
func nativeStartedGenerationFixture(t *testing.T) (*nativeStartedGeneration, OpenShellRuntime) {
	t.Helper()
	current, d, raw, manifest := nativeRuntimeFixture(t)
	observed, err := validateOpenShellRuntime(current, d.Name, d, raw, manifest)
	if err != nil {
		t.Fatal(err)
	}
	record := observed.Record
	record.Files = maps.Clone(record.Files)
	g := &nativeStartedGeneration{cid: observed.ContainerID, startAttempted: true, startAcknowledged: true, prepared: openshell.PreparedRuntime{ContainerRoot: observed.ControlRoot, GenerationRoot: filepath.Join(observed.ControlRoot, "generations", record.Generation), Record: record}}
	return g, observed
}

func TestNativeStartedGenerationCleanupRejectsUnknownOrMissingOriginalAttempt(t *testing.T) {
	for _, change := range []string{"nil", "no-start-attempt", "no-start-ack", "consumed", "lost-activation-result", "uninvoked-handle", "typed-failure", "cid", "target-cid", "target-pid", "target-time", "partial-target"} {
		t.Run(change, func(t *testing.T) {
			g, _ := nativeStartedGenerationFixture(t)
			cause := errors.New("lost original reply")
			switch change {
			case "nil":
				g = nil
			case "no-start-attempt":
				g.startAttempted = false
			case "no-start-ack":
				g.startAcknowledged = false
			case "consumed":
				g.cleanupAttempted = true
			case "lost-activation-result":
				g.activationAttempted = true
			case "uninvoked-handle":
				g.activation = &openshell.NativeActivation{}
			case "typed-failure":
				g.failure = &NativeEffectsUncertain{Cause: cause}
			case "cid":
				g.cid = "incomplete"
			case "target-cid":
				g.target = openshell.ActivationTarget{ContainerID: strings.Repeat("b", 64)}
			case "target-pid":
				g.target = openshell.ActivationTarget{ContainerID: g.cid, PID: 1}
			case "target-time":
				g.target = openshell.ActivationTarget{ContainerID: g.cid, PID: 42}
			case "partial-target":
				g.target = openshell.ActivationTarget{PID: 42}
			}
			err := (LiveApplier{}).stopNativeStartedGeneration(t.Context(), "fixture", g)
			if err == nil {
				t.Fatal("unknown/missing original launch permitted cleanup", change)
			}
			if change == "typed-failure" {
				var uncertain *NativeEffectsUncertain
				if !errors.As(err, &uncertain) || !errors.Is(err, cause) {
					t.Fatal("original launch uncertainty lost", err)
				}
			}
		})
	}
	g, _ := nativeStartedGenerationFixture(t)
	if err := g.cleanupIdentity(); err == nil {
		t.Fatal("data shape substituted for original SDK preparation")
	}
	if err := (LiveApplier{}).stopNativeStartedGeneration(t.Context(), "fixture", g); err == nil || g.cleanupAttempted {
		t.Fatal("data flags bypassed actual native-root guard", err)
	}
	if formatted := fmt.Sprintf("%v %#v", g, g); !strings.Contains(formatted, "redacted") || strings.Contains(formatted, g.cid) {
		t.Fatal("private generation formatting exposed state", formatted)
	}
}

func TestNativeStartedGenerationPreparedSelectionRejectsEveryChangedBinding(t *testing.T) {
	for _, change := range []string{"id", "root", "launch", "generation-root", "record-controller", "record-policy", "record-generation", "record-key"} {
		t.Run(change, func(t *testing.T) {
			g, observed := nativeStartedGenerationFixture(t)
			if !g.matchesSelected(observed) {
				t.Fatal("same original data did not match")
			}
			observed.Record.Files = maps.Clone(observed.Record.Files)
			switch change {
			case "id":
				observed.ContainerID = strings.Repeat("b", 64)
			case "root":
				observed.ControlRoot += "-other"
			case "launch":
				observed.Launch.Workdir = "/other"
			case "generation-root":
				g.prepared.GenerationRoot += "-other"
			case "record-controller":
				observed.Record.Launch.Binding.ControllerGeneration = "different-controller"
			case "record-policy":
				observed.Record.Launch.Binding.PolicySHA256 = strings.Repeat("b", 64)
			case "record-generation":
				observed.Record.Generation = "different-generation"
			case "record-key":
				observed.Record.Files["status/activation.key"] = strings.Repeat("b", 64)
			}
			if g.matchesSelected(observed) {
				t.Fatal("foreign original generation material matched cleanup", change)
			}
		})
	}
}

func TestNativeStartedGenerationStopRequiresOriginalSDKPreparationBeforeEngine(t *testing.T) {
	for _, kind := range []string{"nil", "zero", "closed"} {
		t.Run(kind, func(t *testing.T) {
			client, log, set, read := nativeStopActualAdapterFixture(t, 0)
			selected, _, err := read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			set(nativeCleanupEmptyTaskJSON)
			checks := nativeRuntimeStopChecks{selected: selected, read: read, resources: func(context.Context) error { return nil }}
			var preparation *openshell.NativePreparedGeneration
			if kind != "nil" {
				preparation = &openshell.NativePreparedGeneration{}
			}
			if kind == "closed" {
				_ = preparation.Close()
			}
			endpoints := 0
			err = completeNativeGenerationStop(t.Context(), preparation, func(c context.Context) error { return client.Stop(c, selected.ContainerID) }, checks.owned, checks.inactive, func(context.Context) error { endpoints++; return nil })
			var uncertain *NativeEffectsUncertain
			commands, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !errors.As(err, &uncertain) || strings.Contains(string(commands), "nerdctl stop") || strings.Contains(string(commands), "nerdctl rm") || endpoints != 0 {
				t.Fatal("missing original SDK preparation reached engine/endpoint suffix", err, string(commands), endpoints)
			}
		})
	}
}

func TestNativeStartedGenerationNamespaceCompletionRetainsPostACKContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), nativeStopContextKey{}, "admitted"))
	defer cancel()
	stops, checks := 0, 0
	check := func(c context.Context) error {
		checks++
		deadline, ok := c.Deadline()
		if c.Err() != nil || c.Value(nativeStopContextKey{}) != "admitted" || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
			t.Fatal("post-ACK namespace completion lost bounded admission context")
		}
		return nil
	}
	// Scripted successful retirement contract; actual namespace/engine runtime
	// remains unproved. This checks the shared route after extraction/refactor.
	err := completeNativeStopRoute(ctx, true, func(context.Context) error { stops++; return nil }, check, func(context.Context) error { cancel(); return nil }, check)
	if err != nil || stops != 0 || checks != 2 {
		t.Fatal("completed namespace cleanup repeated Stop or inherited canceled transport", err, stops, checks)
	}
}
