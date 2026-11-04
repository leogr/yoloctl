package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// BuildID is linked once into all binaries in a coordinated build. Unstamped
// standalone Linux builds identify the actual executable deployed into the VM.
var BuildID string

func ExecutableIdentity() (string, error) {
	if BuildID != "" {
		return BuildID, nil
	}
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
