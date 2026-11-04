package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

const operatorProjectionCID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func operatorProjectionFixture() map[string]any {
	endpoint := operator.Endpoint{Box: "selected", Incarnation: strings.Repeat("a", 32)}
	entry := nativeMountInspectFixture(operatorProjectionCID, endpoint.Directory())
	entry["Labels"].(map[string]any)[operator.IncarnationLabel] = endpoint.Incarnation
	mount := entry["Spec"].(map[string]any)["mounts"].([]any)[0].(map[string]any)
	// Exact Linux ProcessFlagV(src:dst:ro) output in pinned nerdctl2.3.5:
	// implicit rbind is appended after default fstype="none" is selected.
	mount["type"] = "none"
	mount["destination"] = operator.Mount
	mount["options"] = []string{"ro", "rprivate", "rbind"}
	return entry
}

func operatorProjectionJSON(t *testing.T, entry map[string]any) string {
	t.Helper()
	raw, err := json.Marshal([]any{entry})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func operatorProjectionCalls(t *testing.T, log string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) != nil || len(args) < 5 {
			t.Fatal("malformed private command fixture log")
		}
		calls = append(calls, args[5:])
	}
	return calls
}

func TestNativeOperatorProjectionActualAdapterBindsOriginalCIDLabelAndOCIMount(t *testing.T) {
	entry := operatorProjectionFixture()
	// A parent mount is not itself an operator projection. These observations
	// do not decide kernel visibility or authorize deleting either directory.
	spec := entry["Spec"].(map[string]any)
	spec["mounts"] = append([]any{map[string]any{"type": "tmpfs", "source": "tmpfs", "destination": "/run"}}, spec["mounts"].([]any)...)
	// A Docker summary and nerdctl mount label are deliberately misleading.
	entry["Mounts"] = []any{map[string]any{"Source": "/wrong-summary", "Destination": operator.Mount, "RW": true}}
	entry["Labels"].(map[string]any)["nerdctl/mounts"] = `[{"Source":"/wrong-label-summary"}]`
	client, log := nativeMountCommandFixture(t, map[string]any{"inspects": []string{operatorProjectionJSON(t, entry)}})
	observation, err := client.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected")
	if err != nil {
		t.Fatal("actual native projection refused", err)
	}
	selection, err := observation.Selection()
	if err != nil || !selection.Enabled || selection.ContainerID != operatorProjectionCID || selection.Box != "selected" || selection.Endpoint != (operator.Endpoint{Box: "selected", Incarnation: strings.Repeat("a", 32)}) || selection.Mount.Source != selection.Endpoint.Directory() || selection.Mount.Destination != operator.Mount || selection.Mount.Type != "none" || !reflect.DeepEqual(selection.Mount.Options, []string{"ro", "rprivate", "rbind"}) {
		t.Fatal("actual projection substituted desired/summary evidence", err)
	}
	selection.Mount.Options[0] = "mutated-copy"
	repeated, err := observation.Selection()
	if err != nil || repeated.Mount.Options[0] != "ro" {
		t.Fatal("caller mutated original observation", err)
	}
	if strings.Contains(fmt.Sprintf("%#v %#v", observation, repeated), "fixture-secret-never-return") {
		t.Fatal("projection rendered private inspection text")
	}
	want := [][]string{{"inspect", "--mode", "native", operatorProjectionCID}, {"inspect", "--mode", "native", operatorProjectionCID}}
	if got := operatorProjectionCalls(t, log); !reflect.DeepEqual(got, want) {
		t.Fatal("projection used name/filtered/summary query or retry", got)
	}
}

func TestNativeOperatorProjectionAcceptsExplicitBindAndOriginalUppercaseIncarnation(t *testing.T) {
	entry := operatorProjectionFixture()
	incarnation := strings.Repeat("B", 32) // Endpoint.Validate historically permits hex case.
	entry["Labels"].(map[string]any)[operator.IncarnationLabel] = incarnation
	mount := entry["Spec"].(map[string]any)["mounts"].([]any)[0].(map[string]any)
	mount["type"], mount["source"], mount["options"] = "bind", operator.Root+"/"+incarnation, []string{"bind", "ro", "rprivate"}
	client, _ := nativeMountCommandFixture(t, map[string]any{"inspects": []string{operatorProjectionJSON(t, entry)}})
	observation, err := client.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected")
	if err != nil {
		t.Fatal("valid explicit bind profile refused", err)
	}
	selection, err := observation.Selection()
	if err != nil || !selection.Enabled || selection.Endpoint.Incarnation != incarnation || selection.Mount.Type != "bind" {
		t.Fatal("original incarnation or explicit bind changed", err)
	}
}

