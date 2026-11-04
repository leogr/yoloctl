//go:build !linux

package agentbox

import "context"

func (a LiveApplier) CheckMissingNativeRemoval(context.Context, string) ([]string, error) {
	return nil, nil
}
