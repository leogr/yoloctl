package openshell

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"golang.org/x/sys/unix"
)

func TestNamespacePublicationIdentityComesFromOwnDurableWriter(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	uid, gid := os.Geteuid(), os.Getegid()
	raw := []byte(`{"fixture":"first"}`)
	first, err := writePinFileAtIdentity(int(directory.Fd()), "state.json", raw, false, uid, gid)
	if err != nil || first == (pinFileIdentity{}) {
		t.Fatal("durable writer omitted its actual created identity", first, err)
	}
	observed, id, err := readPinFileAt(int(directory.Fd()), "state.json", uid, gid, 4096)
	if err != nil || id != first || !bytes.Equal(observed, raw) {
		t.Fatal("publication identity does not name committed bytes", first, id, err)
	}
	second, err := writePinFileAtIdentity(int(directory.Fd()), "state.json", []byte(`{"fixture":"second"}`), true, uid, gid)
	if err != nil || second == first || second == (pinFileIdentity{}) {
		t.Fatal("atomic replacement reused prior producer identity", first, second, err)
	}
	failed, err := writePinFileAtIdentity(int(directory.Fd()), "state.json", []byte("forbidden replacement"), false, uid, gid)
	if !errors.Is(err, os.ErrExist) || failed != (pinFileIdentity{}) {
		t.Fatal("failed no-replace returned publication evidence", failed, err)
	}
	_, id, err = readPinFileAt(int(directory.Fd()), "state.json", uid, gid, 4096)
	if err != nil || id != second {
		t.Fatal("failed write changed target identity", id, err)
	}
}

// A real owned O_PATH directory supports child creation/rename but cannot be
// fsynced. This project fixture exercises an actual post-rename CommitError;
// the visible target must never be interpreted as a successful writer ACK.
func TestNamespacePublicationFailedDirectorySyncReturnsNoProducerEvidence(t *testing.T) {
	root := t.TempDir()
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	id, err := writePinFileAtIdentity(fd, "state.json", []byte("partial publication fixture"), false, os.Geteuid(), os.Getegid())
	var commit *atomicfile.CommitError
	if !errors.As(err, &commit) || id != (pinFileIdentity{}) {
		t.Fatal("lost directory sync returned producer success", id, err)
	}
	if raw, _, readErr := readPinFileAt(fd, "state.json", os.Geteuid(), os.Getegid(), 4096); readErr != nil || string(raw) != "partial publication fixture" {
		t.Fatal("fixture did not reach post-rename directory sync", readErr, string(raw))
	}
}

func TestNamespacePublicationRecordsOnlySuccessfulSameCallStateAndMarkerWrites(t *testing.T) {
	_, _, lock, files, pin, record := namespacePinFilesFixture(t)
	if files.currentPublication != nil || files.retiredPublication != nil {
		t.Fatal("cold metadata became producer authority")
	}
	pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinDirectoryUnmounted, 122, 123 // Scripted bookkeeping, no mount effect.
	if err := files.writeObservation(pin); err != nil {
		t.Fatal(err)
	}
	proof := files.currentPublication
	if proof == nil || !bytes.Equal(proof.raw, files.current) {
		t.Fatal("successful state writer omitted same-call producer evidence")
	}
	_, id, err := readPinFileAt(int(files.directory.Fd()), "state.json", lock.uid, lock.gid, 4<<20)
	if err != nil || id != proof.identity {
		t.Fatal("state producer identity differs from own file", id, err)
	}
	if err := files.publishRetired(syntheticRetirementACKs()); err != nil {
		t.Fatal(err)
	}
	marker := files.retiredPublication
	raw, id, err := readPinFileAt(int(files.directory.Fd()), "retired.json", lock.uid, lock.gid, 4096)
	if err != nil || marker == nil || marker.identity != id || !bytes.Equal(marker.raw, raw) {
		t.Fatal("successful marker writer omitted exact producer evidence", err)
	}
	if err := files.publishRetired(syntheticRetirementACKs()); !errors.Is(err, os.ErrExist) || files.retiredPublication != marker {
		t.Fatal("failed marker publication replaced same-call evidence", err)
	}
	cold, err := lock.read(record, pin.Scope.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cold.Close() }()
	if ok, err := cold.readRetired(); !ok || err != nil || cold.currentPublication != nil || cold.retiredPublication != nil {
		t.Fatal("cold canonical metadata manufactured producer evidence", ok, err)
	}
}

func TestNamespacePublicationPartialStateOrMarkerDoesNotUpdateExpectedProof(t *testing.T) {
	for _, marker := range []bool{false, true} {
		t.Run(map[bool]string{false: "state", true: "marker"}[marker], func(t *testing.T) {
			_, _, _, files, pin, _ := namespacePinFilesFixture(t)
			if marker {
				pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinDirectoryUnmounted, 122, 123
				if err := files.writeObservation(pin); err != nil {
					t.Fatal(err)
				}
			}
			oldRaw := bytes.Clone(files.current)
			oldProof := files.currentPublication
			original := files.directory
			fd, err := unix.Openat(int(original.Fd()), ".", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			lookup := os.NewFile(uintptr(fd), "publication-failure-fixture")
			defer func() { files.directory = original; _ = lookup.Close() }()
			files.directory = lookup
			if marker {
				err = files.publishRetired(syntheticRetirementACKs())
			} else {
				pin.Phase, pin.DirectoryMountID, pin.PinMountID = pinPresent, 122, 123
				err = files.writeObservation(pin)
			}
			var commit *atomicfile.CommitError
			if !errors.As(err, &commit) || !bytes.Equal(files.current, oldRaw) || files.currentPublication != oldProof || files.retiredPublication != nil {
				t.Fatal("partial durable publication updated expected producer state", err)
			}
		})
	}
}
