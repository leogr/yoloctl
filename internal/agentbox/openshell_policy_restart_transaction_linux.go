package agentbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

type nativePolicyRestartActions struct {
	preflight, withdraw, stop func(context.Context) error
	launch                    func(context.Context, []byte, bool) error
}

// The actual launch factory returns a plain failure only before its effects or
// after its original attempted generation's cleanup positively completed. Its
// Start/preparation/activation/cleanup uncertainty is never eligible for retry.
// Once the old Stop ACK is known, rollback is one NEW launch from the retained
// original policy/environment, not replay of the failed launch or old tokens.
func completeNativePolicyRestart(ctx context.Context, originalPolicy, newPolicy []byte, a nativePolicyRestartActions) error {
	if a.preflight == nil || a.withdraw == nil || a.stop == nil || a.launch == nil || len(originalPolicy) == 0 || len(newPolicy) == 0 {
		return errors.New("native policy restart requires original policy and complete admitted wiring")
	}
	originalPolicy, newPolicy = bytes.Clone(originalPolicy), bytes.Clone(newPolicy)
	for _, step := range []func(context.Context) error{a.preflight, a.withdraw, a.stop} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step(ctx); err != nil {
			return err
		}
	}
	// Cancellation here leaves a known stopped old runtime; there has been no
	// uncertain launch. The same authorized rollback can still restore it.
	err := ctx.Err()
	if err == nil {
		err = a.launch(ctx, bytes.Clone(newPolicy), false)
	}
	if err == nil {
		return nil
	}
	var uncertain *NativeEffectsUncertain
	if errors.As(err, &uncertain) {
		return err
	}
	restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if rollbackErr := a.launch(restore, bytes.Clone(originalPolicy), true); rollbackErr != nil {
		return &NativeEffectsUncertain{Cause: errors.Join(err, fmt.Errorf("previous policy/runtime/forwards restoration remains unproved: %w", rollbackErr))}
	}
	return fmt.Errorf("new policy failed; previous policy/runtime/forwards restored with a fresh generation: %w", err)
}
