package agentbox

import (
	"context"
	"errors"
	"testing"
)

func TestReplacementCacheClearRequiresExplicitIntentAndObservedRemoval(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		explicit, absent, clearFails bool
	}{
		{"default", false, true, false}, {"still present", true, false, false}, {"clear failure", true, true, true}, {"removed", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			reported := uint64(0)
			ctx := t.Context()
			if tc.explicit {
				ctx = WithRecreateCacheClear(ctx, func(n uint64) { reported = n })
			}
			failure := errors.New("retained")
			err := clearReplacementCache(ctx, "coder", func(context.Context, string) error {
				calls = append(calls, "absence")
				if !tc.absent {
					return failure
				}
				return nil
			}, func(context.Context, string) (uint64, error) {
				calls = append(calls, "clear")
				if tc.clearFails {
					return 0, failure
				}
				return 123, nil
			})
			if !tc.explicit {
				if err != nil || len(calls) != 0 || reported != 0 {
					t.Fatal("default recreate cleared a cache", calls, err, reported)
				}
				return
			}
			if !tc.absent {
				if !errors.Is(err, failure) || len(calls) != 1 || reported != 0 {
					t.Fatal("cleared before removal", calls, err, reported)
				}
				return
			}
			if tc.clearFails {
				if !errors.Is(err, failure) || reported != 0 {
					t.Fatal("reported a failed clear", calls, err, reported)
				}
				return
			}
			if err != nil || len(calls) != 2 || reported != 123 {
				t.Fatal("clear did not complete once after absence", calls, err, reported)
			}
		})
	}
}

func TestReplacementCacheClearCancelledBeforeObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(WithRecreateCacheClear(t.Context(), nil))
	cancel()
	forbidden := func(context.Context, string) error { t.Fatal("observed after cancellation"); return nil }
	clear := func(context.Context, string) (uint64, error) { t.Fatal("cleared after cancellation"); return 0, nil }
	if err := clearReplacementCache(ctx, "coder", forbidden, clear); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
