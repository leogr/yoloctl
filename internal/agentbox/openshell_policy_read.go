package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/sandbox"
)

// ReadRuntimePolicy reads the policy selected by the already-inspected runtime.
// Native generations use their qualified private record; an unavailable or
// mixed native identity never falls back to the legacy host mirror. This is
// read-only observation, not recovery or launch authority.
func (i LiveInspector) ReadRuntimePolicy(ctx context.Context, details *container.Details) ([]byte, error) {
	if i.Client == nil || details == nil || details.Name == "" {
		return nil, errors.New("runtime policy view requires its inspected named container")
	}
	return readInspectedRuntimePolicy(ctx, details,
		func(read context.Context) ([]byte, error) {
			return readNativeRuntimePolicy(read, i.Client, details.Name, details)
		},
		func(read context.Context) ([]byte, error) {
			return i.Client.ReadFile(read, sandbox.AgentboxPolicyHostPath(details.Name))
		})
}

func readInspectedRuntimePolicy(ctx context.Context, details *container.Details, native, legacy func(context.Context) ([]byte, error)) ([]byte, error) {
	if details == nil || native == nil || legacy == nil {
		return nil, errors.New("runtime policy read lacks inspected evidence/callbacks")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hasOpenShellRuntimeSignals(details) {
		if ClassifyRuntime(details).Mode != RuntimeModeConfined {
			return nil, errors.New("policy read refuses mixed or incomplete native runtime evidence")
		}
		return native(ctx)
	}
	return legacy(ctx)
}
