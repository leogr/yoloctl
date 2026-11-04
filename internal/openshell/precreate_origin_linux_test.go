package openshell

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func precreateObservationFixture(t *testing.T) (*NativeReservation, precreateOriginClaim, *precreateOriginObservation) {
	t.Helper()
	r := ownedReservationFixture(t)
	c, err := r.precreateClaim()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(r.Directory()))
	p, err := observePrecreateOriginAt(root, c, c.origin, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return r, c, p
}

func TestPrecreateOriginIndependentParentReadsOriginalWithoutEffects(t *testing.T) {
	r, c, p := precreateObservationFixture(t)
	if p.origin == r.originalOrigin || p.directory == r.directory || p.collection == r.parent {
		t.Fatal("parent borrowed creator descriptors instead of independently opening")
	}
	if err := p.check(); err != nil {
		t.Fatal(err)
	}
	if files, err := os.ReadDir(r.Directory()); err != nil || len(files) != 1 || files[0].Name() != "origin.json" {
		t.Fatal("read-only observer changed reservation", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := r.precreateClaim(); err == nil || got != (precreateOriginClaim{}) {
		t.Fatal("closed creator reconstructed original publication claim")
	}
	// Existing metadata may still be observed; this is deliberately not proof
	// of a live creator/receipt or authority to resume its publication.
	if err := p.check(); err != nil {
		t.Fatal("parent observation was coupled to the creator's Go object", err)
	}
	if p.claim != c {
		t.Fatal("independent observation changed claimed immutable scope")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.check(); err == nil {
		t.Fatal("closed parent observation fabricated readiness")
	}
	var zero precreateOriginObservation
	if err := zero.check(); err == nil {
		t.Fatal("zero parent observation fabricated readiness")
	}
}

func TestPrecreateOriginRequiresIndependentlySelectedScopeAndCompleteClaim(t *testing.T) {
	r, c, _ := precreateObservationFixture(t)
	root := filepath.Dir(filepath.Dir(r.Directory()))
	for _, field := range []string{"space", "disk", "uuid", "box", "incarnation", "qualification", "helper", "hash", "collection", "directory", "file", "zero", "malformed-token"} {
		t.Run(field, func(t *testing.T) {
			claim := c
			expected := c.origin
			switch field {
			case "space":
				expected.Space = "other"
			case "disk":
				expected.DiskID = "other"
			case "uuid":
				expected.DiskUUID = "other"
			case "box":
				expected.Box = "other"
			case "incarnation":
				expected.Incarnation = strings.Repeat("b", 32)
			case "qualification":
				expected.Qualification = strings.Repeat("d", 64)
			case "helper":
				expected.HelperSHA256 = strings.Repeat("e", 64)
			case "hash":
				claim.digest[0] ^= 1
			case "collection":
				claim.collection.mount++
			case "directory":
				claim.directory.inode++
			case "file":
				claim.file.inode++
			case "zero":
				claim = precreateOriginClaim{}
			case "malformed-token":
				expected.DiskUUID = "uuid\x00suffix"
				claim.origin = expected
				raw, _ := json.Marshal(expected)
				claim.digest = sha256.Sum256(raw)
			}
			p, err := observePrecreateOriginAt(root, claim, expected, os.Geteuid(), os.Getegid())
			if err == nil {
				_ = p.Close()
				t.Fatal("wrong independent precreate scope/identity accepted")
			}
		})
	}
}

func TestPrecreateOriginRefusesCurrentMappingBytesRoleAndConflictingMembers(t *testing.T) {
	for _, change := range []string{"control-root", "collection", "directory", "origin-copy", "origin-bytes", "origin-link", "origin-mode", "partial-generation", "missing-origin", "lost-fd"} {
		t.Run(change, func(t *testing.T) {
			r, _, p := precreateObservationFixture(t)
			dir := r.Directory()
			origin := filepath.Join(dir, "origin.json")
			var err error
			switch change {
			case "control-root":
				err = os.Rename(p.root, p.root+"-old")
				if err == nil {
					t.Cleanup(func() { _ = os.Remove(p.root); _ = os.Rename(p.root+"-old", p.root) })
					err = os.Mkdir(p.root, 0o700)
				}
			case "collection":
				parent := filepath.Dir(dir)
				err = os.Rename(parent, parent+"-old")
				if err == nil {
					err = os.Mkdir(parent, 0o700)
				}
			case "directory":
				err = os.Rename(dir, dir+"-old")
				if err == nil {
					err = os.Mkdir(dir, 0o700)
				}
			case "origin-copy":
				raw, readErr := os.ReadFile(origin)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.Rename(origin, filepath.Join(filepath.Dir(dir), "old-origin"))
				if err == nil {
					err = os.WriteFile(origin, raw, 0o600)
				}
			case "origin-bytes":
				raw, readErr := os.ReadFile(origin)
				if readErr != nil {
					t.Fatal(readErr)
				}
				raw[10] ^= 1
				err = os.WriteFile(origin, raw, 0o600)
			case "origin-link":
				err = os.Rename(origin, filepath.Join(filepath.Dir(dir), "old-origin"))
				if err == nil {
					err = os.Symlink(filepath.Join(filepath.Dir(dir), "old-origin"), origin)
				}
			case "origin-mode":
				err = os.Chmod(origin, 0o644)
			case "partial-generation":
				err = os.Mkdir(filepath.Join(dir, "generations"), 0o700)
			case "missing-origin":
				err = os.Remove(origin)
			case "lost-fd":
				err = p.origin.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := p.check(); err == nil {
				t.Fatal("changed original parent precreate observation accepted")
			}
		})
	}
}

func TestPrecreateOriginDoesNotAdoptReplacementAtInitialParentOpen(t *testing.T) {
	r := ownedReservationFixture(t)
	c, err := r.precreateClaim()
	if err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(r.Directory(), "origin.json")
	raw, err := os.ReadFile(origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(origin, filepath.Join(filepath.Dir(r.Directory()), "old-origin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(origin, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := observePrecreateOriginAt(filepath.Dir(filepath.Dir(r.Directory())), c, c.origin, os.Geteuid(), os.Getegid())
	if err == nil {
		_ = p.Close()
		t.Fatal("parent adopted byte-identical replacement before initial capture")
	}
	after, err := os.ReadFile(origin)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatal("failed parent open changed replacement", err)
	}
	if claim, err := r.precreateClaim(); err == nil || claim != (precreateOriginClaim{}) {
		t.Fatal("creator reminted failed original claim")
	}
	if _, err := os.Stat(filepath.Join(r.Directory(), "generations")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("observer initialized runtime", err)
	}
}

func TestPrecreateOriginBracketsActualReadWithCurrentNativeRoot(t *testing.T) {
	_, _, p := precreateObservationFixture(t)
	called := false
	p.afterRead = func() error {
		called = true
		if err := os.Rename(p.root, p.root+"-old"); err != nil {
			return err
		}
		t.Cleanup(func() { _ = os.Remove(p.root); _ = os.Rename(p.root+"-old", p.root) })
		return os.Mkdir(p.root, 0o700)
	}
	err := p.check()
	if !called || err == nil || !strings.Contains(err.Error(), "precreate native root mapping changed") {
		t.Fatal("root replacement after original read escaped final mapping bracket", err)
	}
}
