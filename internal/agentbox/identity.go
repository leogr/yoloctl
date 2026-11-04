package agentbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/manifestio"
)

// NewIdentity is independent of the box name, account and container incarnation.
// Declaration identities are compared only together with the native disk ID.
func NewIdentity() string {
	var id [16]byte
	_, _ = rand.Read(id[:]) // crypto/rand.Read cannot fail on supported Go versions.
	return hex.EncodeToString(id[:])
}

// preserveIdentity runs under the caller's declaration lock. Existing identities
// cannot be edited away or replaced. A copied box is built from create options,
// which deliberately do not include ID. Import reidentifies declarations before
// publishing a new store, without going through Save.
func (c *Config) preserveIdentity(path string) error {
	raw, err := manifestio.Read(path)
	if err == nil {
		old, err := ParseManifest(raw)
		if err != nil {
			return err
		}
		if old.ID != "" {
			if c.ID != "" && c.ID != old.ID {
				return errors.New("box declaration identity cannot be changed")
			}
			c.ID = old.ID
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if c.ID == "" {
		return nil // Ordinary legacy saves must not introduce a new manifest field.
	}
	return layout.ValidateIdentity(c.ID)
}
