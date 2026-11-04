package agentbox

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the composed binary grant with OPA's slash-delimited glob.match,
// the builtin used by the shipped Rego binary matcher. This checks path scope,
// not full OpenShell enforcement; the live confined smoke owns that proof.
// OPA is an optional development tool, not a runtime dependency.
func TestCodexEgressGlobScope(t *testing.T) {
	opa, err := exec.LookPath("opa")
	if err != nil {
		t.Skip("opa not installed; run with OPA on PATH for policy evaluation")
	}
	dir := t.TempDir()
	data, err := ComposePolicy(&Config{Name: "codex", Space: "test", Features: []string{"codex", "github"}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	const npm = "/home/sandbox/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/"
	const releases = "/home/sandbox/.codex/packages/app-server-daemon/releases/"
	cases := []struct {
		name, binary string
		allow        bool
	}{
		{"legacy-node", "/usr/bin/node", true},
		{"npm-amd64", npm + "codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex", true},
		{"npm-arm64", npm + "codex-linux-arm64/vendor/aarch64-unknown-linux-musl/bin/codex", true},
		{"daemon-current", releases + "0.166.0-x86_64-unknown-linux-musl/bin/codex", true},
		{"daemon-next", releases + "0.167.0-x86_64-unknown-linux-musl/bin/codex", true},
		{"daemon-arm64", releases + "0.167.0-aarch64-unknown-linux-musl/bin/codex", true},
		{"curl-spoofed-argv", "/usr/bin/curl", false},
		{"shell-spoofed-argv", "/bin/sh", false},
		{"wrong-leaf", releases + "0.166.0-x86_64-unknown-linux-musl/bin/curl", false},
		{"wrong-target", releases + "0.166.0-x86_64-apple-darwin/bin/codex", false},
		{"extra-version-directory", releases + "extra/0.166.0-x86_64-unknown-linux-musl/bin/codex", false},
		{"extra-bin-directory", releases + "0.166.0-x86_64-unknown-linux-musl/bin/extra/codex", false},
		{"wrong-package", strings.Replace(releases, "app-server-daemon", "other", 1) + "0.166.0-x86_64-unknown-linux-musl/bin/codex", false},
		{"wrong-npm-target", npm + "codex-linux-x64/vendor/aarch64-unknown-linux-musl/bin/codex", false},
		{"wrong-npm-leaf", npm + "codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/curl", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// An allowed-looking argv is not an executable identity. The
			// actual detached process has no matching Node ancestor.
			input := map[string]any{
				"exec": map[string]any{
					"path": tc.binary, "ancestors": []string{},
					"cmdline_paths": []string{releases + "0.166.0-x86_64-unknown-linux-musl/bin/codex"},
				},
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			query := `count([b | some b in data.network_policies.openai.binaries; glob.match(b.path, ["/"], input.exec.path)]) > 0`
			cmd := exec.CommandContext(t.Context(), opa, "eval", "--format=raw", "--data", config, "--stdin-input", query)
			cmd.Stdin = bytes.NewReader(encoded)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("OPA evaluation: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != "true" && got != "false" {
				t.Fatalf("invalid policy decision %q", got)
			} else if (got == "true") != tc.allow {
				t.Fatalf("binary match=%s, want %t", got, tc.allow)
			}
		})
	}
}
