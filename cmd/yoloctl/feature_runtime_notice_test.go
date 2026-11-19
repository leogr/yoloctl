package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func goFeatureLock(ref, metadata string) ocifeature.Lock {
	return ocifeature.Lock{Ref: ref, Digest: "sha256:" + strings.Repeat("a", 64), Layer: ocifeature.Descriptor{Digest: "sha256:" + strings.Repeat("b", 64), MediaType: "application/vnd.devcontainers.layer.v1+tar", Size: 123}, Metadata: metadata}
}

func captureNotices(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := logging.SetLogger(prettylog.New(&buf, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal}))
	t.Cleanup(restore)
	return &buf
}

func TestManifestWriteNoticeListsIgnoredRuntimeRequests(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ref := "ghcr.io/devcontainers/features/go:1"
	cfg := &agentbox.Config{Name: "dev", Space: "default", Agent: "none", Account: "none", Image: "ubuntu:24.04", Features: []string{ref},
		FeatureLocks: []ocifeature.Lock{goFeatureLock(ref, `{"id":"go","version":"1","init":true,"capAdd":["SYS_PTRACE"],"securityOpt":["seccomp=unconfined"]}`)}}
	notices := captureNotices(t)
	if err := requireResolvableManifest(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	got := notices.String()
	for _, want := range []string{"feature " + ref + ": ignored runtime requests (yoloctl controls the runtime; debuggers may not work):", `capAdd ["SYS_PTRACE"]`, "init", `securityOpt ["seccomp=unconfined"]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice lacks %q:\n%s", want, got)
		}
	}
	// Two declarations in one process (as space import does) each log.
	notices.Reset()
	other := *cfg
	other.Name = "dev2"
	for _, c := range []*agentbox.Config{cfg, &other} {
		if err := requireResolvableManifest(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(notices.String(), "ignored runtime requests"); n != 2 {
		t.Fatalf("%d notices for two declarations:\n%s", n, notices.String())
	}
	// A refused write (unknown property) logs nothing.
	notices.Reset()
	cfg.FeatureLocks = []ocifeature.Lock{goFeatureLock(ref, `{"id":"go","version":"1","init":true,"hostAuthority":true}`)}
	if err := requireResolvableManifest(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), `unsupported Feature property "hostAuthority"`) {
		t.Fatalf("unknown property accepted: %v", err)
	}
	if notices.Len() != 0 {
		t.Fatalf("notice logged for a refused write: %q", notices.String())
	}
}

func TestRuntimeNoticeEscapesAndKeepsLongLists(t *testing.T) {
	notices := captureNotices(t)
	requests := []ocifeature.RuntimeRequest{{Key: "entrypoint", Value: "\"evil\u009b2J\x7f\""}}
	for i := 0; i < 10; i++ {
		requests = append(requests, ocifeature.RuntimeRequest{Key: fmt.Sprintf("mounts%d", i), Value: strings.Repeat("v", 190)})
	}
	requests = append(requests, ocifeature.RuntimeRequest{Key: "postStartCommand", Value: `"last"`})
	noticeIgnoredRuntime([]ocifeature.IgnoredFeature{{Ref: "registry.example/features/tool:1", Requests: requests}})
	got := notices.String()
	if strings.ContainsAny(got, "\u009b\x7f") {
		t.Fatalf("control characters reached the terminal: %q", got)
	}
	if !strings.Contains(got, `postStartCommand "last"`) {
		t.Fatalf("final request lost in a list over 1 KiB:\n%s", got)
	}
}
