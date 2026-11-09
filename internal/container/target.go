package container

import (
	"context"
	"errors"
)

// ValidateContainerID requires the full canonical runtime selector. Box names
// are at most 63 characters; a prefix is never a fixed container identity.
func ValidateContainerID(id string) error {
	if len(id) != 64 {
		return errors.New("container target requires a complete canonical ID")
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("container target requires a complete canonical ID")
		}
	}
	return nil
}

// ResolveContainerID selects an exact name through Inspect, or retains an
// already resolved full ID without another lookup. It grants no ownership or
// lifecycle authority. Callers retain this ID across their operation; a failed
// command must not resolve the name again and target a replacement.
func (c *Client) ResolveContainerID(ctx context.Context, target string) (string, error) {
	if len(target) == 64 {
		if err := ValidateContainerID(target); err != nil {
			return "", err
		}
		return target, nil
	}
	d, err := c.Inspect(ctx, target)
	if err != nil {
		return "", err
	}
	if err := ValidateContainerID(d.ID); err != nil {
		return "", err
	}
	return d.ID, nil
}