func TestNativeOperatorProjectionDisabledIsCapturedCIDOnlyNotEmptyRegistrations(t *testing.T) {
	for _, label := range []string{"missing", "null", "empty-map", "empty-value"} {
		t.Run(label, func(t *testing.T) {
			entry := operatorProjectionFixture()
			entry["Spec"].(map[string]any)["mounts"] = nil
			switch label {
			case "missing":
				delete(entry, "Labels")
			case "null":
				entry["Labels"] = nil
			case "empty-map":
				entry["Labels"] = map[string]any{}
			case "empty-value":
				entry["Labels"] = map[string]any{operator.IncarnationLabel: ""}
			}
			client, _ := nativeMountCommandFixture(t, map[string]any{"inspects": []string{operatorProjectionJSON(t, entry)}})
			observed, err := client.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected")
			if err != nil {
				t.Fatal("valid disabled CID refused", err)
			}
			selection, err := observed.Selection()
			if err != nil || selection.Enabled || selection.ContainerID != operatorProjectionCID || selection.Box != "selected" || selection.Endpoint != (operator.Endpoint{}) || selection.Mount.Source != "" {
				t.Fatal("disabled projection invented an endpoint or lost fixed identity", err)
			}
		})
	}
	for _, empty := range []*NativeOperatorProjection{nil, {}} {
		if _, err := empty.Selection(); err == nil {
			t.Fatal("zero/uncaptured projection fabricated disabled evidence")
		}
	}
}

func TestNativeOperatorProjectionRejectsConflictingOrIncompleteRecordedBind(t *testing.T) {
	for _, change := range []string{"no-label", "wrong-label", "no-bind", "wrong-source", "source-alias", "wrong-type", "rw", "ro-rw", "missing-ro", "missing-bind-option", "duplicate-ro", "duplicate-bind-option", "duplicate-target", "target-alias", "child-target"} {
		t.Run(change, func(t *testing.T) {
			entry := operatorProjectionFixture()
			spec := entry["Spec"].(map[string]any)
			mount := spec["mounts"].([]any)[0].(map[string]any)
			switch change {
			case "no-label":
				delete(entry["Labels"].(map[string]any), operator.IncarnationLabel)
			case "wrong-label":
				entry["Labels"].(map[string]any)[operator.IncarnationLabel] = strings.Repeat("b", 32)
			case "no-bind":
				spec["mounts"] = nil
			case "wrong-source":
				mount["source"] = "/foreign"
			case "source-alias":
				mount["source"] = mount["source"].(string) + "/"
			case "wrong-type":
				mount["type"] = "tmpfs"
			case "rw":
				mount["options"] = []string{"rbind", "rw"}
			case "ro-rw":
				mount["options"] = []string{"rbind", "ro", "rw"}
			case "missing-ro":
				mount["options"] = []string{"rbind"}
			case "missing-bind-option":
				mount["options"] = []string{"ro"}
			case "duplicate-ro":
				mount["options"] = []string{"rbind", "ro", "ro"}
			case "duplicate-bind-option":
				mount["options"] = []string{"rbind", "bind", "ro"}
			case "duplicate-target":
				spec["mounts"] = []any{mount, mount}
			case "target-alias":
				mount["destination"] = operator.Mount + "/"
			case "child-target":
				spec["mounts"] = []any{mount, map[string]any{"type": "bind", "source": "/other", "destination": operator.Socket, "options": []string{"rbind", "ro"}}}
			}
			client, log := nativeMountCommandFixture(t, map[string]any{"inspects": []string{operatorProjectionJSON(t, entry)}})
			if got, err := client.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected"); err == nil || got != nil {
				t.Fatal("incomplete/conflicting recorded projection accepted", change, err)
			}
			if len(operatorProjectionCalls(t, log)) != 1 {
				t.Fatal("invalid projection retried or used a fallback")
			}
		})
	}
}

