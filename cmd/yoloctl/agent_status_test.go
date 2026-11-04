package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestAgentVersionDrift(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		installed string
		latest    string
		want      string
	}{
		{name: "unknown_without_installed", installed: "", latest: "1.2.3", want: "unknown"},
		{name: "unknown_without_latest", installed: "1.2.3", latest: "", want: "unknown"},
		{name: "up_to_date", installed: "1.2.3", latest: "1.2.3", want: "up to date"},
		{name: "one_patch", installed: "1.2.3", latest: "1.2.4", want: "1 patch behind"},
		{name: "many_patches", installed: "1.2.3", latest: "1.2.7", want: "4 patches behind"},
		{name: "minor_behind", installed: "1.2.9", latest: "1.3.0", want: "behind"},
		{name: "major_behind", installed: "1.9.9", latest: "2.0.0", want: "behind"},
		{name: "ahead", installed: "1.3.0", latest: "1.2.9", want: "ahead"},
		{name: "v_prefix", installed: "v1.2.3", latest: "1.2.4", want: "1 patch behind"},
		{name: "unparseable", installed: "nightly", latest: "1.2.3", want: "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentVersionDrift(c.installed, c.latest); got != c.want {
				t.Errorf("agentVersionDrift(%q, %q) = %q, want %q",
					c.installed, c.latest, got, c.want)
			}
		})
	}
}

func TestNpmLatestCacheRoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	info := &agentLatestInfo{
		Latest:    "1.2.3",
		Published: now.Add(-24 * time.Hour),
	}
	if err := writeNpmLatestCache("@scope/pkg", info, now); err != nil {
		t.Fatalf("writeNpmLatestCache: %v", err)
	}
	path, err := layout.NpmLatestCachePath("@scope/pkg")
	if err != nil {
		t.Fatalf("layout.NpmLatestCachePath: %v", err)
	}
	if filepath.Base(path) != "@scope%2Fpkg.json" {
		t.Fatalf("cache filename = %q, want escaped scoped package", filepath.Base(path))
	}
	got, ok := readNpmLatestCache("@scope/pkg", time.Hour, now.Add(30*time.Minute))
	if !ok {
		t.Fatal("readNpmLatestCache within ttl returned ok=false")
	}
	if got.Latest != info.Latest || !got.Published.Equal(info.Published) {
		t.Fatalf("cached info = %+v, want %+v", got, info)
	}
	if _, ok := readNpmLatestCache("@scope/pkg", time.Hour, now.Add(2*time.Hour)); ok {
		t.Fatal("expired cache returned ok=true")
	}
}

func TestAgentboxShowFormattingHelpers(t *testing.T) {
	t.Parallel()
	if got := displayImageRef("docker.io/yoloctl/agentbox:ubuntu-24.04"); got != "yoloctl/agentbox:ubuntu-24.04" {
		t.Errorf("displayImageRef catalog = %q", got)
	}
	if got := displayImageRef("docker.io/library/ubuntu:24.04"); got != "docker.io/library/ubuntu:24.04" {
		t.Errorf("displayImageRef external = %q", got)
	}
	created := "2026-05-27T12:16:35.076970627Z"
	now := time.Date(2026, 5, 27, 12, 21, 42, 0, time.UTC)
	want := "2026-05-27 12:16:35 UTC (5m ago)"
	if got := formatCreatedForDisplay(created, now); got != want {
		t.Errorf("formatCreatedForDisplay = %q, want %q", got, want)
	}
}

func TestPolicyDisplayFromConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  *agentbox.Config
		want string
	}{
		{name: "features_writable", cfg: &agentbox.Config{Features: []string{"claude", "github"}, Capabilities: []string{capabilities.WorkdirWriteCapability}}, want: "features: claude,github"},
		{name: "features_read_only", cfg: &agentbox.Config{Features: []string{"github", "go"}}, want: "features: github,go + read-only"},
		{name: "extra_capability", cfg: &agentbox.Config{Features: []string{"claude"}, Capabilities: []string{capabilities.WorkdirWriteCapability, "network.internal"}}, want: "features: claude + capabilities: network.internal"},
		{name: "capability_read_only", cfg: &agentbox.Config{Capabilities: []string{"network.github"}}, want: "capabilities: network.github + read-only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := policyDisplayFromConfig(c.cfg); got != c.want {
				t.Errorf("policyDisplayFromConfig(%+v) = %q, want %q", c.cfg, got, c.want)
			}
		})
	}
}
