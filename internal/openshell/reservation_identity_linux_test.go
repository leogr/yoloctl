package openshell

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeReservationRetainsExclusivePublicationIdentities(t *testing.T) {
	r := ownedReservationFixture(t)
	for _, id := range []pinFileIdentity{r.collectionID, r.directoryID, r.originID} {
		if id.device == 0 || id.inode == 0 || id.mount == 0 {
			t.Fatal("exclusive reservation did not retain its original identity")
		}
	}
	if r.collectionID.mount != r.directoryID.mount || r.originID.mount != r.directoryID.mount {
		t.Fatal("reservation crossed original mount")
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	if r.originalOrigin == nil {
		t.Fatal("original origin inode is not continuously pinned")
	}
	if id, err := pinIdentity(int(r.originalOrigin.Fd()), os.Geteuid(), os.Getegid(), false); err != nil || id != r.originID {
		t.Fatal("original creation descriptor differs from retained identity", err)
	}
	if flags, err := unix.FcntlInt(r.originalOrigin.Fd(), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("original origin descriptor may leak to child commands", err)
	}
	before := r.originID
	if err := os.Mkdir(filepath.Join(r.Directory(), "unrelated-child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err != nil || r.originID != before {
		t.Fatal("ordinary child publication changed original origin identity", err)
	}
}

func TestNativeReservationRefusesByteIdenticalOriginReplacement(t *testing.T) {
	r := ownedReservationFixture(t)
	origin := filepath.Join(r.Directory(), "origin.json")
	old := filepath.Join(filepath.Dir(r.Directory()), "old-origin")
	raw, err := os.ReadFile(origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(origin, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(origin, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the old file alive so its inode cannot be recycled for this fixture.
	f, err := os.Open(origin)
	if err != nil {
		t.Fatal(err)
	}
	id, statErr := pinIdentity(int(f.Fd()), os.Geteuid(), os.Getegid(), false)
	closeErr := f.Close()
	if statErr != nil || closeErr != nil || id == r.originID {
		t.Fatal("replacement fixture did not select a distinct valid original-role file", statErr, closeErr)
	}
	if id, err := pinIdentity(int(r.originalOrigin.Fd()), os.Geteuid(), os.Getegid(), false); err != nil || id != r.originID {
		t.Fatal("old origin fixture lost its original valid pinned inode", err)
	}
	if err := r.Verify(); err == nil {
		t.Fatal("byte-identical replacement origin adopted")
	}
	if err := r.RemoveAfterContainerRemoval(t.Context()); err == nil {
		t.Fatal("cleanup consumed a replacement origin")
	}
	got, err := os.ReadFile(origin)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("refused cleanup changed foreign replacement", err)
	}
}

func TestNativeReservationRequiresOriginalMountIdentities(t *testing.T) {
	for _, member := range []string{"collection", "directory", "origin"} {
		t.Run(member, func(t *testing.T) {
			r := ownedReservationFixture(t)
			switch member {
			case "collection":
				r.collectionID.mount++
			case "directory":
				r.directoryID.mount++
			case "origin":
				r.originID.mount++
			}
			if err := r.Verify(); err == nil {
				t.Fatal("changed original mount identity accepted")
			}
		})
	}
	t.Run("lost-original-fd", func(t *testing.T) {
		r := ownedReservationFixture(t)
		if err := r.originalOrigin.Close(); err != nil {
			t.Fatal(err)
		}
		if err := r.Verify(); err == nil {
			t.Fatal("closed original origin descriptor accepted")
		}
	})
	var zero NativeReservation
	if err := zero.Verify(); err == nil {
		t.Fatal("zero reservation fabricated original publication")
	}
}

func TestPrivatePublicationObserverSeesOriginalFDAndCannotPublishAfterFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "observer-failure"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			boom := errors.New("original publication observer refused")
			var original pinFileIdentity
			observations := 0
			err := publishPrivateOnceObserved(dir, "origin.json", []byte("nonsecret-origin"), os.Geteuid(), os.Getegid(), func(file *os.File) error {
				observations++
				if _, err := os.Lstat(filepath.Join(dir, "origin.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("publication exposed before original FD observer", err)
				}
				id, err := pinIdentity(int(file.Fd()), os.Geteuid(), os.Getegid(), false)
				if err != nil {
					return err
				}
				original = id
				if fail {
					return boom
				}
				return nil
			})
			if observations != 1 || original.inode == 0 {
				t.Fatal("original FD observer not invoked exactly once")
			}
			if fail {
				if !errors.Is(err, boom) {
					t.Fatal("observer failure lost", err)
				}
				if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) != 0 {
					t.Fatal("failed observer published or retained a temporary file", readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(filepath.Join(dir, "origin.json"))
			if err != nil {
				t.Fatal(err)
			}
			id, statErr := pinIdentity(int(f.Fd()), os.Geteuid(), os.Getegid(), false)
			closeErr := f.Close()
			if statErr != nil || closeErr != nil || id != original {
				t.Fatal("publication replaced actual created inode", statErr, closeErr)
			}
			if err := publishPrivateOnceObserved(dir, "origin.json", []byte("other"), os.Geteuid(), os.Getegid(), nil); !errors.Is(err, os.ErrExist) {
				t.Fatal("exclusive publication adopted existing original", err)
			}
		})
	}
}

func TestNativeReservationCloseReleasesOriginalOriginWithoutDeletingIt(t *testing.T) {
	r := ownedReservationFixture(t)
	original := r.originalOrigin
	name := filepath.Join(r.Directory(), "origin.json")
	if original == nil {
		t.Fatal("missing original descriptor")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("original origin descriptor leaked after Close", err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal("Close changed retained runtime origin", err)
	}
	if err := r.Verify(); err == nil {
		t.Fatal("closed reservation remained usable")
	}
}
