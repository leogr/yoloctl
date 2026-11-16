package agentbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

type nativePolicyRestartActions struct {
	preflight, withdraw, stop func(context.Context) error
	launch                    func(context.Context, []byte) error
}

// The actual launch factory returns a plain failure only before its effects or
// after its attempted generation's cleanup positively completed. Its
// Start/preparation/activation/cleanup uncertainty is never eligible for retry.
// A new run launches only the saved new policy: after the old Stop ACK, a
// failure or a cancellation never launches the previous policy again.
func completeNativePolicyRestart(ctx context.Context, newPolicy []byte, a nativePolicyRestartActions) error {
	if a.preflight == nil || a.withdraw == nil || a.stop == nil || a.launch == nil || len(newPolicy) == 0 {
		return errors.New("native policy restart requires the saved policy and complete admitted wiring")
	}
	newPolicy = bytes.Clone(newPolicy)
	for _, step := range []func(context.Context) error{a.preflight, a.withdraw, a.stop} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step(ctx); err != nil {
			return err
		}
	}
	// Cancellation here leaves a known stopped old runtime; nothing is launched.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("canceled after the previous runtime stopped; the saved policy was not launched and the previous policy is not restored: %w", err)
	}
	err := a.launch(ctx, bytes.Clone(newPolicy))
	if err == nil {
		return nil
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(err, &uncertain) {
		return err
	}
	// The launch's own cleanup ran; the box's state is what a read shows.
	return fmt.Errorf("the saved policy failed to start; the previous policy is not restored: %w", err)
}
