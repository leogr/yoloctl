package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/ocifeature"
)

func featureEnvConfig() *agentbox.Config {
	ref := "ghcr.io/devcontainers/features/rust:1"
	meta := `{"id":"rust","version":"1","containerEnv":{"CARGO_HOME":"/usr/local/cargo","RUSTUP_HOME":"/usr/local/rustup","ODD":"a\u009b2Jb","LD_LIBRARY_PATH":"/opt/lib","ETC_HOME":"/etc","PATH":"/usr/local/cargo/bin:${PATH}"}}`
	return &agentbox.Config{Name: "dev", Space: "default", Agent: "none", Account: "none", Image: "ubuntu:24.04", Features: []string{ref},
		FeatureLocks: []ocifeature.Lock{goFeatureLock(ref, meta)}, Env: map[string]agentbox.EnvVar{"CARGO_HOME": {Literal: "/workdir/.cargo"}}}
}

func TestFeatureEnvLinesForShowAndInspect(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := featureEnvConfig()
	var lines []string
	for _, v := range featureEnvViews(cfg) {
		lines = append(lines, v.line())
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"CARGO_HOME=/usr/local/cargo (from ghcr.io/devcontainers/features/rust:1; overridden by box env)",
		"RUSTUP_HOME=/usr/local/rustup (from ghcr.io/devcontainers/features/rust:1)",
		`ODD=a\u009b2Jb`,
		"LD_LIBRARY_PATH not set (reserved;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lines lack %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\u009b') || strings.Contains(got, "/workdir/.cargo") {
		t.Fatalf("unescaped control or a box literal value shown:\n%s", got)
	}
	// box inspect: both the human and the JSON form carry the declared list.
	m := inspectManifest{DeclaredFeatureEnv: featureEnvViews(cfg)}
	var human bytes.Buffer
	writeInspectManifest(&human, &m)
	if !strings.Contains(human.String(), "Feature env:  RUSTUP_HOME=/usr/local/rustup (from ghcr.io/devcontainers/features/rust:1) (declared)") {
		t.Fatalf("inspect human view:\n%s", human.String())
	}
}

func TestFeatureRuntimeNoticeListsAdjustments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	notices := captureNotices(t)
	if err := requireResolvableManifest(t.Context(), featureEnvConfig()); err != nil {
		t.Fatal(err)
	}
	got := notices.String()
	for _, want := range []string{"runtime adjustments:", "folder not granted (protected): ETC_HOME=/etc", "not set at runtime (reserved): LD_LIBRARY_PATH"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice lacks %q:\n%s", want, got)
		}
	}
}

func TestBoxShowListsDeclaredFeatureEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	meta := agentboxShowMetadata{FeatureEnv: featureEnvViews(featureEnvConfig())}
	var text bytes.Buffer
	d := &container.Details{Name: "dev", Labels: map[string]string{}}
	if err := writeAgentboxShow(&text, d, "default", landlockState{}, agentStateMount{}, meta, sessionStatus{State: "running"}, nil); err != nil {
		t.Fatal(err)
	}
	got := text.String()
	if !strings.Contains(got, "Feature env (declared; new box processes get it, recreate applies it):") || !strings.Contains(got, "  RUSTUP_HOME=/usr/local/rustup (from ghcr.io/devcontainers/features/rust:1)") {
		t.Fatalf("box show view:\n%s", got)
	}
}
