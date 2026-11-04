package imagejob

import (
	"encoding/json"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func localRetainFixture(t *testing.T) Recipe {
	t.Helper()
	r, err := NewLocalRetain("ubuntu", "linux/amd64", LocalTarget{
		Digest: "sha256:" + strings.Repeat("a", 64), Size: 1234, MediaType: ocispec.MediaTypeImageIndex,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLocalRetainIdentityCanonicalizesSourceAndPlatformAliases(t *testing.T) {
	r := localRetainFixture(t)
	if r.BaseReference != "docker.io/library/ubuntu:latest" || r.Platform != "linux/amd64" || r.BaseDigest != r.LocalTarget.Digest || len(r.Dockerfile) != 0 {
		t.Fatal("local retention did not preserve its closed canonical inputs", r)
	}
	want, err := r.Identity()
	if err != nil {
		t.Fatal("local retention required a Dockerfile", err)
	}
	for _, source := range []string{"ubuntu", "ubuntu:latest", "docker.io/library/ubuntu:latest"} {
		for _, platform := range []string{"linux/amd64", "linux/x86_64"} {
			alias, err := NewLocalRetain(source, platform, *r.LocalTarget)
			if err != nil {
				t.Fatal(err)
			}
			got, err := alias.Identity()
			if err != nil || got != want {
				t.Fatal("equivalent local inputs did not deduplicate", source, platform, got, want, err)
			}
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Recipe
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := decoded.Identity()
	if err != nil || got != want {
		t.Fatal("serialized local descriptor changed identity", got, want, err)
	}
}

func TestLocalRetainIdentityBindsSourcePlatformAndCompleteTarget(t *testing.T) {
	r := localRetainFixture(t)
	original, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name string
		edit func(*Recipe)
	}{
		{"source", func(r *Recipe) { r.BaseReference = "docker.io/library/ubuntu:24.04" }},
		{"platform", func(r *Recipe) { r.Platform = "linux/arm64" }},
		{"digest", func(r *Recipe) {
			r.LocalTarget.Digest = "sha256:" + strings.Repeat("b", 64)
			r.BaseDigest = r.LocalTarget.Digest
		}},
		{"size", func(r *Recipe) { r.LocalTarget.Size++ }},
		{"media-type", func(r *Recipe) { r.LocalTarget.MediaType = ocispec.MediaTypeImageManifest }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := Copy(r)
			change.edit(&changed)
			identity, err := changed.Identity()
			if err != nil || identity == original {
				t.Fatal("different retained inputs shared an identity", identity, original, err)
			}
		})
	}
	ordinary := Recipe{Builder: "dockerfile/1", Platform: r.Platform, BaseReference: r.BaseReference,
		BaseDigest: r.BaseDigest, Dockerfile: []byte("FROM ubuntu:latest")}
	if identity, err := ordinary.Identity(); err != nil || identity == original {
		t.Fatal("retention collided with an ordinary build", identity, err)
	}
}

func TestLocalRetainRejectsIncompleteDescriptorsAndMixedRecipes(t *testing.T) {
	r := localRetainFixture(t)
	for _, bad := range []struct {
		name string
		edit func(*Recipe)
	}{
		{"missing-target", func(r *Recipe) { r.LocalTarget = nil }},
		{"missing-source", func(r *Recipe) { r.BaseReference = "" }},
		{"source-alias", func(r *Recipe) { r.BaseReference = "ubuntu" }},
		{"invalid-source", func(r *Recipe) { r.BaseReference = "@@" }},
		{"missing-platform", func(r *Recipe) { r.Platform = "" }},
		{"platform-alias", func(r *Recipe) { r.Platform = "linux/x86_64" }},
		{"invalid-platform", func(r *Recipe) { r.Platform = "linux/amd64/extra/extra" }},
		{"bad-digest", func(r *Recipe) { r.LocalTarget.Digest = "sha256:bad"; r.BaseDigest = r.LocalTarget.Digest }},
		{"base-target-mismatch", func(r *Recipe) { r.BaseDigest = "sha256:" + strings.Repeat("b", 64) }},
		{"zero-size", func(r *Recipe) { r.LocalTarget.Size = 0 }},
		{"negative-size", func(r *Recipe) { r.LocalTarget.Size = -1 }},
		{"missing-media-type", func(r *Recipe) { r.LocalTarget.MediaType = "" }},
		{"config-is-not-target", func(r *Recipe) { r.LocalTarget.MediaType = ocispec.MediaTypeImageConfig }},
		{"layer-is-not-target", func(r *Recipe) { r.LocalTarget.MediaType = ocispec.MediaTypeImageLayer }},
		{"dockerfile", func(r *Recipe) { r.Dockerfile = []byte("FROM ubuntu") }},
		{"images", func(r *Recipe) {
			r.Images = []ImageInput{{Reference: r.BaseReference, Platform: r.Platform, Digest: r.BaseDigest}}
		}},
		{"files", func(r *Recipe) { r.Files = map[string][]byte{"file": []byte("data")} }},
		{"file-modes", func(r *Recipe) { r.FileModes = map[string]uint32{"file": 0600} }},
		{"args", func(r *Recipe) { r.Args = map[string]string{"ARG": "value"} }},
		{"force", func(r *Recipe) { r.Force = true }},
		{"no-cache", func(r *Recipe) { r.NoCache = true }},
		{"other-builder", func(r *Recipe) { r.Builder = "pull/1"; r.Dockerfile = []byte("FROM ubuntu") }},
		{"missing-builder", func(r *Recipe) { r.Builder = "" }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			changed := Copy(r)
			bad.edit(&changed)
			if err := changed.ValidateLocalRetain(); err == nil {
				t.Fatal("invalid local retention was accepted", changed)
			}
			if _, err := changed.Identity(); err == nil {
				t.Fatal("invalid or mixed recipe acquired an identity", changed)
			}
		})
	}
	if _, err := NewLocalRetain("@@", r.Platform, *r.LocalTarget); err == nil {
		t.Fatal("constructor accepted an invalid source")
	}
	for _, platform := range []string{"", "linux", "amd64"} {
		if _, err := NewLocalRetain(r.BaseReference, platform, *r.LocalTarget); err == nil {
			t.Fatal("constructor inferred an incomplete platform", platform)
		}
	}
}

func TestLocalRetainCopyOwnsDescriptorAndRetainsValidIdentity(t *testing.T) {
	r := localRetainFixture(t)
	copy := Copy(r)
	if copy.LocalTarget == r.LocalTarget {
		t.Fatal("copied recipe aliases its source descriptor")
	}
	if err := copy.ValidateLocalRetain(); err != nil {
		t.Fatal("copy introduced irrelevant build inputs", err)
	}
	copy.LocalTarget.Size++
	if r.LocalTarget.Size != 1234 {
		t.Fatal("editing a copy changed the source descriptor")
	}
	target := *r.LocalTarget
	constructed, err := NewLocalRetain(r.BaseReference, r.Platform, target)
	if err != nil {
		t.Fatal(err)
	}
	target.Size++
	if constructed.LocalTarget.Size != r.LocalTarget.Size {
		t.Fatal("constructor retained mutable caller descriptor storage")
	}
	for _, mediaType := range []string{"application/vnd.docker.distribution.manifest.v2+json", "application/vnd.docker.distribution.manifest.list.v2+json"} {
		target.MediaType = mediaType
		if _, err := NewLocalRetain(r.BaseReference, r.Platform, target); err != nil {
			t.Fatal("supported local Docker target was refused", mediaType, err)
		}
	}
}
