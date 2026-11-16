//go:build !linux

package agentbox

import (
	"context"
	"errors"
	"github.com/leogr/yoloctl/internal/container"
)

func VerifyFileExportRuntime(context.Context, *container.Client, string, *container.Details) error {
	return errors.New("native export verification is available only in the Linux controller")
}
