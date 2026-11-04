package operator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type nativeAllocatorFailure struct{}

func (*nativeAllocatorFailure) Error() string { return "original private checkpoint failure" }

func TestNativeIncarnationBridgeKeepsOriginalCallbackAndRefusesFallback(t *testing.T) {
	for _, absent := range []context.Context{t.Context(), WithNativeIncarnationAllocator(t.Context(), nil)} {
		if value, err := AllocateNativeIncarnation(absent, "box"); err == nil || value != "" {
			t.Fatal("missing allocator silently minted a local identity")
		}
	}
	calls := 0
	ctx := WithNativeIncarnationAllocator(t.Context(), func(call context.Context, box string) (string, error) {
		calls++
		if call.Err() != nil || box != "box" {
			t.Fatal("original callback received another box or canceled call")
		}
		return strings.Repeat(string(rune('a'+calls-1)), 32), nil
	})
	first, err := AllocateNativeIncarnation(ctx, "box")
	if err != nil || first != strings.Repeat("a", 32) {
		t.Fatal("original callback identity was replaced", err)
	}
	second, err := AllocateNativeIncarnation(ctx, "box")
	if err != nil || second != strings.Repeat("b", 32) || calls != 2 {
		t.Fatal("bridge cached or replayed an allocation", err)
	}
	if value, err := AllocateNativeIncarnation(ctx, "../box"); err == nil || value != "" || calls != 2 {
		t.Fatal("invalid box reached original allocator")
	}
	sentinel := new(nativeAllocatorFailure)
	failed := WithNativeIncarnationAllocator(t.Context(), func(context.Context, string) (string, error) {
		return strings.Repeat("c", 32), sentinel
	})
	value, err := AllocateNativeIncarnation(failed, "box")
	var typed *nativeAllocatorFailure
	if value != "" || !errors.As(err, &typed) || typed != sentinel {
		t.Fatal("callback failure marker lost or failed value escaped", err)
	}
	for _, value := range []string{"", strings.Repeat("A", 32), strings.Repeat("a", 31), "../" + strings.Repeat("a", 29)} {
		ctx := WithNativeIncarnationAllocator(t.Context(), func(context.Context, string) (string, error) { return value, nil })
		if got, err := AllocateNativeIncarnation(ctx, "box"); err == nil || got != "" {
			t.Fatal("malformed original identity escaped bridge", value)
		}
	}
}

func TestNativeIncarnationBridgeCannotReplaceOriginalCancellation(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			original, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			ctx := WithNativeIncarnationAllocator(original, func(context.Context, string) (string, error) {
				calls++
				cancel()
				return strings.Repeat("a", 32), nil
			})
			if !during {
				cancel()
			}
			// Preserve values but deliberately erase caller cancellation. The
			// bridge must retain the actual installation lifetime independently.
			if value, err := AllocateNativeIncarnation(context.WithoutCancel(ctx), "box"); !errors.Is(err, context.Canceled) || value != "" {
				t.Fatal("replacement context revived original allocator", err)
			}
			want := 0
			if during {
				want = 1
			}
			if calls != want {
				t.Fatal("canceled allocation invoked/replayed callback", calls)
			}
		})
	}
}

func TestNativeIncarnationBridgeRetainsCallerCancellation(t *testing.T) {
	var cancel context.CancelFunc
	calls := 0
	base := WithNativeIncarnationAllocator(t.Context(), func(context.Context, string) (string, error) {
		calls++
		cancel()
		return strings.Repeat("a", 32), nil
	})
	ctx, stop := context.WithCancel(base)
	cancel = stop
	defer stop()
	if value, err := AllocateNativeIncarnation(ctx, "box"); value != "" || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled caller received original identity", err)
	}
	if value, err := AllocateNativeIncarnation(ctx, "box"); value != "" || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled caller replayed allocation", err)
	}
}
