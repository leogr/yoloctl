package openshell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNamespaceKernelBootIDStrictKernelLine(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	got, err := decodeKernelBootID([]byte(id + "\n"))
	if err != nil || got != id {
		t.Fatalf("canonical kernel line: %q, %v", got, err)
	}
	for _, raw := range []string{"", id, id + "\r\n", id + "\n\n", " " + id + "\n", id + " \n", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\x00", strings.ToUpper("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee") + "\n", "00000000-0000-0000-0000-000000000000\n", strings.Repeat("x", 38)} {
		if got, err := decodeKernelBootID([]byte(raw)); err == nil || got != "" {
			t.Errorf("accepted malformed kernel line %q: %q, %v", raw, got, err)
		}
	}
}

func TestNamespaceKernelRejectsOrdinaryProcOrNamespaceDescriptors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys/kernel/random"), 0o700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "sys/kernel/random/boot_id")
	if err := os.WriteFile(name, []byte("11111111-2222-4333-8444-555555555555\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	if got, err := readKernelBootID(int(dir.Fd())); err == nil || got != "" {
		t.Fatalf("ordinary UUID file accepted as kernel boot: %q, %v", got, err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	for _, fd := range []int{int(dir.Fd()), int(file.Fd()), -1} {
		for _, kind := range []int{unix.CLONE_NEWNS, unix.CLONE_NEWNET, unix.CLONE_NEWUSER} {
			if got, err := kernelNamespaceIdentity(fd, kind); err == nil || got != (namespaceIdentity{}) {
				t.Fatalf("ordinary/invalid descriptor accepted: %+v, %v", got, err)
			}
		}
	}
	if file, got, err := openKernelNamespace(int(dir.Fd()), "mnt", unix.CLONE_NEWNS); err == nil || file != nil || got != (namespaceIdentity{}) {
		t.Fatalf("ordinary process directory accepted: %v, %+v, %v", file, got, err)
	}
	for _, name := range []string{"../mnt", "pid", "mnt/../net", "/proc/1/ns/mnt", "net\x00"} {
		if file, got, err := openKernelNamespace(int(dir.Fd()), name, unix.CLONE_NEWNS); err == nil || file != nil || got != (namespaceIdentity{}) {
			t.Fatalf("arbitrary namespace selection accepted: %q", name)
		}
	}
}

func namespaceKernelUnavailable(t *testing.T, err error) {
	t.Helper()
	if os.Getenv("YOLOCTL_REQUIRE_NAMESPACE_KERNEL") == "1" {
		t.Fatalf("required kernel descriptor coverage unavailable: %v", err)
	}
	t.Skipf("kernel descriptor coverage unavailable locally: %v", err)
}

// Project tests only read descriptors; they do not create/enter namespaces,
// mount pins, modify policy or claim admitted native execution. The native
// fixture must require these without skips in addition to its effect proofs.
func TestNamespaceKernelActualTypedDescriptors(t *testing.T) {
	for _, item := range []struct {
		name string
		kind int
	}{{"mnt", unix.CLONE_NEWNS}, {"net", unix.CLONE_NEWNET}} {
		t.Run(item.name, func(t *testing.T) {
			file, err := os.Open("/proc/thread-self/ns/" + item.name)
			if err != nil {
				namespaceKernelUnavailable(t, err)
			}
			defer func() { _ = file.Close() }()
			got, err := kernelNamespaceIdentity(int(file.Fd()), item.kind)
			if err != nil {
				namespaceKernelUnavailable(t, err)
			}
			if got.Device == 0 || got.Inode == 0 {
				t.Fatalf("empty actual namespace identity: %+v", got)
			}
			other := unix.CLONE_NEWNET
			if item.kind == other {
				other = unix.CLONE_NEWNS
				legacy, err := networkNamespaceIdentity(int(file.Fd()))
				if err != nil || legacy != got {
					t.Fatalf("network verifier disagrees: %+v, %v", legacy, err)
				}
			}
			if value, err := kernelNamespaceIdentity(int(file.Fd()), other); err == nil || value != (namespaceIdentity{}) {
				t.Fatalf("wrong namespace type accepted: %+v, %v", value, err)
			}
		})
	}
}

func TestNamespaceKernelActualBootReader(t *testing.T) {
	fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		namespaceKernelUnavailable(t, err)
	}
	defer func() { _ = unix.Close(fd) }()
	got, err := readKernelBootID(fd)
	if err != nil {
		namespaceKernelUnavailable(t, err)
	}
	want, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || string(want) != got+"\n" {
		t.Fatalf("independent fixed kernel read disagrees: %q, %v", want, err)
	}
}

func TestNamespaceKernelClosedHandleAndCancellationRefuse(t *testing.T) {
	var empty *kernelInfrastructure
	if err := empty.revalidate(context.Background()); err == nil {
		t.Fatal("nil kernel handle accepted")
	}
	closed := &kernelInfrastructure{}
	if err := closed.close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.revalidate(context.Background()); err == nil {
		t.Fatal("closed kernel handle accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := withNativeKernelInfrastructure(ctx, func(*kernelInfrastructure) error { called = true; return nil }); err == nil || called {
		t.Fatal("canceled or non-native execution reached infrastructure callback")
	}
}
