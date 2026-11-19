package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func usingSpaceLines(t *testing.T, wire *bytes.Buffer) []string {
	t.Helper()
	var out []string
	for _, r := range readCLIRecords(t, bytes.NewBuffer(append([]byte(nil), wire.Bytes()...))) {
		if r.Message == "using space:" {
			out = append(out, prettylog.StructureOf(r).Subject)
		}
	}
	return out
}

// One outer command names a space once, including through a nested Execute
// that shares its context; the next outer command in the same process names
// it again.
func TestUsingSpaceOncePerOuterCommand(t *testing.T) {
	wire, _ := captureCLIRecords(t)
	run := func() {
		root := newClientRootCmd()
		root.AddCommand(&cobra.Command{Use: "probe-space", Hidden: true, RunE: func(cmd *cobra.Command, _ []string) error {
			cliUsingSpace("falco")
			cliUsingSpace("falco")
			nested := newClientRootCmd()
			nested.AddCommand(&cobra.Command{Use: "probe-nested", RunE: func(*cobra.Command, []string) error {
				cliUsingSpace("falco")
				cliUsingSpace("other")
				return nil
			}})
			nested.SetArgs([]string{"probe-nested"})
			return nested.ExecuteContext(cmd.Context())
		}})
		root.SetArgs([]string{"probe-space"})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if got := strings.Join(usingSpaceLines(t, wire), ","); got != "falco,other" {
		t.Fatalf("first command named %q", got)
	}
	run()
	if got := strings.Join(usingSpaceLines(t, wire), ","); got != "falco,other,falco,other" {
		t.Fatalf("second command named %q", got)
	}
}

// Shell completion never prints the space line.
func TestCompletionPrintsNoSpaceLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wire, _ := captureCLIRecords(t)
	oldTarget, oldOperator := controllerTargetForCommand, loadOperatorClient
	t.Cleanup(func() { controllerTargetForCommand, loadOperatorClient = oldTarget, oldOperator })
	loadOperatorClient = func() (*controller.OperatorClient, error) { return nil, nil }
	controllerTargetForCommand = func(context.Context, *cobra.Command) (controllerTarget, error) {
		return controllerTarget{Space: "falco", Call: func(context.Context, controller.Request, controller.ClientIO) error { return nil }}, nil
	}
	root := newClientRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"__complete", "box", "start", ""})
	_ = root.ExecuteContext(context.Background())
	if got := usingSpaceLines(t, wire); len(got) != 0 {
		t.Fatalf("completion named %v", got)
	}
}

// The unit reader recognizes both label generations and leaves a raw line
// with an empty place literal.
func TestOwnedRecordFrameBothLabelForms(t *testing.T) {
	for line, want := range map[string]bool{
		"warning: space>controller: Store degraded": true,
		"warning: controller: Store degraded":       true,
		"host>yoloctl: using space: falco":          true,
		"warning: >external: task":                  false,
		"warning: space>vm: wrong place":            false,
		"external: task":                            false,
	} {
		if got := ownedRecordFrame(line); got != want {
			t.Errorf("%q owned=%v", line, got)
		}
	}
}
