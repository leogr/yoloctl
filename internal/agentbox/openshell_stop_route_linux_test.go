package agentbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// This uses the same ordinary Client adapter fixture as owned-create cleanup.
// Full versioned record/bind validation is real; limactl is a finite fake.
// No native root, namespace effect or actual VM is exercised by these tests.
func nativeStopActualAdapterFixture(t *testing.T, status int) (*container.Client, string, func(string), func(context.Context) (OpenShellRuntime, *container.Details, error)) {
	t.Helper()
	current, d, raw, manifest := nativeRuntimeFixture(t)
	client, log, write := nativeCleanupAdapterFixture(t, status)
	mounts := []any{}
	for _, m := range d.Mounts {
		mounts = append(mounts, map[string]any{"Source": m.Source, "Destination": m.Destination, "RW": !m.ReadOnly})
	}
	object := map[string]any{"Id": d.ID, "Name": "/" + d.Name, "Path": d.Command[0], "Args": d.Command[1:], "Config": map[string]any{"Labels": d.Labels, "Env": d.Env}, "HostConfig": map[string]any{"Privileged": false}, "Mounts": mounts}
	set := func(state string) { write(object, state, true) }
	set(`{"Status":"running","Running":true,"Pid":42,"StartedAt":"` + d.StartedAt + `"}`)
	read := func(ctx context.Context) (OpenShellRuntime, *container.Details, error) {
		observed, err := client.Inspect(ctx, d.ID)
		if err != nil {
			return OpenShellRuntime{}, nil, err
		}
		value, err := validateOpenShellRuntime(current, d.Name, observed, raw, manifest)
		return value, observed, err
	}
	return client, log, set, read
}

func TestNativeStopActualAdapterKeepsNamespaceRetirementBeforeEndpointAndNoFallback(t *testing.T) {
	for _, mode := range []string{"confined", "unconfined"} {
		for _, outcome := range []string{"success", "failed-stop", "failed-retirement", "failed-endpoint"} {
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				status := 0
				if outcome == "failed-stop" {
					status = 23
				}
				client, log, set, read := nativeStopActualAdapterFixture(t, status)
				selected, _, err := read(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				checks := nativeRuntimeStopChecks{selected: selected, read: read, resources: func(context.Context) error { return nil }}
				if err := checks.owned(t.Context()); err != nil {
					t.Fatal(err)
				}
				// Helper exit loses task history before normal Stop starts. Exact CID,
				// native record/binds and explicit empty fields remain independently read.
				set(nativeCleanupEmptyTaskJSON)
				stop := func(c context.Context) error { return client.Stop(c, selected.ContainerID) }
				namespaces, endpoints := 0, 0
				retired := false
				cause := errors.New("no such container: retained owned cleanup failure")
				namespace := func(c context.Context) error {
					namespaces++
					if err := checks.owned(c); err != nil {
						return err
					}
					// Contract fixture for the real SDK namespace callback: fresh actual
					// Stop ACK and inactive proof before simulated policy/pin completion.
					return completeNativeRuntimeStop(c, stop, func(check context.Context) error {
						if err := checks.inactive(check); err != nil {
							return err
						}
						if outcome == "failed-retirement" {
							return cause
						}
						retired = true
						return nil
					})
				}
				endpoint := func(check context.Context) error {
					endpoints++
					if check.Err() != nil {
						t.Fatal("post-ACK endpoint inherited cancellation")
					}
					if mode == "unconfined" && !retired {
						t.Fatal("endpoint removed before namespace retirement")
					}
					if outcome == "failed-endpoint" {
						return cause
					}
					return nil
				}
				err = completeNativeStopRoute(t.Context(), mode == "unconfined", stop, checks.inactive, namespace, endpoint)
				failed := outcome == "failed-stop" || outcome == "failed-endpoint" || outcome == "failed-retirement" && mode == "unconfined"
				var uncertain *NativeEffectsUncertain
				if (err != nil) != failed || errors.As(err, &uncertain) != failed {
					t.Fatal("Stop route lost known result/typed uncertainty", err)
				}
				wantNamespace := 0
				if mode == "unconfined" {
					wantNamespace = 1
				}
				wantEndpoint := 1
				if outcome == "failed-stop" || outcome == "failed-retirement" && mode == "unconfined" {
					wantEndpoint = 0
				}
				if namespaces != wantNamespace || endpoints != wantEndpoint {
					t.Fatal("namespace route fell back or deleted endpoint after failure", namespaces, endpoints)
				}
				commands, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				text := string(commands)
				if strings.Count(text, "nerdctl stop --time 30 "+selected.ContainerID) != 1 || strings.Contains(text, "nerdctl rm") {
					t.Fatal("Stop route replayed/fell back/removed CID", text)
				}
				if outcome == "failed-stop" {
					after := strings.SplitN(text, "nerdctl stop --time 30 "+selected.ContainerID, 2)[1]
					if strings.Contains(after, "nerdctl inspect") {
						t.Fatal("failed Stop was followed by observation-as-ACK", after)
					}
				}
			})
		}
	}
}

