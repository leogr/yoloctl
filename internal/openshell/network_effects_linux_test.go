package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func nativeNFTEffectFixture(t *testing.T) (nativeEgressPlan, []byte, []byte, []byte) {
	t.Helper()
	plan, _, _ := nativeEgressPlanFixture(t)
	policy, err := prepareEgress(plan.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	objects := append([]nftObject{{"metainfo": nftObject{"version": "1.0.9", "json_schema_version": 1}}}, policy.expectedObjects()...)
	for _, object := range objects {
		if table, ok := object["table"].(nftObject); ok {
			table["handle"] = 17
		}
	}
	readback, err := json.Marshal(map[string]any{"nftables": objects})
	if err != nil {
		t.Fatal(err)
	}
	absent := []byte(`{"nftables":[{"metainfo":{"version":"1.0.9","json_schema_version":1}}]}`)
	present := []byte(`{"nftables":[{"metainfo":{"version":"1.0.9","json_schema_version":1}},{"table":{"family":"inet","name":"` + EgressTable + `","handle":17}}]}`)
	return plan, readback, absent, present
}

type nativeNFTStep struct {
	args, input string
	output      []byte
	err         error
}

func scriptedNativeNFT(t *testing.T, steps []nativeNFTStep) nativeNFTCommand {
	t.Helper()
	n := 0
	t.Cleanup(func() {
		if n != len(steps) {
			t.Errorf("nft sequence stopped at %d/%d", n, len(steps))
		}
	})
	return func(_ context.Context, args []string, input io.Reader) ([]byte, error) {
		t.Helper()
		if n >= len(steps) {
			t.Fatalf("unexpected nft command after sequence: %q", args)
		}
		step := steps[n]
		n++
		var raw []byte
		if input != nil {
			var err error
			raw, err = io.ReadAll(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		if strings.Join(args, " ") != step.args || string(raw) != step.input {
			t.Fatalf("nft step %d: argv=%q input=%q; want argv=%q input=%q", n, args, raw, step.args, step.input)
		}
		return step.output, step.err
	}
}

func TestNativeEgressEffectsApplyUsesOnlyCompleteFinalPlan(t *testing.T) {
	plan, readback, absent, _ := nativeNFTEffectFixture(t)
	policy, err := GenerateEgressPolicy(plan.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(policy, "create table inet "+EgressTable+" { comment \"yoloctl:"+plan.Inputs.OwnerSHA256+"\"; }\ntable inet "+EgressTable+" {\n") || !strings.Contains(policy, "203.0.113.0/24") {
		t.Fatal("plan lacks exclusive create or namespace-discovered prefix")
	}
	execute := scriptedNativeNFT(t, []nativeNFTStep{{args: "-j list tables", output: absent}, {args: "-f -", input: policy}, {args: "-j list table inet " + EgressTable, output: readback}})
	got, err := applyPlannedNativeEgress(context.Background(), plan, plan.Namespace, plan.Observed, execute)
	if err != nil || got.Schema != BootstrapSchema || got.Namespace != plan.Namespace || !sameNativeEgressInputs(got.Inputs, plan.Inputs) || string(got.Readback) != string(readback) {
		t.Fatalf("apply result: %+v, %v", got, err)
	}
	got.Inputs.LocalPrefixes[0] = netip.MustParsePrefix("198.51.100.0/24")
	got.Readback[0] = '!'
	if reflect.DeepEqual(got.Inputs.LocalPrefixes, plan.Inputs.LocalPrefixes) || readback[0] == '!' {
		t.Fatal("result aliases the selected plan/readback")
	}
}

func TestNativeEgressEffectsApplyRefusesChangedInputsAndExistingTable(t *testing.T) {
	plan, _, _, present := nativeNFTEffectFixture(t)
	for _, change := range []string{"added-prefix", "removed-prefix", "namespace", "rules"} {
		t.Run(change, func(t *testing.T) {
			candidate, target, observed := plan, plan.Namespace, plan.Observed
			switch change {
			case "added-prefix":
				observed = append(append([]netip.Prefix(nil), observed...), netip.MustParsePrefix("198.51.100.0/24"))
			case "removed-prefix":
				observed = observed[:1]
			case "namespace":
				target.Inode++
			case "rules":
				candidate.RulesSHA256 = strings.Repeat("a", 64)
			}
			got, err := applyPlannedNativeEgress(context.Background(), candidate, target, observed, scriptedNativeNFT(t, nil))
			if err == nil || got.Schema != 0 {
				t.Fatal("changed selection reached effects", err)
			}
		})
	}
	got, err := applyPlannedNativeEgress(context.Background(), plan, plan.Namespace, plan.Observed, scriptedNativeNFT(t, []nativeNFTStep{{args: "-j list tables", output: present}}))
	if err == nil || got.Schema != 0 {
		t.Fatal("existing table adopted or replaced", err)
	}
}

func TestNativeEgressEffectsFailedApplyACKNeverRetriesOrFallsBack(t *testing.T) {
	for _, where := range []string{"inventory", "write", "readback", "wrong-readback"} {
		t.Run(where, func(t *testing.T) {
			plan, _, absent, _ := nativeNFTEffectFixture(t)
			policy, _ := GenerateEgressPolicy(plan.Inputs)
			lost := errors.New("lost reply: no such table")
			steps := []nativeNFTStep{{args: "-j list tables", output: absent}}
			if where == "inventory" {
				steps[0].err = lost
			} else {
				steps = append(steps, nativeNFTStep{args: "-f -", input: policy})
				if where == "write" {
					steps[1].err = lost
				} else {
					steps = append(steps, nativeNFTStep{args: "-j list table inet " + EgressTable, output: absent})
					if where == "readback" {
						steps[2].err = lost
					}
				}
			}
			got, err := applyPlannedNativeEgress(context.Background(), plan, plan.Namespace, plan.Observed, scriptedNativeNFT(t, steps))
			var uncertain *NativeEffectsUncertain
			// An nft failure is a plain error, never uncertain (leogr's rule).
			if err == nil || got.Schema != 0 || errors.As(err, &uncertain) {
				t.Fatalf("lost apply outcome mishandled: %+v, %v", got, err)
			}
		})
	}
}

func TestNativeEgressEffectsRetireByVerifiedHandleOrProvenAbsence(t *testing.T) {
	plan, readback, absent, present := nativeNFTEffectFixture(t)
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial-never-installed", true: "owned-table"}[exists], func(t *testing.T) {
			steps := []nativeNFTStep{{args: "-j list tables", output: absent}}
			if exists {
				steps[0].output = present
				steps = append(steps, nativeNFTStep{args: "-j list table inet " + EgressTable, output: readback}, nativeNFTStep{args: "delete table inet handle 17"}, nativeNFTStep{args: "-j list tables", output: absent})
			}
			got, err := retirePlannedNativeEgress(context.Background(), plan, plan.Namespace, scriptedNativeNFT(t, steps))
			if err != nil || !got.TableAbsent || got.Deleted != exists || got.Namespace != plan.Namespace || got.RulesSHA256 != plan.RulesSHA256 {
				t.Fatalf("retire observation: %+v, %v", got, err)
			}
		})
	}
}

func TestNativeEgressEffectsRetirementRetainsForeignAndUnknownOutcomes(t *testing.T) {
	for _, where := range []string{"inventory", "foreign", "missing-handle", "delete", "replacement-before-delete", "post-inventory", "replacement"} {
		t.Run(where, func(t *testing.T) {
			plan, readback, absent, present := nativeNFTEffectFixture(t)
			lost := errors.New("lost deletion reply: not found")
			steps := []nativeNFTStep{{args: "-j list tables", output: present}}
			if where == "inventory" {
				steps[0].err = lost
			} else {
				if where == "foreign" {
					readback = []byte(strings.ReplaceAll(string(readback), "yoloctl:"+plan.Inputs.OwnerSHA256, "yoloctl:"+strings.Repeat("a", 64)))
				}
				if where == "missing-handle" {
					readback = []byte(strings.Replace(string(readback), `"handle":17,`, "", 1))
				}
				steps = append(steps, nativeNFTStep{args: "-j list table inet " + EgressTable, output: readback})
				if where != "foreign" && where != "missing-handle" {
					steps = append(steps, nativeNFTStep{args: "delete table inet handle 17"})
					if where == "delete" || where == "replacement-before-delete" {
						// The selected handle disappeared; a replacement table with
						// this name must survive. No name-based retry is permitted.
						steps[2].err = lost
					} else {
						steps = append(steps, nativeNFTStep{args: "-j list tables", output: absent})
						if where == "post-inventory" {
							steps[3].err = lost
						} else {
							steps[3].output = present
						}
					}
				}
			}
			got, err := retirePlannedNativeEgress(context.Background(), plan, plan.Namespace, scriptedNativeNFT(t, steps))
			var uncertain *NativeEffectsUncertain
			// Failed deletion or a surviving table is a plain error.
			if err == nil || got.Schema != 0 || errors.As(err, &uncertain) {
				t.Fatalf("retirement uncertainty/foreign refusal: %+v, %v", got, err)
			}
		})
	}
}

func TestNativeEgressInventoryRequiresCompleteSuccessfulShape(t *testing.T) {
	_, _, absent, present := nativeNFTEffectFixture(t)
	for _, raw := range [][]byte{absent, present} {
		if _, err := parseNativeEgressTableInventory(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, flags := range []string{`"dormant"`, `["dormant","owner"]`} {
		raw := strings.TrimSuffix(string(absent), "]}") + `,{"table":{"family":"ip","name":"other","handle":5,"flags":` + flags + `,"comment":"other owner"}}]}`
		if got, err := parseNativeEgressTableInventory([]byte(raw)); err != nil || got {
			t.Fatalf("unrelated table flags rejected: %v, %v", got, err)
		}
	}
	duplicateTable := strings.TrimSuffix(string(present), "]}") + `,{"table":{"family":"inet","name":"` + EgressTable + `","handle":99}}]}`
	duplicateMeta := strings.TrimSuffix(string(absent), "]}") + `,{"metainfo":{"version":"1.0.9","json_schema_version":1}}]}`
	for _, raw := range []string{duplicateTable, duplicateMeta} {
		if got, err := parseNativeEgressTableInventory([]byte(raw)); err == nil || got {
			t.Fatal("duplicate inventory accepted as complete", err)
		}
	}
	for _, raw := range []string{"", `{}`, `null`, `{"nftables":null}`, `{"nftables":[]}`, string(absent) + `{}`, strings.Replace(string(absent), `"version":"1.0.9",`, "", 1), strings.Replace(string(absent), `"json_schema_version":1`, `"json_schema_version":2`, 1), strings.Replace(string(present), `"handle":17`, `"handle":"17"`, 1), strings.Replace(string(present), `"handle":17`, `"handle":-1`, 1), strings.Replace(string(present), `"family":"inet"`, `"family":"alien"`, 1), strings.Replace(string(present), `"family":"inet"`, `"family":"ip"`, 1), strings.Replace(string(present), `"table":`, `"chain":`, 1), strings.Replace(string(present), `"handle":17`, `"handle":17,"unknown":true`, 1), strings.Replace(string(present), `"handle":17`, `"handle":17,"flags":[]`, 1), strings.Replace(string(present), `"name":`, `"name":"duplicate","name":`, 1)} {
		if got, err := parseNativeEgressTableInventory([]byte(raw)); err == nil || got {
			t.Errorf("invalid/unknown inventory became presence or absence: %q -> %v, %v", raw, got, err)
		}
	}
}

func TestNativeEgressEffectsCancellationPreventsAnyCommand(t *testing.T) {
	plan, _, _, _ := nativeNFTEffectFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := applyPlannedNativeEgress(ctx, plan, plan.Namespace, plan.Observed, scriptedNativeNFT(t, nil)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled apply reached infrastructure", err)
	}
	if _, err := retirePlannedNativeEgress(ctx, plan, plan.Namespace, scriptedNativeNFT(t, nil)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled retirement reached infrastructure", err)
	}
}

func TestNativeEgressWorkerRequestAndModesStrict(t *testing.T) {
	plan, _, _, _ := nativeNFTEffectFixture(t)
	raw, _ := json.Marshal(plan)
	got, err := decodePlannedNetworkRequest(raw)
	if err != nil || !reflect.DeepEqual(got, plan) {
		t.Fatal("complete child request refused", err)
	}
	for _, bad := range [][]byte{nil, []byte(`{}`), append(append([]byte(nil), raw...), []byte(` {}`)...), []byte(strings.Replace(string(raw), `"schema":2`, `"schema":2,"unknown":true`, 1)), []byte(strings.Replace(string(raw), `"schema":2`, `"schema":2,"schema":2`, 1))} {
		if got, err := decodePlannedNetworkRequest(bad); err == nil || got.Schema != 0 {
			t.Fatal("malformed child request accepted", err)
		}
	}
	for _, mode := range []string{"", "plan", "apply", "retire"} {
		var args []string
		if mode != "" {
			args = []string{mode}
		}
		if got, err := nativeNetworkWorkerMode(args); err != nil || got != mode {
			t.Fatalf("fixed role refused %q: %q, %v", args, got, err)
		}
	}
	for _, args := range [][]string{{""}, {"--apply"}, {"apply", "retire"}, {"retire", "/proc/1/ns/net"}} {
		if got, err := nativeNetworkWorkerMode(args); err == nil || got != "" {
			t.Fatalf("arbitrary role accepted %q", args)
		}
	}
}
