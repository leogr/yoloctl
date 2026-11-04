package agentboximage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativePreparedImageNeverFallsBackToClientEffects(t *testing.T) {
	// A nil client is intentional: no preparation callback must fail without
	// consulting an image builder, puller or mutable alias.
	if _, err := RequirePreparedImage(t.Context(), nil, "custom/image:latest", nil, Options{}); err == nil {
		t.Fatal("missing controller preparation accepted")
	}
	want := errors.New("prepared recipe changed")
	calls := 0
	ctx := WithPreparedImage(t.Context(), func(context.Context, PreparedContext) (string, bool, error) {
		calls++
		return "", true, want
	})
	if _, err := RequirePreparedImage(ctx, nil, "custom/image:latest", nil, Options{}); !errors.Is(err, want) || calls != 1 {
		t.Fatal("preparation error was retried or replaced", err, calls)
	}
	calls = 0
	ctx = WithPreparedImage(t.Context(), func(context.Context, PreparedContext) (string, bool, error) {
		calls++
		return "sha256:" + strings.Repeat("a", 64), false, nil
	})
	if ref, err := RequirePreparedImage(ctx, nil, "custom/image:latest", nil, Options{}); err == nil || ref != "" || calls != 1 {
		t.Fatal("declined callback used its nonempty reference or a client fallback", ref, err, calls)
	}
	ctx = WithPreparedImage(t.Context(), func(context.Context, PreparedContext) (string, bool, error) {
		return "sha256:retained-target", true, nil
	})
	if ref, err := RequirePreparedImage(ctx, nil, "custom/image:latest", nil, Options{}); err != nil || ref != "sha256:retained-target" {
		t.Fatal("prepared target changed", ref, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := RequirePreparedImage(canceled, nil, "custom/image:latest", nil, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled preparation continued", err)
	}
}
