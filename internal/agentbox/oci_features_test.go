package agentbox

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/ocifeature"
)

func TestOCIFeaturesPersistOfflineAndDriveImageDrift(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ref := "ghcr.io/example/tool:1"
	lock := ocifeature.Lock{Ref: ref, Digest: "sha256:" + strings.Repeat("a", 64), Layer: ocifeature.Descriptor{Digest: "sha256:" + strings.Repeat("b", 64), MediaType: "application/vnd.devcontainers.layer.v1+tar", Size: 123}, Metadata: `{"id":"tool","version":"1","options":{"version":{"type":"string","default":"latest"}}}`}
	c := &Config{Name: "test", Space: "default", Image: "ubuntu:24.04", Features: []string{ref}, FeatureLocks: []ocifeature.Lock{lock}}
	if e := c.Save(); e != nil {
		t.Fatal(e)
	}
	before, _, e := ResolvedImage(c)
	if e != nil {
		t.Fatal(e)
	}
	loaded, e := Load("default", "test")
	if e != nil {
		t.Fatal(e)
	}
	same, _, e := ResolvedImage(loaded)
	if e != nil || same != before {
		t.Fatal(same, e)
	}
	loaded.FeatureOptions = map[string]map[string]string{ref: {"version": "v2"}}
	after, _, e := ResolvedImage(loaded)
	if e != nil || after == before {
		t.Fatal(after, e)
	}
	if e = loaded.Save(); e != nil {
		t.Fatal(e)
	}
	loaded.FeatureOptions[ref]["unknown"] = "x"
	if e = loaded.Save(); e == nil {
		t.Fatal("unknown option persisted")
	}
	// OCI features add no runtime capabilities.
	caps, e := EffectiveCapabilities(c)
	if e != nil {
		t.Fatal(e)
	}
	if len(caps.Capabilities()) != 0 {
		t.Fatalf("OCI feature added authority: %+v", caps.Capabilities())
	}
}
