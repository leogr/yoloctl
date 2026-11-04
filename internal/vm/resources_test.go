package vm

import (
	"context"
	"fmt"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

type resourceDriver struct {
	vmapi.Driver
	kind string
	got  *[]string
}

func (d resourceDriver) Kind() string { return d.kind }
func (d resourceDriver) SetResources(_ context.Context, name string, r vmapi.Resources) error {
	*d.got = append(*d.got, fmt.Sprintf("%s %s %d %s", d.kind, name, r.CPUs, r.Memory))
	return nil
}

func TestSetResourcesUsesThePersistedBackend(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var got []string
	registry.Lock()
	old := registry.factories
	registry.factories = map[string]func() vmapi.Driver{
		"lima":        func() vmapi.Driver { return resourceDriver{kind: "lima", got: &got} },
		"firecracker": func() vmapi.Driver { return resourceDriver{kind: "firecracker", got: &got} },
	}
	registry.Unlock()
	t.Cleanup(func() { registry.Lock(); registry.factories = old; registry.Unlock() })
	c, err := spaceconfig.New("trial", t.TempDir(), "account")
	if err != nil {
		t.Fatal(err)
	}
	c.AccountDiskInitializing = true
	b, _, err := spaceconfig.Split(c, 1000, 1000, "generation")
	if err != nil {
		t.Fatal(err)
	}
	b.VM.Backend, b.VM.Image = "firecracker", "/images/firecracker"
	if err = b.Save(); err != nil {
		t.Fatal(err)
	}
	if err = New().SetResources(t.Context(), "trial", Resources{CPUs: 6, Memory: "6GiB"}); err != nil {
		t.Fatal(err)
	}
	// Without a bootstrap the space is legacy Lima; nothing else is inferred.
	if err = New().SetResources(t.Context(), "legacy", Resources{CPUs: 2, Memory: "2GiB"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "firecracker trial 6 6GiB" || got[1] != "lima legacy 2 2GiB" {
		t.Fatalf("persisted backend not selected: %q", got)
	}
}
