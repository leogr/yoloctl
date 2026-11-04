package openshell

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func nativePinMountViewFixture(t *testing.T) (string, []nativePinMount, pinFileIdentity, pinFileIdentity) {
	t.Helper()
	suffix := "/runtime/openshell/namespace-pins/" + strings.Repeat("a", 32) + "/pins/g" + strings.Repeat("b", 48)
	target := "/mnt/native" + suffix
	// Synthetic kernel-format inventory, not an attributed VM readback.
	raw := "10 1 8:16 / /mnt/native rw,relatime shared:1 - ext4 /dev/vdb rw\n" +
		"20 10 8:16 " + suffix + " " + target + " rw,relatime - ext4 /dev/vdb rw\n" +
		"21 20 0:4 net:[9001] " + target + "/namespace rw - nsfs nsfs rw\n"
	mounts, err := parseNativePinMounts([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return target, mounts, pinFileIdentity{unix.Mkdev(8, 16), 101, 20}, pinFileIdentity{unix.Mkdev(0, 4), 9001, 21}
}

func TestNamespacePinMountViewDistinguishesBareBoundPrivateAndPinned(t *testing.T) {
	target, mounts, directory, namespace := nativePinMountViewFixture(t)
	bareDir := directory
	bareDir.mount = 10
	bareLeaf := pinFileIdentity{bareDir.device, 102, 10}
	bare, err := validateNativePinMountView(mounts[:1], target, 10, bareDir, bareLeaf, false)
	if err != nil || bare.directoryBound || bare.directoryPrivate || bare.namespaceMount != 0 {
		t.Fatalf("bare view: %+v, %v", bare, err)
	}
	placeholder := pinFileIdentity{directory.device, 102, 20}
	for _, private := range []bool{false, true} {
		dirMounts := append([]nativePinMount(nil), mounts[:2]...)
		if !private {
			dirMounts[1].optional = []string{"shared:1"}
		}
		view, err := validateNativePinMountView(dirMounts, target, 10, directory, placeholder, false)
		if err != nil || !view.directoryBound || view.directoryPrivate != private || view.namespaceMount != 0 {
			t.Fatalf("directory view private=%v: %+v, %v", private, view, err)
		}
	}
	pinned, err := validateNativePinMountView(mounts, target, 10, directory, namespace, true)
	if err != nil || !pinned.directoryBound || !pinned.directoryPrivate || pinned.directoryMount != 20 || pinned.namespaceMount != 21 || pinned.namespace != nil {
		t.Fatalf("pinned view (synthetic has no namespace FD): %+v, %v", pinned, err)
	}
}

func TestNamespacePinMountViewRejectsForeignStackedAndPropagatingMounts(t *testing.T) {
	for _, change := range []string{"base-missing", "base-type", "base-ro", "base-device", "base-root", "directory-id", "directory-parent", "directory-root", "directory-source", "directory-ro", "directory-shared", "directory-slave", "directory-unknown", "namespace-id", "namespace-parent", "namespace-device", "namespace-root", "namespace-type", "namespace-source", "namespace-shared", "namespace-missing", "untyped-leaf", "wrong-leaf-inode", "wrong-leaf-mount", "extra-child", "stacked-directory", "stacked-namespace"} {
		t.Run(change, func(t *testing.T) {
			target, mounts, directory, namespace := nativePinMountViewFixture(t)
			typed := true
			switch change {
			case "base-missing":
				mounts = mounts[1:]
			case "base-type":
				mounts[0].filesystem = "tmpfs"
			case "base-ro":
				mounts[0].readOnly = true
			case "base-device":
				mounts[0].device++
			case "base-root":
				mounts[0].root = "/../../wrong"
			case "directory-id":
				mounts[1].id++
			case "directory-parent":
				mounts[1].parent = 999
			case "directory-root":
				mounts[1].root = "/another-directory"
			case "directory-source":
				mounts[1].source = "/dev/foreign"
			case "directory-ro":
				mounts[1].readOnly = true
			case "directory-shared":
				mounts[1].optional = []string{"shared:7"}
			case "directory-slave":
				mounts[1].optional = []string{"master:7", "propagate_from:9"}
			case "directory-unknown":
				mounts[1].optional = []string{"future:7"}
			case "namespace-id":
				mounts[2].id = 99
			case "namespace-parent":
				mounts[2].parent = 10
			case "namespace-device":
				mounts[2].device++
			case "namespace-root":
				mounts[2].root = "mnt:[9001]"
			case "namespace-type":
				mounts[2].filesystem = "tmpfs"
			case "namespace-source":
				mounts[2].source = "foreign"
			case "namespace-shared":
				mounts[2].optional = []string{"shared:7"}
			case "namespace-missing":
				mounts = mounts[:2]
			case "untyped-leaf":
				typed = false
			case "wrong-leaf-inode":
				namespace.inode++
			case "wrong-leaf-mount":
				namespace.mount++
			case "extra-child":
				mounts = append(mounts, nativePinMount{id: 99, point: target + "/state.json"})
			case "stacked-directory":
				copy := mounts[1]
				copy.id = 99
				mounts = append(mounts, copy)
			case "stacked-namespace":
				copy := mounts[2]
				copy.id = 99
				mounts = append(mounts, copy)
			}
			view, err := validateNativePinMountView(mounts, target, 10, directory, namespace, typed)
			if err == nil || view.directoryMount != 0 || view.namespaceMount != 0 || view.namespace != nil {
				t.Fatalf("foreign/unknown mount returned usable view: %+v, %v", view, err)
			}
		})
	}
}

func TestNamespacePinMountInventoryStrictFieldsAndEscapes(t *testing.T) {
	raw := "10 1 8:16 /root\\040dir /mnt/native\\040dir rw,relatime shared:1 - ext4 /dev/data\\0431 rw\n"
	mounts, err := parseNativePinMounts([]byte(raw))
	if err != nil || len(mounts) != 1 || mounts[0].root != "/root dir" || mounts[0].point != "/mnt/native dir" || mounts[0].source != "/dev/data#1" || len(mounts[0].optional) != 1 {
		t.Fatalf("kernel escape/propagation forms: %+v, %v", mounts, err)
	}
	for _, bad := range []string{"", "\n", raw + raw, strings.Replace(raw, "10 1", "010 1", 1), strings.Replace(raw, "8:16", "8:016", 1), strings.Replace(raw, "8:16", "8:16:2", 1), strings.Replace(raw, " - ", " ", 1), strings.Replace(raw, "rw,relatime", "ro,rw", 1), strings.Replace(raw, "\\040", "\\999", 1), strings.Replace(raw, "/mnt/native\\040dir", "relative", 1), raw + "partial\n", raw + "\x00"} {
		if got, err := parseNativePinMounts([]byte(bad)); err == nil || got != nil {
			t.Fatalf("invalid mount inventory returned data: %q -> %+v, %v", bad, got, err)
		}
	}
}

func TestNamespacePinMountObservationRefusesMissingKernelAndClosesFD(t *testing.T) {
	if view, err := observeNativePinMounts(context.Background(), nil, nil); err == nil || view != nil {
		t.Fatal("missing kernel authority accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if view, err := observeNativePinMounts(ctx, nil, nil); !errors.Is(err, context.Canceled) || view != nil {
		t.Fatal("canceled observation reached infrastructure", err)
	}
	if path, err := nativePinDirectoryPath(nil); err == nil || path != "" {
		t.Fatal("missing files accepted as native target")
	}
	file, err := os.CreateTemp(t.TempDir(), "close-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary FD closure fixture, not a namespace or pin authority.
	view := &nativePinMountView{namespace: file}
	if err := view.Close(); err != nil || view.namespace != nil {
		t.Fatal("observation did not release descriptor", err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("returned descriptor remains open", err)
	}
	if err := view.Close(); err != nil {
		t.Fatal("closed observation was not idempotent", err)
	}
}
