package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

func auditCompletionHost(t *testing.T) {
	t.Helper()
	t.Setenv("OPENSHELL_SANDBOX", "")
	if err := os.Unsetenv("OPENSHELL_SANDBOX"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(spaceenv.ContextEnv, "")
	stubOperatorClient(t, nil)
	oldSignal, oldTarget, oldFetch := auditContainerSignal, controllerTargetForCommand, fetchAuditRuleTools
	t.Cleanup(func() {
		auditContainerSignal, controllerTargetForCommand, fetchAuditRuleTools = oldSignal, oldTarget, oldFetch
	})
	auditContainerSignal = func() string { return "" }
	fetchAuditRuleTools = func(context.Context, string, string) ([]byte, error) {
		t.Fatal("completion tried to fetch or install audit tools")
		return nil, errors.New("unexpected tool fetch")
	}
}

func TestAuditRulesRemoveCompletionUsesStoredLiteralNames(t *testing.T) {
	auditCompletionHost(t)
	reads := 0
	controllerTargetForCommand = func(_ context.Context, cmd *cobra.Command) (controllerTarget, error) {
		space, _ := cmd.Flags().GetString("space")
		if space != "selected" {
			t.Fatal("completion lost explicit space", space)
		}
		return controllerTarget{Space: space, Call: func(_ context.Context, req controller.Request, streams controller.ClientIO) error {
			reads++
			if req.Method != "audit.rules.list" || req.Space != space {
				t.Fatal("completion used a different read", req)
			}
			return json.NewEncoder(streams.Out).Encode(auditRulesResult{Rules: audit.RuleSet{Files: []audit.RuleFile{
				{Name: "builtin.yaml", Builtin: true}, {Name: "alpha.yaml"}, {Name: "alpine.yaml"},
				{Name: "alpha.yaml"}, {Name: "beta rules.yaml"}, {Name: "bad\tannotation"},
				{Name: "bad\nrow"}, {Name: "bad\x1b[31m"}, {Name: ""},
			}}})
		}}, nil
	}
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"", []string{"alpha.yaml", "alpine.yaml", "beta rules.yaml", ":4"}},
		{"al", []string{"alpha.yaml", "alpine.yaml", ":4"}},
		{"missing", []string{":4"}},
	} {
		got := hostCompletion(t, "audit", "rules", "remove", "--space", "selected", tc.prefix)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("prefix %q: got %q, want %q", tc.prefix, got, tc.want)
		}
	}
	before := reads
	got := hostCompletion(t, "audit", "rules", "remove", "--space", "selected", "alpha.yaml", "")
	if !reflect.DeepEqual(got, []string{":4"}) || reads != before {
		t.Fatal("extra positional triggered a controller read or file completion", got, reads)
	}
}

func TestAuditRulesCompletionUnavailableHasNoFileFallback(t *testing.T) {
	auditCompletionHost(t)
	for _, stage := range []string{"target", "read"} {
		t.Run(stage, func(t *testing.T) {
			reads := 0
			controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
				if stage == "target" {
					return controllerTarget{}, errors.New("controller unavailable")
				}
				return controllerTarget{Space: "selected", Call: func(context.Context, controller.Request, controller.ClientIO) error {
					reads++
					return errors.New("rules unavailable")
				}}, nil
			}
			got := hostCompletion(t, "audit", "rules", "remove", "")
			if !reflect.DeepEqual(got, []string{":4"}) || (stage == "target" && reads != 0) || (stage == "read" && reads != 1) {
				t.Fatal(got, reads)
			}
		})
	}
	stubOperatorClient(t, &controller.OperatorClient{Space: "operator"})
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
		t.Fatal("operator completion reached host controller selection")
		return controllerTarget{}, nil
	}
	cmd := newAuditRulesCmd()
	remove, _, err := cmd.Find([]string{"remove"})
	if err != nil {
		t.Fatal(err)
	}
	remove.SetContext(t.Context())
	if names, directive := remove.ValidArgsFunction(remove, nil, ""); len(names) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatal(names, directive)
	}
}

func TestAuditRulesImportCompletesOnlyFirstHostFile(t *testing.T) {
	auditCompletionHost(t)
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
		t.Fatal("file completion queried the controller")
		return controllerTarget{}, nil
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.yaml")
	if err := os.WriteFile(path, []byte("# local fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := hostCompletion(t, "audit", "rules", "import", filepath.Join(dir, "val"))
	if !reflect.DeepEqual(got, []string{path, ":4"}) {
		t.Fatal(got)
	}
	got = hostCompletion(t, "audit", "rules", "import", path, "")
	if !reflect.DeepEqual(got, []string{":4"}) {
		t.Fatal("extra import positional offered files", got)
	}
}
