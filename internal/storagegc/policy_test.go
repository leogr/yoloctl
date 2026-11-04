package storagegc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestPlanProtectsDesiredActualAndAliasedImages(t *testing.T) {
	id := func(s string) string { return "sha256:" + strings.Repeat(s, 64) }
	digest := "docker.io/local/base@" + id("e")
	images := []Image{
		{Ref: "yoloctl/agentbox-composed:desired", ID: id("a"), Managed: true},
		{Ref: "docker.io/yoloctl/agentbox-composed:old", ID: id("b"), Managed: true},
		{Ref: "yoloctl/agentbox-composed:alias", ID: id("b"), Managed: true},
		{Ref: "yoloctl/agentbox-composed:base", ID: id("c"), Names: []string{digest}, Managed: true},
		{Ref: "yoloctl/agentbox-composed:custom", ID: id("d"), Names: []string{"docker.io/local/custom:latest"}, Managed: true},
		{Ref: "yoloctl/agentbox-composed:foreign", ID: id("e"), Managed: false},
		{Ref: "yoloctl/agentbox-composed:garbage", ID: id("f"), Managed: true},
	}
	refs := []string{"docker.io/yoloctl/agentbox-composed:desired", id("b"), "local/base@" + id("e"), "local/custom"}
	got, err := Plan(images, refs)
	if err != nil || !reflect.DeepEqual(got, []string{"yoloctl/agentbox-composed:garbage"}) {
		t.Fatalf("unsafe retention plan: %v, %v", got, err)
	}
	refs[1] = strings.Repeat("b", 12)
	if next, err := Plan(images, refs); err != nil || !reflect.DeepEqual(next, got) {
		t.Fatalf("abbreviated identity: %v, %v", next, err)
	}
}

func TestPlanRejectsUncertainInventory(t *testing.T) {
	for _, test := range []struct {
		image Image
		refs  []string
	}{
		{Image{Ref: "yoloctl/agentbox-composed:a", Managed: true}, nil},
		{Image{Ref: "yoloctl/agentbox-composed:a", ID: "sha256:abc", Managed: true}, nil},
		{Image{Ref: "broken ref", ID: "sha256:" + strings.Repeat("a", 64), Managed: true}, nil},
		{Image{Ref: "yoloctl/agentbox-composed:a", ID: "sha256:" + strings.Repeat("a", 64), Managed: true}, []string{"invalid ref"}},
	} {
		if plan, err := Plan([]Image{test.image}, test.refs); err == nil || len(plan) != 0 {
			t.Fatalf("accepted uncertainty: %v, %v", plan, err)
		}
	}
}

func TestPlanProtectsContainerManifestDigest(t *testing.T) {
	manifest := "sha256:" + strings.Repeat("1", 64)
	config := "sha256:" + strings.Repeat("2", 64)
	images := []Image{{Ref: "yoloctl/agentbox-composed:old", ID: config, Names: []string{"docker.io/yoloctl/agentbox-composed@" + manifest}, Managed: true}}
	if plan, err := Plan(images, []string{manifest}); err != nil || len(plan) != 0 {
		t.Fatalf("lost manifest identity: %v, %v", plan, err)
	}
}