func TestNativeOperatorProjectionRefusesAmbiguousJSONActualAdapter(t *testing.T) {
	valid := operatorProjectionJSON(t, operatorProjectionFixture())
	for name, raw := range map[string]string{
		"labels-alias":          strings.Replace(valid, `"Labels"`, `"labels"`, 1),
		"duplicate-labels":      strings.Replace(valid, `"Labels":`, `"Labels":{},"Labels":`, 1),
		"label-alias":           strings.Replace(valid, operator.IncarnationLabel, strings.ToUpper(operator.IncarnationLabel), 1),
		"duplicate-incarnation": strings.Replace(valid, `"`+operator.IncarnationLabel+`":`, `"`+operator.IncarnationLabel+`":"","`+operator.IncarnationLabel+`":`, 1),
		"label-null":            strings.Replace(valid, `"`+operator.IncarnationLabel+`":"`+strings.Repeat("a", 32)+`"`, `"`+operator.IncarnationLabel+`":null`, 1),
		"invalid-label":         strings.Replace(valid, `"`+operator.IncarnationLabel+`":"`+strings.Repeat("a", 32)+`"`, `"`+operator.IncarnationLabel+`":"not-an-incarnation"`, 1),
		"wrong-cid":             strings.Replace(valid, operatorProjectionCID, strings.Repeat("b", 64), 1),
		"empty-inspect":         `[]`,
		"missing-native-spec":   `[{"ID":"` + operatorProjectionCID + `","Labels":{},"Mounts":[]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := nativeMountCommandFixture(t, map[string]any{"inspects": []string{raw}})
			if got, err := client.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected"); err == nil || got != nil {
				t.Fatal("ambiguous/summary-only inspection became projection evidence", err)
			}
		})
	}
}

func TestNativeOperatorProjectionRefusesChangedObservationAndFailedReplies(t *testing.T) {
	first := operatorProjectionJSON(t, operatorProjectionFixture())
	for _, change := range []string{"label-and-mount", "root", "unrelated-mount", "first-failed", "second-failed", "overflow"} {
		t.Run(change, func(t *testing.T) {
			entry := operatorProjectionFixture()
			config := map[string]any{}
			switch change {
			case "label-and-mount":
				entry["Labels"].(map[string]any)[operator.IncarnationLabel] = strings.Repeat("b", 32)
				entry["Spec"].(map[string]any)["mounts"].([]any)[0].(map[string]any)["source"] = operator.Root + "/" + strings.Repeat("b", 32)
			case "root":
				entry["Spec"].(map[string]any)["root"].(map[string]any)["path"] = "changed-root"
			case "unrelated-mount":
				spec := entry["Spec"].(map[string]any)
				spec["mounts"] = append(spec["mounts"].([]any), map[string]any{"type": "tmpfs", "destination": "/other"})
			case "first-failed":
				config["fail_inspect_at"] = 1
			case "second-failed":
				config["fail_inspect_at"] = 2
			case "overflow":
				config["overflow_inspect"] = true
			}
			config["inspects"] = []string{first, operatorProjectionJSON(t, entry)}
			client, log := nativeMountCommandFixture(t, config)
			got, err := client.NativeOperatorProjection(t.Context(), operatorProjectionCID, "selected")
			if err == nil || got != nil {
				t.Fatal("changed/failed native evidence returned an observation", change, err)
			}
			if strings.HasSuffix(change, "failed") {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 24 {
					t.Fatal("actual command failure discarded", err)
				}
			}
			calls := 2
			if change == "first-failed" || change == "overflow" {
				calls = 1
			}
			if len(operatorProjectionCalls(t, log)) != calls {
				t.Fatal("failed/change observation retried or probed")
			}
		})
	}
}

func TestNativeOperatorProjectionRejectsInvalidSelectionBeforeCommand(t *testing.T) {
	for _, input := range [][2]string{{"selected", "selected"}, {strings.Repeat("A", 64), "selected"}, {operatorProjectionCID, "../selected"}} {
		client, log := nativeMountCommandFixture(t, map[string]any{})
		if got, err := client.NativeOperatorProjection(t.Context(), input[0], input[1]); err == nil || got != nil || len(operatorProjectionCalls(t, log)) != 0 {
			t.Fatal("invalid original identity reached backend", err)
		}
	}
	client, log := nativeMountCommandFixture(t, map[string]any{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := client.NativeOperatorProjection(ctx, operatorProjectionCID, "selected"); !errors.Is(err, context.Canceled) || got != nil || len(operatorProjectionCalls(t, log)) != 0 {
		t.Fatal("cancelled read reached backend or became disabled", err)
	}
}
