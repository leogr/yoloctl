package main

import (
	"os"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestOpenShellForwardPrivateEntryRefusesOrdinaryUserWithoutNativeSetup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("ordinary-user entry denial requires a non-root test process")
	}
	before := spaceenv.Current()
	if err := runOpenShellForwardInternal(t.Context(), []string{"--box", "fixture"}); err == nil {
		t.Fatal("ordinary user entered native forward infrastructure")
	}
	if spaceenv.Current() != before {
		t.Fatal("refused private entry changed native context")
	}
}
