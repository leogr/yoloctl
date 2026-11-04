//go:build !linux

package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
)

func readNativeRuntimePolicy(context.Context, *container.Client, string, *container.Details) ([]byte, error) {
	return nil, errors.New("native policy reads require the Linux VM controller")
}
