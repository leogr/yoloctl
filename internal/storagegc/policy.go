// Package storagegc defines retention for disposable VM image/build storage.
// It never treats a container, account, workdir or arbitrary guest file as garbage.
package storagegc

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"golang.org/x/sys/unix"
)

const MiB = uint64(1024 * 1024)

type Disk struct{ Total, Available uint64 }

func RootDisk() (Disk, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err != nil {
		return Disk{}, err
	}
	d := Disk{Total: stat.Blocks * uint64(stat.Bsize), Available: stat.Bavail * uint64(stat.Bsize)}
	if d.Total == 0 || d.Available > d.Total {
		return Disk{}, fmt.Errorf("invalid root filesystem capacity")
	}
	return d, nil
}

func (d Disk) TargetFree() uint64 { return d.Total / 5 }
func (d Disk) Low() bool          { return d.Available < d.TargetFree() }

// CacheBudget is a soft GC target, not a limit on retained image layers.
func (d Disk) CacheBudget() uint64 { return min(4*1024*MiB, d.Total/10) }

type Image struct {
	Ref, ID string
	Names   []string // tags and repo digests from image inspect
	Managed bool     // proven composed-image provenance, not just a matching name
}

// Plan protects references and image identities before choosing managed tags.
// Aliases, digest-pinned bases, desired images and stopped-container images all
// participate. Callers must supply a complete inventory under the space lock.
func Plan(images []Image, references []string) ([]string, error) {
	protected := map[string]bool{}
	var ids []string
	for _, ref := range references {
		if id := imageID(ref); id != "" {
			ids = append(ids, id)
			continue
		}
		name, err := canonical(ref)
		if err != nil {
			return nil, err
		}
		protected[name] = true
	}
	keptIDs := map[string]bool{}
	for _, img := range images {
		if !IsDigestID(img.ID) {
			return nil, fmt.Errorf("image %q has no valid immutable identity; refusing incomplete inventory", img.Ref)
		}
		for _, name := range append(slices.Clone(img.Names), img.Ref) {
			key, err := canonical(name)
			if err != nil {
				return nil, err
			}
			if protected[key] {
				keptIDs[img.ID] = true
			}
			// nerdctl 2.4+ can report a recorded manifest/index digest,
			// whereas image inspect's Id is the platform config digest.
			if _, digest, ok := strings.Cut(key, "@"); ok {
				for _, id := range ids {
					if strings.HasPrefix(strings.TrimPrefix(digest, "sha256:"), id) {
						keptIDs[img.ID] = true
					}
				}
			}
		}
		for _, id := range ids {
			if strings.HasPrefix(strings.TrimPrefix(img.ID, "sha256:"), id) {
				keptIDs[img.ID] = true
			}
		}
	}
	var remove []string
	for _, img := range images {
		if img.Managed && !keptIDs[img.ID] {
			remove = append(remove, img.Ref)
		}
	}
	slices.Sort(remove)
	return slices.Compact(remove), nil
}

func canonical(ref string) (string, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", fmt.Errorf("invalid image reference %q: %w", ref, err)
	}
	if digested, ok := named.(reference.Digested); ok {
		return reference.TrimNamed(named).String() + "@" + digested.Digest().String(), nil
	}
	return reference.TagNameOnly(named).String(), nil
}

func imageID(s string) string {
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) < 12 || len(s) > 64 {
		return ""
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	return s
}

// IsDigestID distinguishes an immutable OCI SHA256 identity from an image tag.
func IsDigestID(s string) bool {
	return strings.HasPrefix(s, "sha256:") && len(imageID(s)) == 64
}

// ChainID identifies the ordered unpacked OCI layers, independently of tags.
// See image-spec v1.1.1, identity/chainid.go. An empty chain is a scratch rootfs.
func ChainID(layers []string) (string, error) {
	chain := ""
	for _, layer := range layers {
		if !IsDigestID(layer) {
			return "", fmt.Errorf("invalid layer identity %q", layer)
		}
		if chain == "" {
			chain = layer
		} else {
			chain = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(chain+" "+layer)))
		}
	}
	return chain, nil
}
