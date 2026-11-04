package installmigration

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func snapshot(space string) Snapshot {
	return Snapshot{Space: space, Bundle: "old", State: "Running", DiskID: "disk", DiskUUID: "uuid", Generation: "gen", Bootstrap: strings.Repeat("a", 64)}
}
func TestExecutionConsentOrderingAndSnapshotAdvancement(t *testing.T) {
	for _, scenario := range []string{"success", "decline", "yes", "non-tty", "changed-state", "changed-bundle", "skip-locked", "fail", "unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			initial := snapshot("a")
			next := initial
			next.Bundle = "new"
			next.Generation = "next"
			next.Bootstrap = strings.Repeat("b", 64)
			second := snapshot("b")
			proposals := []Proposal{{Snapshot: initial, Commands: []Command{{Args: []string{"update"}}, {Args: []string{"recreate"}, Recreate: true}}}, {Snapshot: second, Commands: []Command{{Args: []string{"update"}}}}}
			current := map[string]Snapshot{"a": initial, "b": second}
			var ran []string
			e := Execution{TargetBundle: "new", Interactive: scenario != "non-tty", Yes: scenario == "yes", Out: io.Discard,
				Confirm: func(string) (bool, error) { return scenario != "decline", nil },
				Observe: func(_ context.Context, name string) (Snapshot, error) {
					s := current[name]
					if name == "a" {
						if scenario == "changed-state" {
							s.State = "Stopped"
						}
						if scenario == "changed-bundle" {
							s.Bundle = "concurrent"
						}
					}
					return s, nil
				},
				Run: func(_ context.Context, before Snapshot, c Command) (Receipt, error) {
					ran = append(ran, before.Space+":"+c.Args[0])
					if before.Space == "a" && scenario == "skip-locked" {
						return Receipt{}, ErrChanged
					}
					if scenario == "fail" {
						return Receipt{}, errors.New("failure")
					}
					after := before
					after.Bundle = "new"
					after.Generation = "next"
					after.Bootstrap = next.Bootstrap
					current[before.Space] = after
					if scenario == "unconfirmed" {
						return Receipt{}, nil
					}
					return Receipt{Before: before, After: after}, nil
				}}
			err := Execute(t.Context(), proposals, e)
			if (scenario == "fail" || scenario == "unconfirmed") != (err != nil) {
				t.Fatal(err)
			}
			want := []string{"a:update", "a:recreate", "b:update"}
			switch scenario {
			case "yes", "non-tty", "decline":
				want = nil
			case "changed-state", "changed-bundle":
				want = []string{"b:update"}
			case "skip-locked":
				want = []string{"a:update", "b:update"}
			case "fail", "unconfirmed":
				want = []string{"a:update"}
			}
			if !reflect.DeepEqual(ran, want) {
				t.Fatalf("ran %v, want %v", ran, want)
			}
		})
	}
}
func TestSnapshotDecodeStrictAndRoundtrip(t *testing.T) {
	original := snapshot("a")
	decoded, err := Decode(original.Encode())
	if err != nil || decoded != original {
		t.Fatal(decoded, err)
	}
	for _, s := range []string{"garbage", "e30", "", "W10"} {
		if _, err := Decode(s); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}

// An unavailable observation is reported with its cause, not as a change.
func TestExecutionReportsObservationError(t *testing.T) {
	var out strings.Builder
	initial := snapshot("a")
	err := Execute(t.Context(), []Proposal{{Snapshot: initial, Commands: []Command{{Args: []string{"update"}}}}}, Execution{
		TargetBundle: "new", Interactive: true, Out: &out,
		Confirm: func(string) (bool, error) { return true, nil },
		Observe: func(context.Context, string) (Snapshot, error) { return Snapshot{}, errors.New("VM probe timed out") },
		Run: func(context.Context, Snapshot, Command) (Receipt, error) {
			t.Fatal("ran without a matching observation")
			return Receipt{}, nil
		},
	})
	if err != nil || !strings.Contains(out.String(), "VM probe timed out") {
		t.Fatal(err, out.String())
	}
}

// archyFable F1: an interrupted installer stops prompting and never turns an
// interrupted run into "state changed, skipped".
func TestExecutionStopsOnInterruption(t *testing.T) {
	initial := snapshot("a")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	prompts := 0
	err := Execute(ctx, []Proposal{{Snapshot: initial, Commands: []Command{{Args: []string{"update"}}, {Args: []string{"recreate"}}}}, {Snapshot: snapshot("b"), Commands: []Command{{Args: []string{"update"}}}}}, Execution{
		TargetBundle: "new", Interactive: true, Out: io.Discard,
		Confirm: func(string) (bool, error) { prompts++; return true, nil },
		Observe: func(_ context.Context, name string) (Snapshot, error) { return snapshot(name), nil },
		Run: func(context.Context, Snapshot, Command) (Receipt, error) {
			cancel() // Ctrl-C during the first command
			return Receipt{}, ErrChanged
		},
	})
	if !errors.Is(err, context.Canceled) || prompts != 1 {
		t.Fatal("interruption skipped or kept prompting", err, prompts)
	}
}

// archyAstra r2: Ctrl-C during the LAST proposal's Observe (or at its prompt)
// returns the interruption, never a "skipped" line followed by success.
func TestExecutionInterruptedLastObservationOrPromptIsNotSuccess(t *testing.T) {
	for _, at := range []string{"observe", "confirm"} {
		t.Run(at, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var out strings.Builder
			err := Execute(ctx, []Proposal{{Snapshot: snapshot("a"), Commands: []Command{{Args: []string{"update"}}}}}, Execution{
				TargetBundle: "new", Interactive: true, Out: &out,
				Confirm: func(string) (bool, error) {
					if at == "confirm" {
						cancel()
						return false, nil
					}
					return true, nil
				},
				Observe: func(context.Context, string) (Snapshot, error) {
					cancel()
					return Snapshot{}, context.Canceled
				},
				Run: func(context.Context, Snapshot, Command) (Receipt, error) {
					t.Fatal("ran after interruption")
					return Receipt{}, nil
				},
			})
			if !errors.Is(err, context.Canceled) || strings.Contains(out.String(), "skipped") {
				t.Fatalf("interrupted %s returned %v, output %q", at, err, out.String())
			}
		})
	}
}
