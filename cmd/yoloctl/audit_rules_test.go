package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

func TestAuditRulesHostStreamAndEditor(t *testing.T) {
	t.Setenv("OPENSHELL_SANDBOX", "")
	if err := os.Unsetenv("OPENSHELL_SANDBOX"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(spaceenv.ContextEnv, "")
	stubOperatorClient(t, nil)
	oldSignal, oldTarget, oldFetch, oldEditor := auditContainerSignal, controllerTargetForCommand, fetchAuditRuleTools, auditRulesEditor
	t.Cleanup(func() {
		auditContainerSignal, controllerTargetForCommand, fetchAuditRuleTools, auditRulesEditor = oldSignal, oldTarget, oldFetch, oldEditor
	})
	auditContainerSignal = func() string { return "" }
	snapshot := auditRulesResult{Architecture: "arm64", Revision: strings.Repeat("a", 64), Rules: audit.RuleSet{Selection: []byte("rules: []\n#" + strings.Repeat("large selection ", 300000)), Files: []audit.RuleFile{{Name: "rules.yaml", SHA256: strings.Repeat("b", 64), Builtin: true}}}}
	tools := []byte("pinned archive")
	downloads := 0
	fetchAuditRuleTools = func(context.Context, string, string) ([]byte, error) { downloads++; return tools, nil }
	changed := 0
	wantKind, wantBody := "", ""
	stale := false
	target := controllerTarget{Space: "test"}
	target.Call = func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
		if req.Method == "audit.rules.list" {
			return json.NewEncoder(streams.Out).Encode(snapshot)
		}
		changed++
		if req.Method != "audit.rules."+wantKind {
			t.Fatal(req.Method)
		}
		var input auditRequest
		if err := json.Unmarshal(req.Payload, &input); err != nil {
			t.Fatal(err)
		}
		if input.Rules == nil || input.Rules.Expected != snapshot.Revision {
			t.Fatal("editor did not retain snapshot revision", input)
		}
		body, err := io.ReadAll(streams.In)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(tools)+wantBody {
			t.Fatal("stdin bytes changed or selection capped")
		}
		response := snapshot
		response.Changed = true
		response.Validation = audit.Validation{Diagnostics: "native warning\n\x1b]52;c;secret\a", Warnings: 2}
		if err := json.NewEncoder(streams.Out).Encode(response); err != nil {
			t.Fatal(err)
		}
		if stale {
			return audit.ErrRuleGenerationChanged
		}
		return nil
	}
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) { return target, nil }
	execute := func(args ...string) (string, string, error) {
		cmd := newAuditCmd()
		cmd.SetArgs(args)
		var out, diag bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&diag)
		err := cmd.ExecuteContext(t.Context())
		return out.String(), diag.String(), err
	}
	out, _, err := execute("rules", "list", "--json")
	if err != nil || len(out) < len(snapshot.Rules.Selection) || downloads != 0 {
		t.Fatal("list truncated data or installed tools", len(out), err)
	}
	source := filepath.Join(t.TempDir(), "same.yaml")
	wantKind, wantBody = "import", "opaque\n"
	if err := os.WriteFile(source, []byte(wantBody), 0600); err != nil {
		t.Fatal(err)
	}
	_, diag, err := execute("rules", "import", source)
	if err != nil || changed != 1 || downloads != 1 || strings.ContainsAny(diag, "\x1b\a") || !strings.Contains(diag, "native warning") {
		t.Fatal("import/control escaping", changed, diag, err)
	}
	wantKind, wantBody = "remove", ""
	if _, _, err := execute("rules", "remove", "same.yaml", "--sha256", "01234567"); err != nil {
		t.Fatal(err)
	}
	wantKind, wantBody = "edit", "rules:\n- disable: {rule: '*'}\n"
	editedPath := ""
	auditRulesEditor = func(_ context.Context, path string) error {
		if changed != 2 {
			t.Fatal("mutation preceded editor")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("nonprivate editor file", err)
		}
		editedPath = path
		t.Cleanup(func() { _ = os.Remove(path) })
		return os.WriteFile(path, []byte(wantBody), 0600)
	}
	stale = true
	_, _, err = execute("rules", "edit")
	if !errors.Is(err, audit.ErrRuleGenerationChanged) || !strings.Contains(err.Error(), editedPath) {
		t.Fatal("lost stale edit", err)
	}
	retained, err := os.ReadFile(editedPath)
	if err != nil || string(retained) != wantBody {
		t.Fatal("changed text not retained", err)
	}
	// No-op editor does not download tools or submit a candidate.
	auditRulesEditor = func(context.Context, string) error { return nil }
	before := downloads
	out, _, err = execute("rules", "edit")
	if err != nil || out != "no changes\n" || downloads != before || changed != 3 {
		t.Fatal("no-op editor changed native state", out, err)
	}
}
