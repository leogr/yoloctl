package agentbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func nativeCleanupAdapterFixture(t *testing.T, stopStatus int) (*container.Client, string, func(map[string]any, string, bool)) {
	t.Helper()
	if spaceenv.Native() {
		t.Fatal("mock adapter must not execute in configured native infrastructure")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	directory := t.TempDir()
	t.Setenv("YOLOCTL_NATIVE_CLEANUP_FIXTURE", directory)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("YOLOCTL_NATIVE_CLEANUP_STOP_STATUS", strconv.Itoa(stopStatus))
	id := strings.Repeat("a", 64)
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$YOLOCTL_NATIVE_CLEANUP_FIXTURE/commands.log"
case "$*" in
 'shell yolo-fixture -- sudo nerdctl inspect ` + id + `') cat "$YOLOCTL_NATIVE_CLEANUP_FIXTURE/inspect.json" ;;
 'shell yolo-fixture -- sudo nerdctl stop --time 30 ` + id + `') exit "$YOLOCTL_NATIVE_CLEANUP_STOP_STATUS" ;;
 'shell yolo-fixture -- sudo nerdctl rm ` + id + `') exit 0 ;;
 *) exit 97 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(object map[string]any, state string, present bool) {
		copy := map[string]any{}
		for k, v := range object {
			copy[k] = v
		}
		if present {
			copy["State"] = json.RawMessage(state)
		} else {
			delete(copy, "State")
		}
		raw, err := json.Marshal([]any{copy})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "inspect.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return container.NewWithOutput("fixture", io.Discard, io.Discard), filepath.Join(directory, "commands.log"), write
}

func nativeCleanupAdapterPlan(t *testing.T) (nativeContainerPlan, map[string]any, openshell.ActivationTarget) {
	t.Helper()
	c, current, release := nativeContainerPlanFixture(t)
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	options, plan, err := planNativeContainer(c, c.Image, current, release, strings.Repeat("d", 32), policy)
	if err != nil {
		t.Fatal(err)
	}
	d := createdNativePlanDetails(options)
	mounts := []any{}
	for _, m := range d.Mounts {
		mounts = append(mounts, map[string]any{"Source": m.Source, "Destination": m.Destination, "RW": !m.ReadOnly})
	}
	object := map[string]any{"Id": d.ID, "Name": "/" + d.Name, "Path": d.Command[0], "Args": d.Command[1:], "Config": map[string]any{"Image": options.Image, "Labels": d.Labels, "Env": d.Env}, "HostConfig": map[string]any{"Privileged": false}, "Mounts": mounts}
	return plan, object, openshell.ActivationTarget{ContainerID: d.ID, PID: 42, StartedAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}
}

const nativeCleanupEmptyTaskJSON = `{"Status":"","Running":false,"Pid":0,"StartedAt":"","Paused":false,"Restarting":false,"ExitCode":0,"Error":"","FinishedAt":""}`
const nativeCleanupRunningJSON = `{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-31T00:00:00Z"}`