func TestNativeStopActualAdapterRefusesChangedAmbiguousAndUnknownBeforeEffects(t *testing.T) {
	for _, change := range []string{"live-pid", "live-time", "inactive-time", "unknown", "paused", "missing-fields", "alias-first", "alias-last", "foreign-record", "typed-resource", "new-live-after-inactive"} {
		t.Run(change, func(t *testing.T) {
			_, log, set, read := nativeStopActualAdapterFixture(t, 0)
			selected, _, err := read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			state := nativeCleanupEmptyTaskJSON
			switch change {
			case "live-pid":
				state = `{"Status":"running","Running":true,"Pid":43,"StartedAt":"2026-10-30T20:00:00Z"}`
			case "live-time":
				state = `{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-30T20:00:01Z"}`
			case "inactive-time":
				state = `{"Status":"exited","Running":false,"Pid":0,"StartedAt":"2026-10-30T20:00:01Z"}`
			case "unknown":
				state = `{"Status":"unknown","Running":false,"Pid":0,"StartedAt":""}`
			case "paused":
				state = `{"Status":"","Running":false,"Pid":0,"StartedAt":"","Paused":true}`
			case "missing-fields":
				state = `{"Status":"","Running":false,"StartedAt":""}`
			case "new-live-after-inactive":
				selected.Running = false
				selected.PID = 0
				selected.StartedAt = selected.StartedAt.Add(-1)
				state = `{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-30T20:00:00Z"}`
			case "foreign-record":
				selected.RecordSHA256 = "foreign"
			}
			set(state)
			if change == "alias-first" || change == "alias-last" {
				// Inject root member aliases in order into the actual fake response.
				p := strings.TrimSuffix(log, "commands.log") + "inspect.json"
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				var entries []map[string]json.RawMessage
				if err := json.Unmarshal(raw, &entries); err != nil {
					t.Fatal(err)
				}
				delete(entries[0], "State")
				body, err := json.Marshal(entries[0])
				if err != nil {
					t.Fatal(err)
				}
				running := `{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-30T20:00:00Z"}`
				members := `"state":` + running + `,"State":` + nativeCleanupEmptyTaskJSON
				if change == "alias-last" {
					members = `"State":` + nativeCleanupEmptyTaskJSON + `,"state":` + running
				}
				if err := os.WriteFile(p, []byte(`[{`+members+`,`+string(body[1:])+`]`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cause := errors.New("current native storage uncertainty")
			checks := nativeRuntimeStopChecks{selected: selected, read: read, resources: func(context.Context) error {
				if change == "typed-resource" {
					return &NativeEffectsUncertain{Cause: cause}
				}
				return nil
			}}
			if err := checks.owned(t.Context()); err == nil {
				t.Fatal("changed/ambiguous normal Stop admitted effects", change)
			} else if change == "typed-resource" {
				var uncertain *NativeEffectsUncertain
				if !errors.As(err, &uncertain) || !errors.Is(err, cause) {
					t.Fatal("typed storage uncertainty lost", err)
				}
			}
			commands, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(commands), "nerdctl stop") || strings.Contains(string(commands), "nerdctl rm") {
				t.Fatal("refusal reached engine effects", string(commands))
			}
		})
	}
}

func TestNativeStopRouteRefusesMissingCallbacksAndNamespaceBeforeAnyEngineFallback(t *testing.T) {
	for _, missing := range []string{"stop", "inactive", "namespace", "endpoint"} {
		t.Run(missing, func(t *testing.T) {
			calls := 0
			callback := func(context.Context) error { calls++; return nil }
			stop, inactive, namespace, endpoint := callback, callback, callback, callback
			switch missing {
			case "stop":
				stop = nil
			case "inactive":
				inactive = nil
			case "namespace":
				namespace = nil
			case "endpoint":
				endpoint = nil
			}
			if err := completeNativeStopRoute(t.Context(), true, stop, inactive, namespace, endpoint); err == nil || calls != 0 {
				t.Fatal("incomplete route reached effects", err, calls)
			}
		})
	}
	calls := 0
	callback := func(context.Context) error { calls++; return nil }
	cause := errors.New("namespace ownership missing")
	if err := completeNativeStopRoute(t.Context(), true, callback, callback, func(context.Context) error { return cause }, callback); !errors.Is(err, cause) || calls != 0 {
		t.Fatal("known namespace preflight failure fell back to engine/endpoint", err, calls)
	}
}
