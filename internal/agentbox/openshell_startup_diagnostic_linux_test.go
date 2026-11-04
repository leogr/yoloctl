package agentbox

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

func TestNativeStartupDiagnosticIsCollectedBeforeOwnedCleanup(t *testing.T) {
	var events []string
	available := true
	id := strings.Repeat("a", 64)
	actions := nativeTransactionFixture(&events, "", nil)
	actions.activate = func(ctx context.Context) error {
		events = append(events, "activate")
		_, err := selectNativeStartedTargetWithDiagnostic(ctx, &container.Details{ID: id}, id, openshell.ActivationTarget{}, func(_ context.Context, target string) string {
			if !available || target != id {
				t.Fatal("diagnostic lost original CID or ran after cleanup")
			}
			events = append(events, "diagnostic")
			return "OpenShell helper: actual early error"
		})
		return err
	}
	actions.cleanup = func(context.Context, nativeCreateProgress) error {
		available = false
		events = append(events, "cleanup")
		return nil
	}
	err := completeNativeContainerCreation(t.Context(), actions)
	if err == nil || errors.Unwrap(err) == nil || !strings.Contains(err.Error(), "native startup requires its acknowledged running container") || !strings.Contains(err.Error(), "actual early error") {
		t.Fatalf("err=%v", err)
	}
	want := []string{"preflight", "reserve", "create", "inspect", "prepare", "start", "activate", "diagnostic", "cleanup"}
	if !slices.Equal(events, want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestNativeStartupDiagnosticDoesNotChangeSuccessfulObservation(t *testing.T) {
	id := strings.Repeat("a", 64)
	d := &container.Details{ID: id, Running: true, PID: 42, StartedAt: "2026-11-03T10:00:00Z"}
	got, err := selectNativeStartedTargetWithDiagnostic(t.Context(), d, id, openshell.ActivationTarget{}, func(context.Context, string) string { t.Fatal("success queried diagnostic"); return "" })
	if err != nil || got.ContainerID != id || got.PID != 42 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
