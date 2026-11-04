package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoAgentNameBranchesOutsideAuthShims enforces the agent-adapter boundary:
//
//	Go code must not branch on the agent name in install, launch, update,
//	feature, capability, image, or non-auth lifecycle paths. Agent-name
//	branching is allowed ONLY in isolated, named auth shims.
//
// It scans the source for `== "claude"` / `== "codex"` / `== "gemini"` control-flow branches
// outside the permitted auth file. A new occurrence elsewhere is a
// regression toward the old hardcoded-per-agent model and must instead be
// expressed as profile/feature/capability data.
func TestNoAgentNameBranchesOutsideAuthShims(t *testing.T) {
	// The only file allowed to branch on agent name — the named auth shim for
	// provisional OAuth/subscription + the claude companion-config flow.
	allowedSuffixes := []string{
		"cmd/yoloctl/agentauth.go",
	}
	// Equality-operator patterns only: catches `x == "claude"` style control
	// branches while ignoring data tables (RequiredAgent: "claude"), map keys
	// ("claude-default"), help text, and comments.
	banned := []string{`== "claude"`, `== "codex"`, `!= "claude"`, `!= "codex"`, `== "gemini"`, `!= "gemini"`, `== "agy"`, `!= "agy"`, `== "opencode"`, `!= "opencode"`, `== "pi"`, `!= "pi"`}

	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			slash := filepath.ToSlash(path)
			for _, suf := range allowedSuffixes {
				if strings.HasSuffix(slash, suf) {
					return nil
				}
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(b)
			for _, pat := range banned {
				if strings.Contains(src, pat) {
					t.Errorf("%s contains agent-name branch %q — only the auth shim (agentauth.go) may branch on agent name; express this as profile/feature/capability data instead", slash, pat)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
