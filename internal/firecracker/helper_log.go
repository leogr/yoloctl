package firecracker

import (
	"context"
	"errors"
	"log/slog"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// helperLogFinalizer reads the named result when the helper returns. Both the
// early-return cleanup and the pre-retirement defer use it, on that goroutine.
func helperLogFinalizer(fileLog *prettylog.Logger, log *slog.Logger, result *error) func() {
	finished := false
	return func() {
		if finished {
			return
		}
		finished = true
		if *result != nil && !errors.Is(*result, context.Canceled) {
			log.Error("VM helper ended", prettylog.Err(*result))
		}
		_ = fileLog.Close()
	}
}
