package ocifeature

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

// CommonReferences seeds discovery. Arbitrary public registry refs remain valid;
// there is no universal cross-registry search API.
var CommonReferences = []string{
	"ghcr.io/devcontainers/features/go:1", "ghcr.io/devcontainers/features/python:1",
	"ghcr.io/devcontainers/features/node:1", "ghcr.io/devcontainers/features/rust:1",
	"ghcr.io/devcontainers/features/java:1", "ghcr.io/devcontainers/features/github-cli:1",
}

func cacheDir() string {
	p, e := layout.OCIFeatureCacheDir()
	if e != nil {
		return ""
	}
	return p
}
func remember(l Lock) {
	root := cacheDir()
	if root == "" {
		return
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return
	}
	data, e := json.Marshal(l)
	if e == nil {
		_ = safepath.WriteFile(root, strings.TrimPrefix(digest([]byte(l.Ref)), "sha256:")+".json", data, 0600)
	}
}

// Completion metadata is advisory only: never a source for declaration locks.
// Bounded failures simply omit suggestions. Freshly saved declarations supply
// their own authoritative metadata to option completion.
func MetadataForCompletion(ctx context.Context, ref string) (Metadata, error) {
	root := cacheDir()
	name := strings.TrimPrefix(digest([]byte(ref)), "sha256:") + ".json"
	if data, info, e := safepath.ReadFileInfo(root, name, 2*maxMetadata); e == nil && time.Since(info.ModTime()) < time.Hour {
		var l Lock
		if json.Unmarshal(data, &l) == nil && l.Ref == ref {
			return l.Definition()
		}
	}
	l, e := NewRegistry().Resolve(ctx, ref)
	if e != nil {
		return Metadata{}, e
	}
	return l.Definition()
}
func CachedReferences() []string {
	root := cacheDir()
	entries, e := os.ReadDir(root)
	if e != nil {
		return nil
	}
	out := []string{}
	for i, entry := range entries {
		if i >= 256 {
			break
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, e := safepath.ReadFile(root, entry.Name(), 2*maxMetadata)
		if e != nil {
			continue
		}
		var l Lock
		if json.Unmarshal(data, &l) != nil {
			continue
		}
		if _, e := Parse(l.Ref); e == nil {
			out = append(out, l.Ref)
		}
	}
	return out
}
