package main

import (
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// vmTerminalCacheDir is the session holders' compiled terminal core: on the
// VM's own system disk under /var/lib/yoloctl, which box grants refuse; root
// only. The paths live here, not in internal/layout, which the OpenShell helper
// links: a helper source change asks every confined box for a recreate.
const vmTerminalCacheDir = "/var/lib/yoloctl/terminal-core"

// terminalCacheDir is the host's compiled terminal core: native code the host
// TUI runs, so it lives under the config root, which host grants refuse.
func terminalCacheDir() (string, error) {
	root, err := layout.ConfigRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "terminal-core"), nil
}

// hostTerminalCacheDir is where a host process keeps its compiled terminal core.
// It is "" in a VM context, in a confined box and for an operator client: those
// compile in memory. A privileged box without the operator grant shows none of
// these signals and uses its own config root; VM holders use session-hold's folder.
func hostTerminalCacheDir() string {
	if spaceenv.Native() || os.Getenv(spaceenv.ContextEnv) != "" {
		return ""
	}
	if _, present := os.LookupEnv("OPENSHELL_SANDBOX"); present {
		return ""
	}
	if op, err := loadOperatorClient(); err != nil || op != nil {
		return ""
	}
	dir, err := terminalCacheDir()
	if err != nil {
		return ""
	}
	return dir
}
