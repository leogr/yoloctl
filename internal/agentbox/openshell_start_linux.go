package agentbox

import "context"

func (a LiveApplier) prepareNativeRecordedStart(ctx context.Context, c *Config, observedForwards bool) (*NativeRecordedStart, error) {
	selected, checkpoint, err := a.snapshotNativeGeneration(ctx, c)
	if err != nil {
		return nil, err
	}
	// These callbacks close over the independently selected real SDK checkpoint
	// and fixed CID. Neither metadata nor caller callbacks can construct them.
	plain := a
	plain.nativeForwardRestoration = nil
	return retainNativeRecordedStart(ctx, c, observedForwards, checkpoint, nativeRecordedStartActions{
		qualify: func(check context.Context, cfg *Config, policy []byte) (*Config, error) {
			return plain.qualifyNativePolicyRestart(check, cfg, policy, selected, checkpoint)
		},
		preserve: func(check context.Context) error { return CheckStart(check, c, LiveInspector{Client: a.Client}) },
		launch: func(start context.Context, boot *Config, policy []byte) error {
			return plain.startNativeStoppedGenerationWithCheckpoint(start, boot, policy, checkpoint, selected.ContainerID)
		},
	})
}
