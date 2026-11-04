package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
)

type gcFixture struct {
	images     []container.Image
	details    map[string]container.ImageDetails
	boxes      []container.Container
	containers map[string]*container.Details
	parents    map[string]string
	fail       string
	removed    []string
}

func (f *gcFixture) ImagesList(context.Context, string) ([]container.Image, error) {
	if f.fail == "images" {
		return nil, errors.New("images failed")
	}
	return f.images, nil
}
func (f *gcFixture) InspectImages(_ context.Context, refs []string) ([]container.ImageDetails, error) {
	var out []container.ImageDetails
	for _, ref := range refs {
		if f.fail == ref {
			return nil, errors.New("image disappeared")
		}
		out = append(out, f.details[ref])
	}
	return out, nil
}
func (f *gcFixture) List(_ context.Context, filter string) ([]container.Container, error) {
	if filter != "" {
		panic("must include non-yoloctl containers")
	}
	if f.fail == "containers" {
		return nil, errors.New("containers failed")
	}
	return f.boxes, nil
}
func (f *gcFixture) Inspect(_ context.Context, name string) (*container.Details, error) {
	if f.fail == name {
		return nil, errors.New("container disappeared")
	}
	return f.containers[name], nil
}
func (f *gcFixture) ImageRemove(_ context.Context, ref string) error {
	f.removed = append(f.removed, ref)
	return nil
}

func (f *gcFixture) SnapshotParent(_ context.Context, name string) (string, error) {
	parent, ok := f.parents[name]
	if !ok {
		return "", errors.New("snapshot unavailable")
	}
	return parent, nil
}

func imageGCFixture() *gcFixture {
	f := &gcFixture{details: map[string]container.ImageDetails{}, containers: map[string]*container.Details{}, parents: map[string]string{}}
	for n, tag := range []string{"desired", "actual", "base", "obsolete", "foreign"} {
		id := "sha256:" + strings.Repeat(string(rune('a'+n)), 64)
		ref := agentboximage.ComposedImageRepo + ":" + tag
		f.images = append(f.images, container.Image{Repository: agentboximage.ComposedImageRepo, Tag: tag, ID: id})
		d := container.ImageDetails{ID: id, RepoTags: []string{"docker.io/" + ref}}
		d.RootFS.Type = "layers"
		d.RootFS.Layers = []string{id} // distinct unpacked roots, one layer each
		d.RepoDigests = []string{"docker.io/" + agentboximage.ComposedImageRepo + "@sha256:" + strings.Repeat(string(rune('1'+n)), 64)}
		if tag != "foreign" {
			d.Config.Labels = map[string]string{"yoloctl.kind": "composed"}
		}
		f.details[ref] = d
	}
	f.boxes = []container.Container{{Name: "stopped-external"}}
	// nerdctl 2.3.5 reports the mutable image name in both fields. Its actual
	// rootfs, not the moved tag, supplies the immutable retention identity.
	f.containers["stopped-external"] = &container.Details{Image: "local/moved:latest", ImageID: "local/moved:latest"}
	f.parents["stopped-external"] = f.images[1].ID
	return f
}

func TestImagePruneUsesActualStoppedContainersAndDesiredBases(t *testing.T) {
	f := imageGCFixture()
	refs := []string{"docker.io/" + agentboximage.ComposedImageRepo + ":desired", agentboximage.ComposedImageRepo + ":base"}
	want := []string{agentboximage.ComposedImageRepo + ":obsolete"}
	var out bytes.Buffer
	if n, err := collectImages(t.Context(), f, refs, &out, true); err != nil || n != 0 || len(f.removed) != 0 || !strings.Contains(out.String(), "No images removed") {
		t.Fatalf("dry run: %d, %v, %v, %s", n, err, f.removed, out.String())
	}
	if n, err := collectImages(t.Context(), f, refs, io.Discard, false); err != nil || n != 1 || !reflect.DeepEqual(f.removed, want) {
		t.Fatalf("prune: %d, %v, %v", n, err, f.removed)
	}
}

func TestImagePruneInventoryFailureRemovesNothing(t *testing.T) {
	for _, fail := range []string{"containers", "images", "stopped-external", agentboximage.ComposedImageRepo + ":foreign", "missing identity", "malformed list"} {
		t.Run(fail, func(t *testing.T) {
			f := imageGCFixture()
			f.fail = fail
			if fail == "missing identity" {
				f.containers["stopped-external"].ImageID = ""
				delete(f.parents, "stopped-external")
			}
			if fail == "malformed list" {
				f.images = append(f.images, container.Image{})
			}
			if _, err := collectImages(t.Context(), f, nil, io.Discard, false); err == nil || len(f.removed) != 0 {
				t.Fatalf("unsafe prune: %v, %v", err, f.removed)
			}
		})
	}
}

func TestImagePruneResolvesExternalAliases(t *testing.T) {
	f := imageGCFixture()
	alias := container.Image{Repository: "local/saved", Tag: "latest", ID: f.images[3].ID}
	f.images = append(f.images, alias)
	// nerdctl inspect returns just the inspected tag, not all aliases.
	detail := f.details[agentboximage.ComposedImageRepo+":obsolete"]
	detail.RepoTags = []string{"local/saved:latest"}
	f.details["local/saved:latest"] = detail
	refs := []string{agentboximage.ComposedImageRepo + ":desired", agentboximage.ComposedImageRepo + ":base", "local/saved"}
	if n, err := collectImages(t.Context(), f, refs, io.Discard, false); err != nil || n != 0 {
		t.Fatalf("lost desired alias: %d, %v, %v", n, err, f.removed)
	}
}

func TestImagePruneNerdctl24UsesRecordedManifestDigest(t *testing.T) {
	f := imageGCFixture()
	f.containers["stopped-external"].ImageID = "sha256:" + strings.Repeat("2", 64)
	f.parents = nil // the digest path must not need a snapshot lookup
	refs := []string{agentboximage.ComposedImageRepo + ":desired", agentboximage.ComposedImageRepo + ":base"}
	if n, err := collectImages(t.Context(), f, refs, io.Discard, false); err != nil || n != 1 || f.removed[0] != agentboximage.ComposedImageRepo+":obsolete" {
		t.Fatalf("nerdctl 2.4 digest retention: %d %v %v", n, err, f.removed)
	}
}

func TestImagePruneSkipsUntaggedEntries(t *testing.T) {
	f := imageGCFixture()
	f.images = append(f.images, container.Image{Repository: "<none>", Tag: "<none>", ID: "dangling"})
	if _, err := collectImages(t.Context(), f, nil, io.Discard, true); err != nil {
		t.Fatal(err)
	}
}

func TestImagePruneUnnamedContainerIdentifiesBlocker(t *testing.T) {
	f := imageGCFixture()
	f.boxes = append(f.boxes, container.Container{ID: "external-id"})
	if _, err := collectImages(t.Context(), f, nil, io.Discard, false); err == nil || !strings.Contains(err.Error(), "external-id") || len(f.removed) != 0 {
		t.Fatalf("unnamed container: %v; removed %v", err, f.removed)
	}
}
