package agentbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
)

const disabledOperatorCID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func disabledOperatorClientFixture(t *testing.T, mode string) (*container.Client, string, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("YOLOCTL_DISABLED_OPERATOR_FIXTURE", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	entry := map[string]any{"ID": disabledOperatorCID, "Labels": map[string]string{}, "Spec": map[string]any{"ociVersion": "1.3.0", "root": map[string]string{"path": "rootfs"}, "mounts": []any{}}}
	if mode == "enabled" {
		inc := strings.Repeat("b", 32)
		entry["Labels"] = map[string]string{operator.IncarnationLabel: inc}
		entry["Spec"].(map[string]any)["mounts"] = []any{map[string]any{"type": "none", "source": operator.Root + "/" + inc, "destination": operator.Mount, "options": []string{"ro", "rprivate", "rbind"}}}
	}
	if mode == "conflicting" {
		entry["Labels"] = map[string]string{operator.IncarnationLabel: strings.Repeat("b", 32)}
	}
	raw, err := json.Marshal([]any{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	status := "0"
	if mode == "failed" {
		status = "24"
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$YOLOCTL_DISABLED_OPERATOR_FIXTURE/calls.log"
case "$*" in
 'shell yolo-fixture -- sudo nerdctl inspect --mode native ` + disabledOperatorCID + `')
  cat "$YOLOCTL_DISABLED_OPERATOR_FIXTURE/native.json"
  exit ` + status + `;;
 *) exit 97;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return container.NewWithOutput("fixture", io.Discard, io.Discard), filepath.Join(dir, "calls.log"), filepath.Join(dir, "native.json")
}

func TestNativeDisabledOperatorAbsenceBindsActualCaptureAndNeverRequeriesRemovedCID(t *testing.T) {
	client, log, data := disabledOperatorClientFixture(t, "disabled")
	original, err := client.NativeOperatorProjection(t.Context(), disabledOperatorCID, "selected")
	if err != nil {
		t.Fatal(err)
	}
	owned, inactive, prepared, withdrawn, closed := 0, 0, 0, 0, 0
	remaining := func(context.Context) error { return nil }
	p, err := prepareNativeDisabledOperatorAbsenceWith(t.Context(), original, disabledOperatorCID, "selected", func(context.Context) error { owned++; return nil }, func(context.Context) error { inactive++; return nil }, func(c context.Context, o, i func(context.Context) error) (*nativeOperatorRootLease, error) {
		prepared++
		if err := o(c); err != nil {
			return nil, err
		}
		if err := i(c); err != nil {
			return nil, err
		}
		return &nativeOperatorRootLease{withdraw: func(c context.Context, current func(context.Context) error) (openshell.OperatorRegistrationRootAbsenceResult, error) {
			withdrawn++
			if err := current(c); err != nil {
				return openshell.OperatorRegistrationRootAbsenceResult{}, err
			}
			return openshell.OperatorRegistrationRootAbsenceResult{ObservedRootAbsent: true, ParentSynced: true}, nil
		}, close: func() error { closed++; return nil }}, nil
	})
	if err != nil || owned < 2 || inactive < 2 || prepared != 1 {
		t.Fatal("original disabled selection/initial proofs not retained", err)
	}
	// The later suffix has only original metadata plus independent remaining
	// proof. Its actual Client can no longer inspect anything successfully.
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	if err := p.withdraw(t.Context(), remaining); err != nil || withdrawn != 1 {
		t.Fatal("disabled CID absence failed after source CID disappeared", err)
	}
	raw, err := os.ReadFile(log)
	if err != nil || strings.Count(string(raw), "inspect --mode native") != 2 {
		t.Fatal("post-rm suffix queried removed CID", err, string(raw))
	}
	if err := p.withdraw(t.Context(), remaining); err == nil || withdrawn != 1 {
		t.Fatal("completed disabled absence retried")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil || closed != 1 {
		t.Fatal("original prefix ownership closed twice", err)
	}
}

func TestNativeDisabledOperatorAbsenceRejectsUncapturedEnabledAndMismatchedMetadataBeforePrefix(t *testing.T) {
	for _, mode := range []string{"nil", "zero", "enabled", "wrong-cid", "wrong-box", "failed", "conflicting"} {
		t.Run(mode, func(t *testing.T) {
			client, _, _ := disabledOperatorClientFixture(t, mode)
			original, readErr := client.NativeOperatorProjection(t.Context(), disabledOperatorCID, "selected")
			if mode == "failed" || mode == "conflicting" {
				if readErr == nil || original != nil {
					t.Fatal("bad actual metadata acquired a capture")
				}
			} else if readErr != nil {
				t.Fatal(readErr)
			}
			cid, box := disabledOperatorCID, "selected"
			if mode == "nil" {
				original = nil
			}
			if mode == "zero" {
				original = new(container.NativeOperatorProjection)
			}
			if mode == "wrong-cid" {
				cid = strings.Repeat("c", 64)
			}
			if mode == "wrong-box" {
				box = "other"
			}
			prepared := 0
			noop := func(context.Context) error { return nil }
			got, err := prepareNativeDisabledOperatorAbsenceWith(t.Context(), original, cid, box, noop, noop, func(context.Context, func(context.Context) error, func(context.Context) error) (*nativeOperatorRootLease, error) {
				prepared++
				return nil, nil
			})
			if err == nil || got != nil || prepared != 0 {
				t.Fatal("uncaptured/enabled/foreign selection reached prefix preparation", err, prepared)
			}
		})
	}
}

func TestNativeDisabledOperatorAbsencePreservesPresentRootAndConsumedFailures(t *testing.T) {
	for _, failure := range []string{"owner", "inactive", "present-root", "remaining", "prefix-failed", "partial-result", "closed"} {
		t.Run(failure, func(t *testing.T) {
			client, _, _ := disabledOperatorClientFixture(t, "disabled")
			original, err := client.NativeOperatorProjection(t.Context(), disabledOperatorCID, "selected")
			if err != nil {
				t.Fatal(err)
			}
			boom := errors.New("scripted failed actual proof or present operators root")
			owned, inactive := func(context.Context) error { return nil }, func(context.Context) error { return nil }
			if failure == "owner" {
				owned = func(context.Context) error { return boom }
			}
			if failure == "inactive" {
				inactive = func(context.Context) error { return boom }
			}
			prepared, withdrawals := 0, 0
			p, err := prepareNativeDisabledOperatorAbsenceWith(t.Context(), original, disabledOperatorCID, "selected", owned, inactive, func(context.Context, func(context.Context) error, func(context.Context) error) (*nativeOperatorRootLease, error) {
				prepared++
				if failure == "present-root" {
					return nil, boom
				}
				return &nativeOperatorRootLease{withdraw: func(context.Context, func(context.Context) error) (openshell.OperatorRegistrationRootAbsenceResult, error) {
					withdrawals++
					if failure == "prefix-failed" {
						// Claimed current postconditions cannot turn a failed or
						// lost reply into an acknowledged completion.
						return openshell.OperatorRegistrationRootAbsenceResult{ObservedRootAbsent: true, ParentSynced: true}, boom
					}
					if failure == "partial-result" {
						return openshell.OperatorRegistrationRootAbsenceResult{ObservedRootAbsent: true}, nil
					}
					return openshell.OperatorRegistrationRootAbsenceResult{ObservedRootAbsent: true, ParentSynced: true}, nil
				}, close: func() error { return nil }}, nil
			})
			if failure == "owner" || failure == "inactive" || failure == "present-root" {
				if !errors.Is(err, boom) || p != nil {
					t.Fatal("pre-effect refusal fabricated cold success", err)
				}
				if failure != "present-root" && prepared != 0 {
					t.Fatal("failed initial proof reached prefix")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			remaining := func(context.Context) error { return nil }
			if failure == "remaining" {
				remaining = func(context.Context) error { return boom }
			}
			if failure == "closed" {
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.withdraw(t.Context(), remaining); err == nil {
				t.Fatal("failed/current/partial suffix became completion")
			}
			before := withdrawals
			if err := p.withdraw(t.Context(), func(context.Context) error { return nil }); err == nil || withdrawals != before {
				t.Fatal("failed disabled-CID absence retried")
			}
			if failure == "remaining" && withdrawals != 0 {
				t.Fatal("failed consumer proof reached prefix sync")
			}
		})
	}
}