func TestNativeCleanupActualAdapterAllowsInactiveHistoryAtBothPreStopGates(t *testing.T) {
	for _, initialInactive := range []bool{true, false} {
		for _, stopStatus := range []int{0, 23} {
			t.Run(fmt.Sprintf("before-%t/stop-%d", initialInactive, stopStatus), func(t *testing.T) {
				plan, object, target := nativeCleanupAdapterPlan(t)
				client, log, write := nativeCleanupAdapterFixture(t, stopStatus)
				if initialInactive {
					write(object, nativeCleanupEmptyTaskJSON, true)
				} else {
					write(object, nativeCleanupRunningJSON, true)
				}
				inspect := func(c context.Context) (*container.Details, error) {
					d, err := client.Inspect(c, target.ContainerID)
					if err != nil {
						return nil, err
					}
					if err := plan.validateContainer(d); err != nil {
						return nil, err
					}
					return d, nil
				}
				checks := nativeCreatedCleanupChecks{inspect: inspect, remaining: func(context.Context) error { return nil }, id: target.ContainerID, target: target, started: true}
				// This is the SAME outer guard used by the real create callback.
				if err := checks.owned(t.Context()); err != nil {
					t.Fatal("inactive-before-cleanup rejected by actual shared gate", err)
				}
				// Model helper exit between outer guard and actual owner callback.
				write(object, nativeCleanupEmptyTaskJSON, true)
				err := cleanupNativeCreatedContainerID(t.Context(), checks, nil, func(c context.Context) error { return client.Stop(c, target.ContainerID) }, client.RemoveContainerID)
				raw, readErr := os.ReadFile(log)
				if readErr != nil {
					t.Fatal(readErr)
				}
				commands := string(raw)
				if strings.Count(commands, "nerdctl stop --time 30 "+target.ContainerID) != 1 {
					t.Fatal("missing/duplicate actual Stop callback", commands)
				}
				if stopStatus == 0 {
					if err != nil || strings.Count(commands, "nerdctl rm "+target.ContainerID) != 1 {
						t.Fatal("owned inactive history did not reach one ACKed removal", err, commands)
					}
				} else {
					// A failed Stop is a plain error (decision #3) and nothing
					// is removed after it.
					var uncertain *NativeEffectsUncertain
					if err == nil || errors.As(err, &uncertain) || strings.Contains(commands, "nerdctl rm") || strings.Count(commands, "nerdctl inspect") != 2 {
						t.Fatal("failed Stop inspected/removed or became uncertain", err, commands)
					}
				}
			})
		}
	}
}

func TestNativeCleanupActualAdapterRetainsChangedLiveUnknownAndBindingRefusals(t *testing.T) {
	for _, change := range []string{"live-pid", "live-time", "inactive-time", "malformed-time", "foreign-cid", "unknown-status", "missing-state", "null-state", "incomplete-state", "uncertain-owner"} {
		t.Run(change, func(t *testing.T) {
			plan, object, target := nativeCleanupAdapterPlan(t)
			client, log, write := nativeCleanupAdapterFixture(t, 0)
			write(object, nativeCleanupRunningJSON, true)
			cause := errors.New("uncertain current storage")
			proofs := 0
			inspect := func(c context.Context) (*container.Details, error) {
				d, err := client.Inspect(c, target.ContainerID)
				if err != nil {
					return nil, err
				}
				if err := plan.validateContainer(d); err != nil {
					return nil, err
				}
				return d, nil
			}
			checks := nativeCreatedCleanupChecks{inspect: inspect, remaining: func(context.Context) error {
				proofs++
				if change == "uncertain-owner" && proofs > 1 {
					return &NativeEffectsUncertain{Cause: cause}
				}
				return nil
			}, id: target.ContainerID, target: target, started: true}
			if err := checks.owned(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, present := nativeCleanupEmptyTaskJSON, true
			switch change {
			case "live-pid":
				state = strings.Replace(nativeCleanupRunningJSON, `"Pid":42`, `"Pid":43`, 1)
			case "live-time":
				state = strings.Replace(nativeCleanupRunningJSON, "00:00:00Z", "00:00:01Z", 1)
			case "inactive-time":
				state = `{"Status":"exited","Running":false,"Pid":0,"StartedAt":"2026-10-31T00:00:01Z"}`
			case "malformed-time":
				state = `{"Status":"exited","Running":false,"Pid":0,"StartedAt":"malformed"}`
			case "foreign-cid":
				object["Id"] = strings.Repeat("b", 64)
			case "foreign-bind":
				object["Mounts"] = []any{map[string]any{"Source": "/foreign", "Destination": plan.launch.Workdir, "RW": false}}
			case "unknown-status":
				state = `{"Status":"unknown","Running":false,"Pid":0,"StartedAt":""}`
			case "missing-state":
				present = false
			case "null-state":
				state = `null`
			case "incomplete-state":
				state = `{"Status":"","Running":false,"StartedAt":""}`
			}
			write(object, state, present)
			err := cleanupNativeCreatedContainerID(t.Context(), checks, nil, func(c context.Context) error { return client.Stop(c, target.ContainerID) }, client.RemoveContainerID)
			if err == nil {
				t.Fatal("changed/unknown ownership admitted cleanup", change)
			}
			if change == "uncertain-owner" {
				var uncertain *NativeEffectsUncertain
				if !errors.As(err, &uncertain) || !errors.Is(err, cause) {
					t.Fatal("owner uncertainty lost", err)
				}
			}
			raw, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(raw), "nerdctl stop") || strings.Contains(string(raw), "nerdctl rm") {
				t.Fatal("refusal reached mutation", string(raw))
			}
		})
	}
}

