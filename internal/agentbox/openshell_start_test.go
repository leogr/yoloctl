package agentbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// Real Client inventory/inspect decoding via a finite subprocess. Native root
// is unavailable here: failure must precede launch/legacy effects. The legacy
// control supplies the retained-byte verifier read while proving the native
// gate adds no policy/catalog or native mutation requirement to that route.
func TestNativeStartActualAPIRefusesUnavailableQualificationWithoutLegacyEffects(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "native-unavailable"}[native], func(t *testing.T) {
			cfg := validConfig()
			inspector := fixtureLiveInspector(t, cfg, sandbox.EntrypointArgs(cfg.Workdir.ContainerPath(), false, "sleep", "infinity"), buildMounts(cfg))
			bin := os.Getenv("WORKDIR_TEST_FIXTURE")
			if native {
				d := openShellDetailsFixture(t)
				var mounts []map[string]any
				for _, m := range d.Mounts {
					mounts = append(mounts, map[string]any{"Source": m.Source, "Destination": m.Destination, "RW": !m.ReadOnly})
				}
				raw, err := json.Marshal([]any{map[string]any{"Id": strings.Repeat("a", 64), "Name": cfg.Name, "Path": d.Command[0], "Args": d.Command[1:], "Config": map[string]any{"Labels": d.Labels}, "HostConfig": map[string]any{"Privileged": false}, "Mounts": mounts}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "inspect.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				cfg.Capabilities = append(cfg.Capabilities, "not-a-current-catalog-capability")
			}
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$WORKDIR_TEST_FIXTURE/start-api.log"
case "$*" in
 *'nerdctl ps'*) printf '{"Names":"coder","Status":"Up"}\n' ;;
 *'nerdctl inspect'*) cat "$WORKDIR_TEST_FIXTURE/inspect.json" ;;
 *'binary=/opt/openshell/bin/openshell-sandbox'*'sha256sum --check --status'*) exit 0 ;;
 *) exit 42 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			a := LiveApplier{Client: inspector.Client, VerifyStorage: func(context.Context) error { calls++; return nil }}
			for _, observed := range []bool{false, true} {
				prepared, handled, err := a.PrepareNativeStart(t.Context(), cfg, observed)
				if prepared != nil {
					_ = prepared.Close()
				}
				if handled != native || (err != nil) != native || prepared != nil {
					t.Fatal("native API fell back or changed legacy qualification", observed, handled, err)
				}
			}

			raw, err := os.ReadFile(filepath.Join(bin, "start-api.log"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(raw), "binary=/opt/openshell/bin/openshell-sandbox"); got != !native {
				t.Fatalf("retained-byte verifier selected for wrong runtime: native=%v calls=%s", native, raw)
			}
			if calls != 0 || strings.Contains(string(raw), "nerdctl start") || strings.Contains(string(raw), "mkdir") || strings.Contains(string(raw), "sudo cat") || strings.Contains(string(raw), "systemctl") {
				t.Fatal("unqualified native API reached runtime or legacy effects", calls, string(raw))
			}
		})
	}
}

func TestNativeStartPostReconcileDoesNotReloadPolicyOrDuplicateForwards(t *testing.T) {
	// Planner seam only: actual new-generation readiness/forward publication is
	// required from the factory/native QA. Once those observed values match,
	// the shared post-start reconcile must perform no second mutation.
	cfg := validConfig()
	cfg.Ports = []Port{{Published: 9000, Target: 7000}, {Published: 9100, Target: 7100}}
	observed := ActualState{Exists: true, Image: mustResolveImage(t, cfg), PolicyContent: mustCompose(t, cfg), PortForwards: append([]Port(nil), cfg.Ports...), WorkloadPoolKnown: true, WorkloadPool: true, Runtime: RuntimeEvidence{Mode: RuntimeModeConfined, Protocol: openshell.HelperProtocol}}
	inspector := &fakeInspector{state: observed}
	applier := &fakeApplier{}
	result, err := ReconcileForStart(t.Context(), cfg, inspector, applier)
	if err != nil || result.ContainerCreated || result.ContainerRecreated || result.ContainerRestarted || len(applier.createCalls)+len(applier.recreateCalls)+len(applier.restartCalls)+len(applier.addCalls)+len(applier.removeCalls) != 0 {
		t.Fatal("native post-start reconciliation duplicated policy/forward effects", result, err, applier)
	}
}