func TestBuildkitConfigPreservesSettingsAndIsIdempotent(t *testing.T) {
	raw := []byte(`debug = true
[registry."example.org"]
  mirrors = ["mirror.example.org"]
[worker.oci]
  enabled = false
[worker.containerd]
  enabled = true
  namespace = "default"
  snapshotter = "overlayfs"
  gc = false
  gckeepstorage = 23000
  max-parallelism = 2
[[worker.containerd.gcpolicy]]
  keepDuration = "1440h"
  maxUsedSpace = "80%"
`)
	disk := Disk{Total: 30 * 1024 * MiB, Available: 1024 * MiB}
	got, err := BuildkitConfig(raw, "buildkitd github.com/moby/buildkit v0.31.2 hash", disk)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Debug    bool
		Registry map[string]struct{ Mirrors []string }
		Worker   struct {
			OCI        struct{ Enabled bool }
			Containerd struct {
				Enabled                bool
				Namespace, Snapshotter string
				GC                     bool
				Parallel               int `toml:"max-parallelism"`
				Policy                 []struct {
					All                                       bool
					ReservedSpace, MaxUsedSpace, MinFreeSpace int64
				} `toml:"gcpolicy"`
			}
		}
	}
	if err := toml.Unmarshal(got, &cfg); err != nil {
		t.Fatal(err)
	}
	w := cfg.Worker.Containerd
	if !cfg.Debug || cfg.Worker.OCI.Enabled || len(cfg.Registry["example.org"].Mirrors) != 1 || cfg.Registry["example.org"].Mirrors[0] != "mirror.example.org" ||
		!w.Enabled || !w.GC || w.Namespace != "default" || w.Snapshotter != "overlayfs" || w.Parallel != 2 {
		t.Fatalf("lost unrelated settings: %s", got)
	}
	if len(w.Policy) != 1 || !w.Policy[0].All || w.Policy[0].ReservedSpace != int64(256*MiB) ||
		w.Policy[0].MaxUsedSpace != int64(disk.Total/10) || w.Policy[0].MinFreeSpace != int64(disk.Total/5) {
		t.Fatalf("wrong cache budget: %+v", w.Policy)
	}
	if strings.Contains(string(got), "gckeepstorage") || strings.Contains(string(got), "keepDuration") {
		t.Fatalf("retained old GC rule: %s", got)
	}
	withComment := append([]byte("# preserved on a no-op\n"), got...)
	next, err := BuildkitConfig(withComment, "buildkitd v0.17.0", disk)
	if err != nil || string(next) != string(withComment) {
		t.Fatalf("not idempotent: %s, %v", next, err)
	}
}

func TestBuildkitConfigRefusesUnknownWorkersAndOldVersions(t *testing.T) {
	valid := []byte("[worker.containerd]\nenabled=true\nnamespace='default'\n")
	for _, version := range []string{"buildkitd v0.16.0", "unknown"} {
		if _, err := BuildkitConfig(valid, version, Disk{Total: 30 * 1024 * MiB}); err == nil {
			t.Fatalf("accepted %s", version)
		}
	}
	for _, cfg := range []string{"", "bad[", "[worker.containerd]\nenabled=false\nnamespace='default'", "[worker.containerd]\nenabled=true\nnamespace='other'"} {
		if _, err := BuildkitConfig([]byte(cfg), "buildkitd v0.31.2", Disk{Total: 30 * 1024 * MiB}); err == nil {
			t.Fatalf("accepted %s", cfg)
		}
	}
}

func TestDiskBudgets(t *testing.T) {
	d := Disk{Total: 30 * 1024 * MiB, Available: 6 * 1024 * MiB}
	if d.Low() || d.TargetFree() != 6*1024*MiB || d.CacheBudget() != 3*1024*MiB {
		t.Fatal(d)
	}
	d.Available--
	if !d.Low() {
		t.Fatal("threshold not enforced")
	}
	d.Total = 100 * 1024 * MiB
	if d.CacheBudget() != 4*1024*MiB {
		t.Fatal("unbounded cache budget")
	}
}

func TestOCIChainIdentity(t *testing.T) {
	a, b := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64)
	for _, test := range []struct {
		layers []string
		want   string
	}{{nil, ""}, {[]string{a}, a}, {[]string{a, b}, "sha256:9932074217c35353d2e03a3f5a86549f7c67bfeb0ba53e23d69f4d4c8f7958f5"}} {
		got, err := ChainID(test.layers)
		if err != nil || got != test.want {
			t.Fatalf("%v: %q %v", test.layers, got, err)
		}
	}
	if _, err := ChainID([]string{"not-a-digest"}); err == nil {
		t.Fatal("accepted uncertain layer identity")
	}
}
