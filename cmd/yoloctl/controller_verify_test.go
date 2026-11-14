package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

type recoveryContainerFixture struct {
	list                  []container.Container
	detail                *container.Details
	listErr, errorInspect error
	inspected             string
	filter                string
}

func (f *recoveryContainerFixture) List(_ context.Context, filter string) ([]container.Container, error) {
	f.filter = filter
	return f.list, f.listErr
}
func (f *recoveryContainerFixture) Inspect(_ context.Context, id string) (*container.Details, error) {
	f.inspected = id
	return f.detail, f.errorInspect
}

func TestRecoveryContainerObservationDoesNotInferSuccessOrAbsenceFromFailure(t *testing.T) {
	for _, kind := range []string{"running", "stopped", "absent", "list-error", "inspect-error", "foreign-space", "foreign-box", "multiple", "nil", "ambiguous", "missing-task", "bad-id", "wrong-name"} {
		t.Run(kind, func(t *testing.T) {
			fullID := strings.Repeat("a", 64)
			f := &recoveryContainerFixture{list: []container.Container{{ID: fullID[:12], Name: "web"}}, detail: &container.Details{ID: fullID, Name: "web", Running: true, Labels: map[string]string{agentbox.LabelSpace: "work", agentbox.LabelAgentbox: "web"}}}
			want := "not observed"
			switch kind {
			case "running":
				want = "running"
			case "stopped":
				f.detail.Running = false
				f.detail.InactiveTaskFieldsObserved = true
				want = "stopped"
			case "absent":
				f.list = nil
				want = "absent"
			case "list-error":
				f.listErr = errors.New("engine unavailable")
			case "inspect-error":
				f.errorInspect = errors.New("inspection failed")
			case "foreign-space":
				f.detail.Labels[agentbox.LabelSpace] = "other"
			case "foreign-box":
				f.detail.Labels[agentbox.LabelAgentbox] = "other"
			case "multiple":
				f.list = append(f.list, f.list[0])
			case "nil":
				f.detail = nil
			case "ambiguous":
				f.detail.AmbiguousTaskStateObserved = true
			case "bad-id":
				f.detail.ID = strings.Repeat("A", 64)
			case "wrong-name":
				f.detail.Name = "other"
			case "missing-task":
				f.detail.Running = false
			}
			observations := observeRecoveryBoxes(t.Context(), "work", []string{"web"}, f)
			if len(observations) != 1 || observations[0].Name != "web" || observations[0].State != want {
				t.Fatal(observations)
			}
			if f.filter != agentbox.LabelAgentbox+"=web" {
				t.Fatal(f.filter)
			}
			if want == "running" || want == "stopped" {
				if observations[0].ID != fullID || f.inspected != fullID[:12] {
					t.Fatal(observations, f.inspected)
				}
			} else if observations[0].ID != "" {
				t.Fatal("invented current identity", observations)
			}
			if want == "not observed" && observations[0].Reason == "" {
				t.Fatal("failed observation has no reason")
			}
		})
	}
}
