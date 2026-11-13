package lima

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/vmapi"
)

func TestNestedInstanceWriteTransitionsPreserveOtherKeys(t *testing.T) {
	c, path, _ := resourceClient(t, "Stopped", "qemu")
	raw := []byte("# retained comment\ncpus: 4\nmemory: \"4GiB\"\nforeign: keep\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []bool{true, false, true, false} {
		if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 4, Memory: "4GiB", NestedVirtualization: &value}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		configured, err := nestedVirtualizationInYAML(got)
		if err != nil || configured == nil || *configured != value {
			t.Fatalf("value %v: %s %v", value, got, err)
		}
		if !bytes.Contains(got, []byte("# retained comment")) || !bytes.Contains(got, []byte("foreign: keep")) {
			t.Fatalf("lost unrelated bytes: %s", got)
		}
	}
}

func TestNestedInstanceWriteRetainsStoppedAndNoFollowGuards(t *testing.T) {
	for _, status := range []string{"Running", "Broken", "Unknown"} {
		t.Run(status, func(t *testing.T) {
			c, path, _ := resourceClient(t, status, "qemu")
			before, _ := os.ReadFile(path)
			yes := true
			if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 4, Memory: "4GiB", NestedVirtualization: &yes}); err == nil {
				t.Fatal("accepted unsafe state")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("file changed")
			}
		})
	}
	c, path, _ := resourceClient(t, "Stopped", "qemu")
	target := path + ".retained"
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 4, Memory: "4GiB", NestedVirtualization: &yes}); err == nil {
		t.Fatal("followed symlink")
	}
}

func TestNestedConfigRejectsAmbiguousValues(t *testing.T) {
	for _, raw := range []string{"nestedVirtualization: true\nnestedVirtualization: false\n", "nestedVirtualization: 'true'\n", "nestedVirtualization: [true]\n", "nestedVirtualization: true\n---\nx: y\n"} {
		if _, err := nestedVirtualizationInYAML([]byte(raw)); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	yes := true
	if _, err := setResourcesInYAML([]byte("cpus: 4\nmemory: 4GiB\nnestedVirtualization: false\nnestedVirtualization: true\n"), vmapi.Resources{CPUs: 4, Memory: "4GiB", NestedVirtualization: &yes}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate write: %v", err)
	}
}
