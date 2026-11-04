package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
)

type replacementRemovalFixture struct {
	calls *[]string
	fail  string
	err   error
}

func (r replacementRemovalFixture) Remove(context.Context) error {
	*r.calls = append(*r.calls, "remove")
	if r.fail == "remove" {
		return r.err
	}
	return nil
}
func (r replacementRemovalFixture) Close() error {
	*r.calls = append(*r.calls, "close")
	if r.fail == "close" {
		return r.err
	}
	return nil
}

func TestNativeReplacementRemovalSettlesBeforeCreate(t *testing.T) {
	for _, failed := range []string{"", "prefix", "remove", "close", "create"} {
		t.Run("failure="+failed, func(t *testing.T) {
			var calls []string
			failure := errors.New("original result")
			removal := replacementRemovalFixture{&calls, failed, failure}
			step := func(name string) func(context.Context) error {
				return func(context.Context) error {
					calls = append(calls, name)
					if failed == name {
						return failure
					}
					return nil
				}
			}
			err := completeNativeReplacement(t.Context(), removal, step("prefix"), step("create"))
			want := []string{"prefix", "remove", "close", "create"}
			if failed != "" && !errors.Is(err, failure) {
				t.Fatalf("err=%v", err)
			}
			if failed == "" && err != nil {
				t.Fatal(err)
			}
			wantUncertain := false
			if workerEffectsUncertain(err) != wantUncertain {
				t.Fatalf("uncertainty=%v want=%v err=%v", workerEffectsUncertain(err), wantUncertain, err)
			}
			switch failed {
			case "prefix":
				want = []string{"prefix", "close"}
			case "remove", "close":
				want = want[:3]
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestNativeReplacementCancellationClosesWithoutEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var calls []string
	forbidden := func(context.Context) error { t.Fatal("effect after cancellation"); return nil }
	err := completeNativeReplacement(ctx, replacementRemovalFixture{calls: &calls}, forbidden, forbidden)
	if !errors.Is(err, context.Canceled) || !slices.Equal(calls, []string{"close"}) {
		t.Fatalf("err=%v calls=%v", err, calls)
	}
}

// An acknowledged step's ordinary/missing-record error is not a lost reply.
// Keep real typed uncertainty while avoiding new holds for known failures.
func TestNativeReplacementPreservesEachStepsOwnCertainty(t *testing.T) {
	for _, step := range []string{"prefix", "remove", "close"} {
		for _, uncertain := range []bool{false, true} {
			t.Run(step+fmt.Sprint(uncertain), func(t *testing.T) {
				var calls []string
				cause := error(os.ErrNotExist)
				if uncertain {
					cause = &agentbox.NativeEffectsUncertain{Cause: cause}
				}
				removal := replacementRemovalFixture{calls: &calls, fail: step, err: cause}
				prefix := func(context.Context) error {
					calls = append(calls, "prefix")
					if step == "prefix" {
						return cause
					}
					return nil
				}
				create := func(context.Context) error { t.Fatal("create followed a failed cleanup step"); return nil }
				err := completeNativeReplacement(t.Context(), removal, prefix, create)
				if !errors.Is(err, os.ErrNotExist) || workerEffectsUncertain(err) != uncertain {
					t.Fatal("step result was reclassified", err)
				}
				if slices.Contains(calls, "create") || countReplacementClose(calls) != 1 {
					t.Fatal("cleanup ownership changed", calls)
				}
			})
		}
	}
}

func countReplacementClose(calls []string) int {
	count := 0
	for _, call := range calls {
		if call == "close" {
			count++
		}
	}
	return count
}
