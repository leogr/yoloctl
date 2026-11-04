package vm

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/vmapi"
)

func TestInstancePathValidationUsesSelectedAdapter(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 100)))
	// A non-Lima adapter need not implement the optional preflight. In
	// particular Firecracker's sockets are in its private temporary directory.
	registry.Lock()
	old := registry.factories
	registry.factories = map[string]func() vmapi.Driver{
		"lima":        old["lima"],
		"firecracker": func() vmapi.Driver { return lookupDriver{kind: "firecracker"} },
	}
	registry.Unlock()
	t.Cleanup(func() { registry.Lock(); registry.factories = old; registry.Unlock() })
	for _, backend := range []string{"lima", ""} {
		if err := ValidateInstancePaths(backend, "a"); err == nil || !strings.Contains(err.Error(), "Lima socket path") {
			t.Fatalf("backend %q: %v", backend, err)
		}
	}
	if err := ValidateInstancePaths("firecracker", "a"); err != nil {
		t.Fatal("Lima limit applied to Firecracker", err)
	}
	if err := ValidateInstancePaths("missing", "a"); err == nil {
		t.Fatal("unavailable backend accepted")
	}
}
