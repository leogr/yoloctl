package agentbox

import (
	"context"
	"errors"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
)

func readNativeRuntimePolicy(ctx context.Context, client *container.Client, name string, details *container.Details) ([]byte, error) {
	if details == nil || !nativeLowerHex(details.ID, 32) {
		return nil, errors.New("native policy view lacks its observed fixed CID")
	}
	observed, err := inspectOpenShellRuntime(ctx, client, name, details.ID)
	if err != nil {
		return nil, err
	}
	if err := sameNativePolicyObservation(details, observed); err != nil {
		return nil, err
	}
	return openshell.ReadNativePolicy(ctx, observed.Record, observed.Release)
}
func sameNativePolicyObservation(details *container.Details, observed OpenShellRuntime) error {
	if details == nil || details.Name != observed.Record.Launch.Binding.Box || details.Labels[LabelAgentbox] != observed.Record.Launch.Binding.Box || details.Labels[LabelSpace] != observed.Record.Launch.Binding.Space || details.ID != observed.ContainerID || details.Running != observed.Running || details.PID != observed.PID || details.AmbiguousTaskStateObserved {
		return errors.New("native policy view changed container/process observation")
	}
	launch, ok := openShellRuntimeEvidence(details)
	if !ok || launch != observed.Launch || details.Labels[openshell.LabelControlRoot] != observed.ControlRoot {
		return errors.New("native policy view changed launch/control identity")
	}
	var started time.Time
	if details.StartedAt != "" {
		var err error
		started, err = time.Parse(time.RFC3339Nano, details.StartedAt)
		if err != nil {
			return err
		}
	}
	if !started.Equal(observed.StartedAt) {
		return errors.New("native policy view changed start history")
	}
	return nil
}
