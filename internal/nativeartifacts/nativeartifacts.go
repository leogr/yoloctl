// Package nativeartifacts holds the release archives every yoloctl binary
// embeds for native boxes: the static OpenShell helper and the OpenShell
// sandbox and supervisor, for linux amd64 and arm64. The qualified manifest
// names each archive and its archive, member and
// binary hashes; make build writes that selection next to the archives
// (see Selection). The OpenShell installer verifies them exactly as it verified
// downloaded release archives, it only reads them from here.
//
// This package is imported only by CLI/controller code, never by the helper
// or its dependency closure, so the helper's own archive never becomes an
// input to its next build. An absent archive means this binary was built
// without the qualified artifacts: native use refuses with ErrNotBuilt.
package nativeartifacts

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
)

//go:embed all:embedded
var embedded embed.FS

// ErrNotBuilt means the requested archive is not embedded in this binary.
var ErrNotBuilt = errors.New("native artifact is not embedded")

var archiveName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,126}\.tar\.gz$`)

// ReleaseArchive returns a copy of the embedded archive NAME, the final path
// element of its qualified release URL.
func ReleaseArchive(name string) ([]byte, error) {
	return archiveIn(embedded, "embedded", name)
}

func archiveIn(fsys fs.FS, dir, name string) ([]byte, error) {
	if !archiveName.MatchString(name) {
		return nil, fmt.Errorf("invalid native release archive name %q", name)
	}
	raw, err := fs.ReadFile(fsys, dir+"/"+name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s; build yoloctl with its qualified OpenShell artifacts", ErrNotBuilt, name)
	}
	if err != nil {
		return nil, err
	}
	return bytes.Clone(raw), nil
}

// SelectionDir holds the qualified selection make build generates next to the
// archives: the manifest, its reviewed SHA-256 and the paired Rego.
const SelectionDir = "selection"

var selectionFiles = [3]string{"manifest.json", "manifest.sha256", "sandbox-policy.rego"}

// Selection returns the generated native selection, or three empty values when
// this binary was built without it (a plain go build). A partial selection is
// an error. The loader validates the contents.
func Selection() (manifest []byte, reviewedHash string, rego []byte, err error) {
	return selectionIn(embedded, "embedded/"+SelectionDir)
}

func selectionIn(fsys fs.FS, dir string) ([]byte, string, []byte, error) {
	var values [3][]byte
	present := 0
	for i, name := range selectionFiles {
		raw, err := fs.ReadFile(fsys, dir+"/"+name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", nil, err
		}
		values[i] = bytes.Clone(raw)
		present++
	}
	switch present {
	case 0:
		return nil, "", nil, nil
	case len(selectionFiles):
		return values[0], string(values[1]), values[2], nil
	}
	return nil, "", nil, errors.New("embedded native OpenShell selection is incomplete; run make build")
}