func TestNativeContainerPlanExplicitEmptyTaskObservationDoesNotGrantHistory(t *testing.T) {
	plan, object, target := nativeCleanupAdapterPlan(t)
	client, _, write := nativeCleanupAdapterFixture(t, 0)
	write(object, nativeCleanupEmptyTaskJSON, true)
	d, err := client.Inspect(t.Context(), target.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	// Data assembly only: the real caller must separately hold its own fresh
	// Create ACK and know Start was not attempted. Empty fields prove neither.
	if _, err := plan.bootstrapForCreated(d); err != nil {
		t.Fatal("explicit adapter shape blocked fresh-owned bootstrap data", err)
	}
	write(object, nativeCleanupEmptyTaskJSON, false)
	d, err = client.Inspect(t.Context(), target.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.bootstrapForCreated(d); err == nil {
		t.Fatal("missing State became fresh bootstrap evidence")
	}
	write(object, `{"Status":"created","Running":false,"Pid":0,"StartedAt":"2026-10-31T00:00:00Z"}`, true)
	d, err = client.Inspect(t.Context(), target.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.bootstrapForCreated(d); err == nil {
		t.Fatal("present past execution was erased by empty-task support")
	}
}

func TestNativeCleanupActualAdapterRefusesStateAliasesAtBothGates(t *testing.T) {
	for _, members := range []struct{ name, json string }{
		{"alias-first", `"state":` + nativeCleanupRunningJSON + `,"State":` + nativeCleanupEmptyTaskJSON},
		{"alias-last", `"State":` + nativeCleanupEmptyTaskJSON + `,"state":` + nativeCleanupRunningJSON},
		{"paused-first", `"state":{"Paused":true},"State":` + nativeCleanupEmptyTaskJSON},
		{"restarting-last", `"State":` + nativeCleanupEmptyTaskJSON + `,"state":{"Restarting":true}`},
		{"unicode-alias", `"ſtate":` + nativeCleanupRunningJSON + `,"State":` + nativeCleanupEmptyTaskJSON},
	} {
		for _, beforeOuter := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/before-outer-%t", members.name, beforeOuter), func(t *testing.T) {
				plan, object, target := nativeCleanupAdapterPlan(t)
				client, log, write := nativeCleanupAdapterFixture(t, 0)
				write(object, nativeCleanupRunningJSON, true)
				inspect := func(c context.Context) (*container.Details, error) {
					d, err := client.Inspect(c, target.ContainerID)
					if err != nil {
						return nil, err
					}
					if err := plan.validateContainer(d); err != nil {
						return nil, err
					}
					return d, nil
				}
				checks := nativeCreatedCleanupChecks{inspect: inspect, remaining: func(context.Context) error { return nil }, id: target.ContainerID, target: target, started: true}
				if !beforeOuter {
					if err := checks.owned(t.Context()); err != nil {
						t.Fatal("canonical live control refused", err)
					}
				}
				// Preserve key order in the actual adapter's raw JSON response.
				// A map marshal would sort the keys and mask one order.
				raw, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(`[{` + members.json + `,` + string(raw[1:]) + `]`)
				if err := os.WriteFile(filepath.Join(filepath.Dir(log), "inspect.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if beforeOuter {
					err = checks.owned(t.Context())
				} else {
					err = cleanupNativeCreatedContainerID(t.Context(), checks, nil, func(c context.Context) error { return client.Stop(c, target.ContainerID) }, client.RemoveContainerID)
				}
				if err == nil {
					t.Fatal("ambiguous State admitted outer/owner cleanup", members.json)
				}
				commands, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(commands), "nerdctl stop") || strings.Contains(string(commands), "nerdctl rm") {
					t.Fatal("ambiguous State reached engine effects", string(commands))
				}
			})
		}
	}
}
