package container

import (
	"context"
	"errors"
)

// StopContainerID requests one actual stop of an independently owned full CID.
// Immediate preserves forced destruction's SIGKILL/no-grace behavior using
// nerdctl's explicit zero timeout. Both paths retain the real command reply;
// inactivity, namespace retirement, remaining consumers and nonforced removal
// still need their independent proofs. No name lookup, retry or gone fallback.
func (c *Client) StopContainerID(ctx context.Context, id string, immediate bool) error {
	if len(id) != 64 {
		return errors.New("container stop requires a complete owned ID")
	}
	for _, r := range id {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return errors.New("container stop requires a canonical owned ID")
		}
	}
	grace := stopGraceSeconds
	if immediate {
		grace = "0"
	}
	return c.nerdctl(ctx, "stop", "--time", grace, id)
}
