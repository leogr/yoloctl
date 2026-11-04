package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

// ImageEntrypoint reads one selected local image without pulling or creating
// anything. The caller retains the prepared-image pin; a matching tag here is
// only an observation, not authority to replace or launch a container.
func (c *Client) ImageEntrypoint(ctx context.Context, ref, architecture string) ([]string, error) {
	if ref == "" || architecture == "" {
		return nil, errors.New("image launch inspection requires a selected image and architecture")
	}
	raw, err := c.command(ctx, "sudo", "nerdctl", "image", "inspect", "--", ref).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect image launch configuration: %w", err)
	}
	return imageEntrypoint(raw, ref, architecture)
}

func imageEntrypoint(raw []byte, ref, architecture string) ([]string, error) {
	var images []struct {
		ID           string `json:"Id"`
		RepoDigests  []string
		OS           string `json:"Os"`
		Architecture string
		Config       *struct{ Entrypoint []string }
	}
	if err := json.Unmarshal(raw, &images); err != nil {
		return nil, fmt.Errorf("parse image launch configuration: %w", err)
	}
	if len(images) != 1 || images[0].Config == nil {
		return nil, errors.New("selected image has unavailable launch configuration")
	}
	image := images[0]
	configID, err := digest.Parse(image.ID)
	if err != nil || configID.Algorithm() != digest.SHA256 || image.OS != "linux" || image.Architecture != architecture {
		return nil, errors.New("selected image has unavailable configuration identity or differs from the native Linux architecture")
	}
	if strings.HasPrefix(ref, "sha256:") {
		// Main consumes a retained prepared image by its OCI target digest.
		// nerdctl's Id is a different, platform-specific CONFIG digest. Its
		// RepoDigests bind the target to this same object's selected config.
		target, err := digest.Parse(ref)
		if err != nil || target.Algorithm() != digest.SHA256 || len(image.RepoDigests) == 0 {
			return nil, errors.New("selected image lacks its complete OCI target digest")
		}
		for _, alias := range image.RepoDigests {
			name, err := reference.ParseDockerRef(alias)
			canonical, ok := name.(reference.Canonical)
			if err != nil || !ok || canonical.Digest() != target {
				return nil, errors.New("selected image target digest is foreign or ambiguous")
			}
		}
	} else if _, err := parseImageInspects(raw, []string{ref}); err != nil {
		// Named inspection keeps the existing strict tag matcher. Do not
		// change GC's tag-based identity rules to support the native digest.
		return nil, err
	}
	// nerdctl 2.3.5 omits Entrypoint when empty. A present Config object
	// without that field is valid; a missing/null Config above is not.
	return image.Config.Entrypoint, nil
}
