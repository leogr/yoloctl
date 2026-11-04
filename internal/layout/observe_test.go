package layout

import (
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestAgentObserveDirIsAControllerLeafKeyedByValidName(t *testing.T) {
	got, err := AgentObserveDir("dev")
	if err != nil || got != filepath.Join(spaceenv.RunRoot, "agent-observe", "dev") {
		t.Fatalf("AgentObserveDir = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a/b", "sessions", "controller"} {
		if _, err := AgentObserveDir(bad); err == nil {
			t.Errorf("AgentObserveDir(%q) accepted", bad)
		}
	}
}
