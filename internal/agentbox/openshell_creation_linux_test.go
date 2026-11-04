package agentbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
)

func TestNativeCreateRouteDoesNotFallBackToLegacyAfterQualificationRefusal(t *testing.T) {
	c, _, release := nativeContainerPlanFixture(t)
	client, log := nativeImageClientFixture(t, "[]", 97)
	a := (LiveApplier{Client: client}).WithNativeCreationRelease(release, nil)
	selections := 0
	ctx := agentboximage.WithPreparedImage(t.Context(), func(context.Context, agentboximage.PreparedContext) (string, bool, error) {
		selections++
		return "sha256:" + strings.Repeat("a", 64), true, nil
	})
	err := a.Create(ctx, c, []byte("desired policy"))
	if err == nil || !strings.Contains(err.Error(), "native creation qualification requires admitted VM-root infrastructure") || selections != 0 {
		t.Fatal("ordinary create failed to select native qualification", err, selections)
	}
	if raw, readErr := os.ReadFile(log); !errors.Is(readErr, os.ErrNotExist) || len(raw) != 0 {
		t.Fatal("native refusal staged legacy policy, ran a container or retried", string(raw), readErr)
	}
}

func TestNativeCreateSelectionPreservesPrivilegedGuardBeforeImageSelection(t *testing.T) {
	c, _, release := nativeContainerPlanFixture(t)
	c.Privileged = true
	want := errors.New("privileged endpoint not qualified")
	old := InspectEndpointBinaryFn
	t.Cleanup(func() { InspectEndpointBinaryFn = old })
	InspectEndpointBinaryFn = func(context.Context, *container.Client) (EndpointBinary, error) {
		return EndpointBinary{}, want
	}
	ctx := agentboximage.WithPreparedImage(t.Context(), func(context.Context, agentboximage.PreparedContext) (string, bool, error) {
		t.Fatal("privileged guard ran after image/native setup")
		return "", true, nil
	})
	a := (LiveApplier{}).WithNativeCreationRelease(release, nil)
	if err := a.Create(ctx, c, nil); !errors.Is(err, want) {
		t.Fatal("native selection bypassed privileged endpoint guard", err)
	}
}
