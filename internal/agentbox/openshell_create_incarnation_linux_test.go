package agentbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/operator"
)

// An operator-enabled box gets its own random incarnation, as on the old path
// (leogr's relaxed decisions): no parent allocation is required or consulted,
// and the binding, launch and container label all carry it.
func TestNativeCreationPlanGivesOperatorBoxesAFreshIncarnation(t *testing.T) {
	c, current, release := nativeContainerPlanFixture(t)
	c.Capabilities = []string{operator.Capability}
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx := operator.WithNativeIncarnationAllocator(t.Context(), func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("unexpected parent allocation")
	})
	options, first, enabled, err := planNativeCreation(ctx, c, c.Image, current, release, policy)
	if err != nil || !enabled || calls != 0 {
		t.Fatal("operator plan needed a parent allocation", err)
	}
	value := first.binding.Incarnation
	if len(value) != 32 || first.launch.Incarnation != value || options.Labels[openshell.LabelIncarnation] != value || !strings.HasSuffix(first.controlRoot, "/"+value) {
		t.Fatal("operator plan identities disagree", value)
	}
	_, second, _, err := planNativeCreation(t.Context(), c, c.Image, current, release, policy)
	if err != nil || second.binding.Incarnation == value {
		t.Fatal("operator plan reused an incarnation", err)
	}
}

func TestNativeCreationPlanPreservesNonOperatorAndRejectsOtherLaunchModes(t *testing.T) {
	c, current, release := nativeContainerPlanFixture(t)
	policy, err := ComposePolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx := operator.WithNativeIncarnationAllocator(t.Context(), func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("unexpected operator allocation")
	})
	_, first, enabled, err := planNativeCreation(ctx, c, c.Image, current, release, policy)
	if err != nil || enabled || calls != 0 {
		t.Fatal("non-operator creation acquired parent publication dependency", err)
	}
	_, second, enabled, err := planNativeCreation(t.Context(), c, c.Image, current, release, policy)
	if err != nil || enabled || first.binding.Incarnation == second.binding.Incarnation {
		t.Fatal("ordinary native fresh identity changed", err)
	}
	c.Privileged = true
	if _, _, _, err := planNativeCreation(ctx, c, c.Image, current, release, policy); err == nil || calls != 0 {
		t.Fatal("privileged route acquired native operator allocator")
	}
	if _, _, _, err := planNativeCreation(ctx, nil, c.Image, current, release, policy); err == nil || calls != 0 {
		t.Fatal("missing config reached native allocator")
	}
	c.Privileged = false
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := planNativeCreation(canceled, c, c.Image, current, release, policy); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled create plan proceeded", err)
	}
}
