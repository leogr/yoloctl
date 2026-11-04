// Package imagejob defines immutable inputs shared by image preparation and
// the controller-owned build registry. It grants no execution authority.
package imagejob

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"sort"
	"strconv"

	digest "github.com/opencontainers/go-digest"
)

// Recipe contains copied, validated build inputs, never host paths to
// mutable context files. The backend must require the immutable BaseDigest.
type Recipe struct {
	Images        []ImageInput      `json:"images,omitempty"`
	Builder       string            `json:"builder"`
	Platform      string            `json:"platform"`
	BaseReference string            `json:"base_reference,omitempty"`
	BaseDigest    string            `json:"base_digest"`
	LocalTarget   *LocalTarget      `json:"local_target,omitempty"`
	Dockerfile    []byte            `json:"dockerfile"`
	Files         map[string][]byte `json:"files,omitempty"`
	FileModes     map[string]uint32 `json:"file_modes,omitempty"`
	NoCache       bool              `json:"no_cache,omitempty"`
	Args          map[string]string `json:"args,omitempty"`
	Force         bool              `json:"force,omitempty"`
}

func hashField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

// Identity is canonical over every input byte. The random refresh argument is
// allocated by the job owner, after deduplication, and is not a safety key.
func (r Recipe) Identity() (string, error) {
	if r.Builder == LocalRetainBuilder {
		return r.localRetainIdentity()
	}
	if r.LocalTarget != nil {
		return "", errors.New("local target requires the local-retain builder")
	}
	if r.Builder == "" || r.Platform == "" || len(r.Dockerfile) == 0 {
		return "", errors.New("incomplete image recipe")
	}
	if err := digest.Digest(r.BaseDigest).Validate(); err != nil {
		return "", fmt.Errorf("image base must be immutable: %w", err)
	}
	h := sha256.New()
	for _, s := range []string{"yoloctl image input v1", r.Builder, r.Platform, r.BaseDigest} {
		hashField(h, []byte(s))
	}
	hashField(h, r.Dockerfile)
	images := append([]ImageInput(nil), r.Images...)
	sort.Slice(images, func(i, j int) bool {
		if images[i].Reference != images[j].Reference {
			return images[i].Reference < images[j].Reference
		}
		return images[i].Platform < images[j].Platform
	})
	for _, input := range images {
		if _, err := CanonicalReference(input.Reference); err != nil {
			return "", err
		}
		if err := digest.Digest(input.Digest).Validate(); err != nil {
			return "", fmt.Errorf("image dependency must be immutable: %w", err)
		}
		for _, s := range []string{input.Reference, input.Platform, input.Digest} {
			hashField(h, []byte(s))
		}
	}

	names := make([]string, 0, len(r.Files))
	for name := range r.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !fs.ValidPath(name) || name == "Dockerfile" {
			return "", fmt.Errorf("invalid build-context path %q", name)
		}
		mode := uint32(0755)
		if explicit, ok := r.FileModes[name]; ok {
			mode = explicit
		}
		if mode&^0777 != 0 {
			return "", errors.New("invalid build-context mode")
		}
		hashField(h, []byte("file"))
		hashField(h, []byte(name))
		hashField(h, r.Files[name])
		hashField(h, []byte(strconv.FormatUint(uint64(mode), 8)))
	}
	names = names[:0]
	for name := range r.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		hashField(h, []byte("arg"))
		hashField(h, []byte(name))
		hashField(h, []byte(r.Args[name]))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func Copy(r Recipe) Recipe {
	if r.LocalTarget != nil {
		target := *r.LocalTarget
		r.LocalTarget = &target
	}
	r.Images = append([]ImageInput(nil), r.Images...)
	r.Dockerfile = append([]byte(nil), r.Dockerfile...)
	files := make(map[string][]byte, len(r.Files))
	for k, v := range r.Files {
		files[k] = append([]byte(nil), v...)
	}
	r.Files = files
	modes := make(map[string]uint32, len(r.FileModes))
	for k, v := range r.FileModes {
		modes[k] = v
	}
	r.FileModes = modes
	args := make(map[string]string, len(r.Args))
	for k, v := range r.Args {
		args[k] = v
	}
	r.Args = args
	return r
}
