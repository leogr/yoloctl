// Package auditplugin embeds the qualified Linux extraction plugin. Ordinary
// builds need Go alone; maintenance uses the pinned C ABI and Zig recipe.
package auditplugin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
)

//go:embed embedded/*
var files embed.FS

func Manifest() []byte { raw, _ := files.ReadFile("embedded/manifest.json"); return raw }
func QualificationDigest() string {
	sum := sha256.Sum256(Manifest())
	return hex.EncodeToString(sum[:])
}
func Plugin(arch string) ([]byte, string, error) {
	var manifest struct {
		Artifacts map[string]struct {
			File, SHA256 string
			Bytes        int
		}
	}
	if err := json.Unmarshal(Manifest(), &manifest); err != nil {
		return nil, "", err
	}
	pin, ok := manifest.Artifacts[arch]
	if !ok {
		return nil, "", errors.New("unsupported audit plugin architecture")
	}
	raw, err := files.ReadFile("embedded/" + pin.File)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	if len(raw) != pin.Bytes || hex.EncodeToString(sum[:]) != pin.SHA256 {
		return nil, "", errors.New("embedded audit plugin does not match its pin")
	}
	return raw, pin.SHA256, nil
}
