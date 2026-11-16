package agentbox

import (
	"context"
	"errors"
)

func (a LiveApplier) prepareNativeSavedStart(ctx context.Context, c *Config, observedForwards bool) (*NativePreparedStart, error) {
	if c == nil {
		return nil, errors.New("prepared Start lacks its box declaration")
	}
	selected, checkpoint, err := a.snapshotNativeGeneration(ctx, c)
	if err != nil {
		return nil, err
	}
	// These callbacks close over the independently selected real SDK checkpoint
	// and fixed CID. Neither metadata nor caller callbacks can construct them.
	return retainNativePreparedStart(ctx, c, observedForwards, checkpoint, nativePreparedStartActions{
		qualify: func(check context.Context, cfg *Config, policy []byte) (*Config, error) {
			return a.qualifyNativePolicyRestart(check, cfg, policy, selected, checkpoint)
		},
		preserve: func(check context.Context) error { return CheckStart(check, c, LiveInspector{Client: a.Client}) },
		launch: func(start context.Context, boot *Config, policy []byte) error {
			return a.startNativeStoppedGenerationWithCheckpoint(start, boot, policy, checkpoint, selected.ContainerID)
		},
	})
}
