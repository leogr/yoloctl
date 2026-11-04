package agentboximage

import (
	"context"
	"errors"
)

// RequirePreparedImage consumes the enclosing controller's retained image
// preparation. Native launch must never fall back to building, pulling or
// selecting a mutable image while holding mutation admission or after holder
// teardown. The image pin remains owned by the enclosing controller request.
func RequirePreparedImage(ctx context.Context, c Client, base string, candidates []string, opts Options) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref, handled, err := preparedImage(ctx, c, base, candidates, opts)
	if err != nil {
		return "", err
	}
	if !handled || ref == "" {
		return "", errors.New("native launch requires the controller's retained prepared image; no build/pull fallback")
	}
	return ref, nil
}
