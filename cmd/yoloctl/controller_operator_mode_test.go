package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
)

func TestLegacyOperatorRestoreCannotDowngradeNativeOrUnknownRuntime(t *testing.T) {
	for _, kind := range []string{"native-label", "native-argv", "native-mount", "read-failure", "changed-cid", "changed-name", "changed-label", "nil-runtime", "canceled", "legacy-running", "legacy-stopped-wrapper"} {
		t.Run(kind, func(t *testing.T) {
			endpoint := operator.Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
			d := &container.Details{ID: strings.Repeat("b", 64), Name: "box", Labels: map[string]string{agentbox.LabelSpace: "space", agentbox.LabelAgentbox: "box", operator.IncarnationLabel: endpoint.Incarnation}, Running: true}
			var failure error
			switch kind {
			case "native-label":
				d.Labels[openshell.LabelProtocol] = ""
			case "native-argv":
				d.Command = []string{openshell.HelperBinary}
			case "native-mount":
				d.Mounts = []container.DetailMount{{Destination: openshell.NativeProjection}}
			case "read-failure":
				failure = errors.New("unavailable actual inventory")
			case "changed-cid":
				d.ID = "short"
			case "changed-name":
				d.Name = "other"
			case "changed-label":
				d.Labels[operator.IncarnationLabel] = strings.Repeat("c", 32)
			case "nil-runtime":
				d = nil
			case "legacy-stopped-wrapper":
				d.Running = false
				d.Command = []string{"/custom/exec-wrapper", "openshell-sandbox"}
			}
			ctx := t.Context()
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			inspections, preparations := 0, 0
			err := restoreLegacyOperatorEndpoint(ctx, "space", endpoint, func(_ context.Context, box string) (*container.Details, error) {
				inspections++
				if box != "box" {
					t.Fatal("restore inspected another box")
				}
				return d, failure
			}, func(e operator.Endpoint) error {
				preparations++
				if e != endpoint {
					t.Fatal("restore changed identity")
				}
				return nil
			})
			if strings.HasPrefix(kind, "legacy-") {
				if err != nil || inspections != 1 || preparations != 1 {
					t.Fatal("supported existing legacy restoration changed", err, inspections, preparations)
				}
			} else if err == nil || preparations != 0 {
				t.Fatal("native/uncertain restoration reached legacy producer", err, preparations)
			}
			if strings.HasPrefix(kind, "native-") && !errors.Is(err, operator.ErrNativeRegistration) {
				t.Fatal("native restoration lost typed dispatch refusal", err)
			}
			if kind == "read-failure" && !errors.Is(err, failure) {
				t.Fatal("restore replaced actual read failure", err)
			}
		})
	}
}

// A native operator box found at controller startup (after a VM stop/start)
// is restored through the native publisher with its labelled qualification;
// a legacy box keeps the legacy branch; a native box without a usable
// qualification, or with another identity, is refused without any effect.
func TestOperatorRestoreRoutesNativeBoxesToNativeRestore(t *testing.T) {
	endpoint := operator.Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	qualification := strings.Repeat("d", 64)
	for _, kind := range []string{"native", "legacy", "native-no-qualification", "native-changed-label"} {
		t.Run(kind, func(t *testing.T) {
			d := &container.Details{ID: strings.Repeat("b", 64), Name: "box", Labels: map[string]string{agentbox.LabelSpace: "space", agentbox.LabelAgentbox: "box", operator.IncarnationLabel: endpoint.Incarnation}}
			switch kind {
			case "native":
				d.Labels[openshell.LabelQualification] = qualification
			case "native-no-qualification":
				d.Labels[openshell.LabelProtocol] = "2"
			case "native-changed-label":
				d.Labels[openshell.LabelQualification] = qualification
				d.Labels[operator.IncarnationLabel] = strings.Repeat("c", 32)
			}
			var restored []operator.RegistrationIdentity
			preparations := 0
			err := restoreOperatorEndpoint(t.Context(), "space", endpoint,
				func(context.Context, string) (*container.Details, error) { return d, nil },
				func(operator.Endpoint) error { preparations++; return nil },
				func(_ context.Context, i operator.RegistrationIdentity) error {
					restored = append(restored, i)
					return nil
				})
			switch kind {
			case "native":
				want := operator.RegistrationIdentity{Endpoint: endpoint, Mode: operator.RegistrationOpenShellNative, HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: qualification}
				if err != nil || preparations != 0 || len(restored) != 1 || restored[0] != want {
					t.Fatalf("native restore: %v, %d legacy, %+v", err, preparations, restored)
				}
			case "legacy":
				if err != nil || preparations != 1 || len(restored) != 0 {
					t.Fatalf("legacy restore: %v, %d legacy, %+v", err, preparations, restored)
				}
			default:
				if err == nil || preparations != 0 || len(restored) != 0 {
					t.Fatalf("refused restore had effects: %v, %d legacy, %+v", err, preparations, restored)
				}
			}
		})
	}
}
