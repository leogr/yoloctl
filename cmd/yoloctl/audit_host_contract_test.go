package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Force the host registration path even when the repository gate runs inside
// an OpenShell box. Environment-based hiding must not mask host CLI contracts.
func TestAuditHostSurfacePreservesCLIContracts(t *testing.T) {
	t.Logf("audit host surface before forcing registration: %t", auditHostAvailable())
	t.Setenv("OPENSHELL_SANDBOX", "")
	if err := os.Unsetenv("OPENSHELL_SANDBOX"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(spaceenv.ContextEnv, "")
	previous := auditContainerSignal
	auditContainerSignal = func() string { return "" }
	t.Cleanup(func() { auditContainerSignal = previous })
	t.Run("entity-completers", TestEveryEntityFlagHasACompleter)
	t.Run("positional-completers", TestEveryPositionalCommandHasACompleter)
	t.Run("name-validation-before-selection", TestBoxNameRefusedBeforeControllerOrPersistence)
}

func TestAuditHostChecksOperatorAtExecution(t *testing.T) {
	t.Setenv("OPENSHELL_SANDBOX", "")
	if err := os.Unsetenv("OPENSHELL_SANDBOX"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(spaceenv.ContextEnv, "")
	old := auditContainerSignal
	auditContainerSignal = func() string { return "" }
	t.Cleanup(func() { auditContainerSignal = old })
	stubOperatorClient(t, &controller.OperatorClient{Space: "test"})
	if err := requireAuditHost(); err == nil {
		t.Fatal("operator passed execution guard")
	}
}

func TestAuditBoxFilterRefusedBeforeSelection(t *testing.T) {
	previous := loadOperatorClient
	loadOperatorClient = func() (*controller.OperatorClient, error) {
		t.Fatal("invalid filter reached controller selection")
		return nil, nil
	}
	t.Cleanup(func() { loadOperatorClient = previous })
	for _, action := range []string{"logs", "export"} {
		cmd := newAuditCmd()
		cmd.SetArgs([]string{action, "--box", "../escape"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "agentbox") {
			t.Fatal(action, err)
		}
	}
}
