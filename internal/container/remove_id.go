package container

import (
	"context"
	"errors"
)

// RemoveContainerID removes exactly the acknowledged container resource,
// without force. It does not interpret the ID as a box name or remove operator
// projections. Startup rollback owns those separately, after independently
// proving their incarnation and acknowledged container removal. The caller
// must retain its own create history and current ownership/admission proof;
// a later name lookup is not a substitute. Errors are never suppressed using
// a subsequent absent/stopped observation.
func (c *Client) RemoveContainerID(ctx context.Context, id string) error {
	if len(id) != 64 {
		return errors.New("container removal requires a complete acknowledged ID")
	}
	for _, r := range id {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return errors.New("container removal requires a canonical acknowledged ID")
		}
	}
	return c.nerdctl(ctx, "rm", id)
}
