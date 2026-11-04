//go:build linux && amd64

package firecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// Bump when assembly logic changes the image without changing pinned inputs or
// the init binary. This version is independent of the guest wire protocol.
const imageRecipeVersion = 4

func defaultRecipe() (string, error) {
	inputs := map[string]string{}
	for _, name := range []string{"pins.json", "runtime-pins.json"} {
		raw, err := assets.VMImageRecipe.ReadFile("vm/firecracker/" + name)
		if err != nil {
			return "", err
		}
		inputs[name] = string(raw)
	}
	for _, name := range []string{"kernel.json", "kernel.config"} {
		raw, err := assets.VMKernel.ReadFile("vm/firecracker/" + name)
		if err != nil {
			return "", err
		}
		inputs[name] = string(raw)
	}
	return recipeIdentity(imageRecipeVersion, vmwire.Protocol, guestbinary.InitAMD64SHA256, DefaultImageURL, DefaultImageSHA256, inputs)
}

func recipeIdentity(version, protocol int, init, url, sum string, inputs map[string]string) (string, error) {
	if url == "" && sum == "" {
		if raw, err := hex.DecodeString(init); err != nil || len(raw) != sha256.Size {
			return "", errors.New("automatic VM image selection needs the complete yoloctl bundle; rebuild/install it or supply --vm-image")
		}
	}
	data, err := json.Marshal(struct {
		Recipe, Protocol  int
		Init, URL, SHA256 string
		Inputs            map[string]string
	}{version, protocol, init, url, sum, inputs})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// ReadDefaultImage returns only this build's already prepared default. It
// never acquires preparation locks, creates roots, or invokes ResolveImage.
func ReadDefaultImage() (string, error) {
	recipe, err := defaultRecipe()
	if err != nil {
		return "", err
	}
	root, err := layout.VMImagesDir("firecracker")
	if err != nil {
		return "", err
	}
	raw, err := safepath.ReadFile(root, "default-"+recipe+".json", 4096)
	if err != nil {
		return "", err
	}
	var id string
	if err := strictJSON(raw, &id); err != nil {
		return "", err
	}
	sum, err := hex.DecodeString(id)
	if err != nil || len(sum) != sha256.Size {
		return "", errors.New("invalid default image identity")
	}
	path := filepath.Join(root, id)
	if err := verifyCachedIdentity(path, id); err != nil {
		return "", err
	}
	return path, nil
}
