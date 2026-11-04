package vm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmapi"
)

func TestNewSpaceDefaultDoesNotChangeLegacyAdapter(t *testing.T) {
	want := "lima"
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		want = "firecracker"
	}
	if got := DefaultBackend(); got != want {
		t.Fatalf("new space backend %q, want %q", got, want)
	}
	d, err := Driver("")
	if err != nil || d.Kind() != "lima" {
		t.Fatalf("legacy backend changed: %v %v", d, err)
	}
}

func TestLegacyAdapterAndUnknownBackend(t *testing.T) {
	d, e := Driver("")
	if e != nil || d.Kind() != "lima" {
		t.Fatalf("legacy default: %v %v", d, e)
	}
	if _, e = Driver("unlinked"); e == nil {
		t.Fatal("unknown backend accepted")
	}
}
func TestMissingLimaDoesNotHideExistingSpace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LIMA_HOME", filepath.Join(t.TempDir(), "lima"))
	c, e := spaceconfig.New("legacy", t.TempDir(), "trial")
	if e != nil {
		t.Fatal(e)
	}
	c.AccountDiskInitializing = true
	b, _, e := spaceconfig.Split(c, 1000, 1000, "generation")
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Save(); e != nil {
		t.Fatal(e)
	}
	if _, e = New().List(context.Background()); e == nil || !strings.Contains(e.Error(), "lima") {
		t.Fatalf("hidden unavailable legacy runtime: %v", e)
	}
}
func TestReadDoesNotCreateHostState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", root)
	d, e := ForSpace("absent")
	if e != nil || d.Kind() != "lima" {
		t.Fatalf("selection: %v", e)
	}
	if _, e = os.Stat(root); !os.IsNotExist(e) {
		t.Fatalf("read initialized state: %v", e)
	}
}

type lookupDriver struct {
	vmapi.Driver
	kind, dir string
	calls     *int
}

func (d lookupDriver) Kind() string { return d.kind }
func (d lookupDriver) InstanceDir(name string) (string, error) {
	return filepath.Join(d.dir, name), nil
}
func (d lookupDriver) List(context.Context) ([]vmapi.Instance, error) {
	*d.calls++
	return []vmapi.Instance{{Name: "trial", Status: "Running"}}, nil
}
func TestLookupIgnoresUnrelatedBackendButRejectsOrphanMetadata(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	selected, foreign := 0, 0
	root := t.TempDir()
	registry.Lock()
	old := registry.factories
	registry.factories = map[string]func() vmapi.Driver{
		"lima": func() vmapi.Driver {
			return lookupDriver{kind: "lima", dir: filepath.Join(root, "lima"), calls: &selected}
		},
		"other": func() vmapi.Driver {
			return lookupDriver{kind: "other", dir: filepath.Join(root, "other"), calls: &foreign}
		},
	}
	registry.Unlock()
	t.Cleanup(func() { registry.Lock(); registry.factories = old; registry.Unlock() })
	if _, found, err := New().Lookup(t.Context(), "trial"); err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	if selected != 1 || foreign != 0 {
		t.Fatalf("backend calls %d %d", selected, foreign)
	}
	if _, found, err := New().LookupBackend(t.Context(), "trial", "other"); err != nil || !found {
		t.Fatalf("explicit backend lookup: %v %v", found, err)
	}
	if selected != 1 || foreign != 1 {
		t.Fatalf("explicit backend called default adapter: %d %d", selected, foreign)
	}
	if err := os.MkdirAll(filepath.Join(root, "other", "trial"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New().Lookup(t.Context(), "trial"); err == nil {
		t.Fatal("foreign orphan ignored")
	}
	if foreign != 1 {
		t.Fatal("lookup ran foreign hypervisor")
	}
}

func TestNativeLookupUsesGuestInventory(t *testing.T) {
	// Configure is deliberately process-global and immutable. Run the
	// controller-side lookup in a child process.
	if os.Getenv("YOLOCTL_TEST_NATIVE_LOOKUP") != "1" {
		t.Setenv("YOLOCTL_TEST_NATIVE_LOOKUP", "1")
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeLookupUsesGuestInventory$")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native lookup: %v\n%s", err, out)
		}
		return
	}
	// Like a Firecracker guest: no limactl, and config/space.yaml holds the
	// native settings rather than a host bootstrap.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := spaceconfig.New("work", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	c.AccountDiskUUID = "uuid"
	b, _, err := spaceconfig.Split(c, 1000, 1000, "generation")
	if err != nil {
		t.Fatal(err)
	}
	b.Bundle, b.VM.Backend = "bundle", "firecracker"
	native := b.Context("/home/developer/.config/yoloctl")
	native.DiskMount = t.TempDir()
	native.ConfigDir = filepath.Join(native.DiskMount, "config")
	if err := os.Mkdir(native.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := spaceenv.Configure(native); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	inst, found, err := New().Lookup(t.Context(), "work")
	if err != nil || !found || inst.Name != "work" || inst.Status != "Running" {
		t.Fatalf("guest lookup: %+v %v %v", inst, found, err)
	}
	if _, _, err := New().Lookup(t.Context(), "other"); err == nil {
		t.Fatal("guest looked up another space")
	}
}
