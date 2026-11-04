package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const stalePinTestBoot = "22222222-3333-4444-8555-666666666666"

func TestNamespaceStalePinRegularIdentityAcceptsChangedDeviceOnlyAfterDifferentBoot(t *testing.T) {
	root, _, lock, files, pin, record := namespacePinFilesFixture(t)
	origin, err := decodeNamespacePinOrigin(files.origin, record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an old dev_t in the immutable on-disk origin. Ordinary file
	// fixture only: no actual VM boot, native disk or kernel context is claimed.
	origin.Directory.Device++
	raw, _ := json.Marshal(origin)
	name := filepath.Join(root, "namespace-pins", lock.incarnation, "origins", record.Generation+".json")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if cold, err := lock.read(record, pin.Scope.ContainerID); err == nil {
		_ = cold.Close()
		t.Fatal("same-boot ordinary reader weakened original device binding")
	}
	_, placeholder, err := readPinFileAt(int(files.directory.Fd()), "namespace", lock.uid, lock.gid, 0)
	if err != nil {
		t.Fatal(err)
	}
	native := lock.directories[4].identity
	if err := validateStalePinFiles(origin, stalePinTestBoot, native, files.identity, placeholder); err != nil {
		t.Fatal("new-boot regular inode/current-native binding refused changed old device", err)
	}
	for _, change := range []string{"same-boot", "invalid-boot", "zero-boot", "directory-inode", "placeholder-inode", "foreign-device", "old-device", "directory-mount", "placeholder-mount", "zero-native", "zero-directory-origin", "zero-placeholder-origin"} {
		o, n, d, p := origin, native, files.identity, placeholder
		boot := stalePinTestBoot
		switch change {
		case "same-boot":
			boot = origin.Record.Scope.KernelBootID
		case "invalid-boot":
			boot = "not-a-boot"
		case "zero-boot":
			boot = "00000000-0000-0000-0000-000000000000"
		case "directory-inode":
			d.inode++
		case "placeholder-inode":
			p.inode++
		case "foreign-device":
			d.device++
		case "old-device":
			d.device = origin.Directory.Device
		case "directory-mount":
			d.mount++
		case "placeholder-mount":
			p.mount++
		case "zero-native":
			n.mount = 0
		case "zero-directory-origin":
			o.Directory.Inode = 0
		case "zero-placeholder-origin":
			o.Record.Placeholder.Inode = 0
		}
		if err := validateStalePinFiles(o, boot, n, d, p); err == nil {
			t.Fatal("foreign/same-boot identity accepted", change)
		}
	}
}

func TestNamespaceStalePinMountInventoryRefusesAnyCurrentMountAtOldPath(t *testing.T) {
	target, mounts, directory, _ := nativePinMountViewFixture(t)
	directory.mount = 10
	if err := validateBareStalePinDirectory(mounts[:1], target, 10, directory); err != nil {
		t.Fatal("actual bare inventory shape refused", err)
	}
	for _, change := range []string{"directory-bind", "namespace-without-directory", "metadata-child-mount", "foreign-child", "wrong-base", "base-missing", "base-type", "base-ro", "base-device", "base-root", "base-path", "duplicate-base", "opened-bind", "target-outside", "target-base", "target-relative"} {
		t.Run(change, func(t *testing.T) {
			inventory := append([]nativePinMount(nil), mounts[:1]...)
			selected, d := target, directory
			base := uint64(10)
			switch change {
			case "directory-bind":
				inventory = append(inventory, mounts[1])
			case "namespace-without-directory":
				inventory = append(inventory, mounts[2])
			case "metadata-child-mount":
				inventory = append(inventory, nativePinMount{id: 99, point: target + "/state.json"})
			case "foreign-child":
				inventory = append(inventory, nativePinMount{id: 99, point: target + "/foreign"})
			case "wrong-base":
				base = 11
			case "base-missing":
				inventory = nil
			case "base-type":
				inventory[0].filesystem = "tmpfs"
			case "base-ro":
				inventory[0].readOnly = true
			case "base-device":
				inventory[0].device++
			case "base-root":
				inventory[0].root = "/root/../unknown"
			case "base-path":
				inventory[0].point = "relative"
			case "duplicate-base":
				inventory = append(inventory, inventory[0])
			case "opened-bind":
				d.mount = 20
			case "target-outside":
				selected = "/outside/pin"
			case "target-base":
				selected = "/mnt/native"
			case "target-relative":
				selected = "relative"
			}
			if err := validateBareStalePinDirectory(inventory, selected, base, d); err == nil {
				t.Fatal("current mount/foreign base accepted", change)
			}
		})
	}
	// An unrelated sibling is not owned by this reader and grants no authority.
	sibling := append(append([]nativePinMount(nil), mounts[:1]...), nativePinMount{id: 99, point: target + "-other/namespace"})
	if err := validateBareStalePinDirectory(sibling, target, 10, directory); err != nil {
		t.Fatal("unrelated sibling changed exact target scope", err)
	}
}

func TestNamespacePinOriginDecoderRejectsAmbiguityAndChangedSelection(t *testing.T) {
	_, _, _, files, pin, record := namespacePinFilesFixture(t)
	if _, err := decodeNamespacePinOrigin(files.origin, record, pin.Scope.ContainerID); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"unknown", "duplicate", "trailing", "null", "empty", "oversize", "wrong-cid", "wrong-record", "zero-directory", "later-origin-phase"} {
		raw := append([]byte(nil), files.origin...)
		cid, r := pin.Scope.ContainerID, record
		switch change {
		case "unknown":
			raw = append([]byte(`{"extra":true,`), raw[1:]...)
		case "duplicate":
			raw = append([]byte(`{"directory":{},`), raw[1:]...)
		case "trailing":
			raw = append(raw, []byte(`{}`)...)
		case "null":
			raw = []byte(`null`)
		case "empty":
			raw = nil
		case "oversize":
			raw = []byte(strings.Repeat(" ", (4<<20)+1))
		case "wrong-cid":
			cid = strings.Repeat("b", 64)
		case "wrong-record":
			r.Generation = "g" + strings.Repeat("b", 48)
		case "zero-directory", "later-origin-phase":
			var o namespacePinOrigin
			if err := json.Unmarshal(raw, &o); err != nil {
				t.Fatal(err)
			}
			if change == "zero-directory" {
				o.Directory.Device = 0
			} else {
				o.Record.Phase = pinDirectoryBindAttempt
			}
			raw, _ = json.Marshal(o)
		}
		got, err := decodeNamespacePinOrigin(raw, r, cid)
		if err == nil || got.Directory.Inode != 0 {
			t.Fatal("invalid origin returned usable metadata", change, err)
		}
	}
}

func TestNamespaceStalePinActualReaderRequiresCurrentKernelAndNeverCreatesFiles(t *testing.T) {
	if got, err := observeStaleNamespacePin(context.Background(), nil, nil, RuntimeRecord{}, ""); err == nil || got != nil {
		t.Fatal("reader accepted missing actual kernel", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := observeStaleNamespacePin(ctx, nil, nil, RuntimeRecord{}, ""); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal("canceled reader acquired metadata", got, err)
	}
	if mounts, err := readNativePinMountInventory(context.Background(), nil); err == nil || mounts != nil {
		t.Fatal("inventory reader accepted missing actual kernel", err)
	}
	if err := (*staleNamespacePinObservation)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, files, _, _ := namespacePinFilesFixture(t)
	observed := &staleNamespacePinObservation{files: files, currentBoot: stalePinTestBoot, placeholder: pinFileIdentity{unix.Mkdev(8, 16), 101, 10}}
	if err := observed.Close(); err != nil {
		t.Fatal(err)
	}
	if files.directory != nil {
		t.Fatal("closing stale observation retained directory FD")
	}
}
