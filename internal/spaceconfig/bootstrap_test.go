package spaceconfig

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"gopkg.in/yaml.v3"
)

func TestSplitBootstrapKeepsDeclarationsOnNativeDisk(t *testing.T) {
	legacy, err := New("work", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	legacy.AccountDiskUUID = "filesystem-identity"
	b, s, err := Split(legacy, 1000, 20, "generation")
	if err != nil {
		t.Fatal(err)
	}
	boot, err := yaml.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"default_account", "default_workdir", "secrets", "personal"} {
		if strings.Contains(string(boot), forbidden) {
			t.Fatalf("bootstrap leaks controller data: %s", forbidden)
		}
	}
	parsed, err := ParseBootstrap(boot)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := s.Compose(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if composed.DefaultAccount != legacy.DefaultAccount || composed.DefaultWorkdir != legacy.DefaultWorkdir ||
		composed.AccountDiskID != legacy.AccountDiskID || composed.AccountDiskUUID != legacy.AccountDiskUUID {
		t.Fatal("split/compose lost declaration or native disk identity")
	}
	if _, err := ParseBootstrap(append(boot, []byte("default_account: injected\n")...)); err == nil {
		t.Fatal("bootstrap accepted controller-owned fields")
	}
}

func TestBootstrapDistinguishesMigrationFromCorruption(t *testing.T) {
	if _, err := ParseBootstrap([]byte("schema_version: 2\n")); !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("legacy schema: %v", err)
	}
	if _, err := ParseBootstrap([]byte("schema_version: 99\n")); err == nil || errors.Is(err, ErrLegacySchema) {
		t.Fatalf("unknown schema was treated as migration: %v", err)
	}
}

func TestBootstrapCannotInitializeExistingDiskImplicitly(t *testing.T) {
	legacy, err := New("work", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Split(legacy, 1000, 20, "generation"); err == nil {
		t.Fatal("accepted missing disk UUID without explicit initialization")
	}
	legacy.AccountDiskInitializing = true
	if _, _, err := Split(legacy, 1000, 20, "generation"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Split(legacy, 1000, 0, "generation"); err == nil {
		t.Fatal("accepted root group")
	}
}

func TestFirecrackerImageCannotBeAProjectGrant(t *testing.T) {
	legacy, e := New("work", "/projects", "trial")
	if e != nil {
		t.Fatal(e)
	}
	legacy.AccountDiskInitializing = true
	b, _, e := Split(legacy, 1000, 1000, "generation")
	if e != nil {
		t.Fatal(e)
	}
	b.VM.Backend = "firecracker"
	for _, image := range []string{"relative", "/projects", "/projects/image", "/"} {
		b.VM.Image = image
		if e = b.Validate(); e == nil {
			t.Fatalf("accepted unsafe image %q", image)
		}
	}
	b.VM.Image = "/prepared"
	if e = b.Validate(); e != nil {
		t.Fatal(e)
	}
	if b.Context("/config").ServiceManager != "microvm" {
		t.Fatal("guest supervisor was not selected")
	}
}

func TestVMImageUpdateCheckpointValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Bootstrap)
	}{
		{"other backend", func(b *Bootstrap) { b.VM.Backend = "lima"; b.VM.Image = "" }},
		{"initializing", func(b *Bootstrap) { b.Initializing = true }},
		{"no uuid", func(b *Bootstrap) { b.DiskUUID = "" }},
		{"importing", func(b *Bootstrap) { b.Import = &ImportState{} }},
		{"no bundle", func(b *Bootstrap) { b.ImageUpdate.Bundle = "" }},
		{"bad phase", func(b *Bootstrap) { b.ImageUpdate.Phase = "unknown" }},
		{"relative path", func(b *Bootstrap) { b.ImageUpdate.Target = "relative" }},
		{"unclean path", func(b *Bootstrap) { b.ImageUpdate.Target = "/images/../target" }},
		{"wrong selection", func(b *Bootstrap) { b.ImageUpdate.Phase = "selected" }},
		{"target grant", func(b *Bootstrap) { b.Mounts = []Mount{{Source: "/images/new"}} }},
		{"previous grant", func(b *Bootstrap) {
			b.VM.Image = "/images/new"
			b.ImageUpdate.Phase = "selected"
			b.Mounts = []Mount{{Source: "/images/old"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bootstrap{SchemaVersion: 3, Name: "work", DiskID: strings.Repeat("1", 32), DiskUUID: "uuid", UID: 1000, GID: 1000, Generation: "generation", VM: VMSettings{Backend: "firecracker", Image: "/images/old", CPUs: 2, Memory: "2GiB", Disk: "30GiB"}, ImageUpdate: &VMImageUpdate{Previous: "/images/old", Target: "/images/new", Bundle: "bundle", Phase: "prepared"}}
			if err := b.Validate(); err != nil {
				t.Fatal(err)
			}
			tc.change(b)
			if err := b.Validate(); err == nil {
				t.Fatal("accepted unsafe update checkpoint")
			}
		})
	}
}

func TestNativeWorkerCannotReadHostBootstrap(t *testing.T) {
	// Configure is deliberately process-global and immutable. Run the
	// controller-side read in a child process.
	if os.Getenv("YOLOCTL_TEST_NATIVE_BOOTSTRAP") != "1" {
		t.Setenv("YOLOCTL_TEST_NATIVE_BOOTSTRAP", "1")
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeWorkerCannotReadHostBootstrap$")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native bootstrap read: %v\n%s", err, out)
		}
		return
	}
	c, err := New("work", "/projects", "personal")
	if err != nil {
		t.Fatal(err)
	}
	c.AccountDiskUUID = "uuid"
	b, _, err := Split(c, 1000, 1000, "generation")
	if err != nil {
		t.Fatal(err)
	}
	b.Bundle = "bundle"
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
	// Native config/space.yaml shares the bootstrap file name and must never
	// be decoded as one.
	if _, err := LoadBootstrap("work"); err == nil || !strings.Contains(err.Error(), "cannot read host bootstrap") {
		t.Fatalf("native settings read as bootstrap: %v", err)
	}
	if loaded, err := Load("work"); err != nil || loaded.DefaultAccount != "personal" {
		t.Fatalf("native settings: %+v %v", loaded, err)
	}
}
