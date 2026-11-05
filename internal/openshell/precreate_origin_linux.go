package openshell

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/leogr/yoloctl/internal/layout"
	"golang.org/x/sys/unix"
)

// precreateOriginClaim is copied NONSECRET metadata, not an original worker,
// receipt, admission, exclusive-reservation ACK or publication capability.
// The later actual parent protocol must bind all of those independently.
// No pathname, CID, credential or new on-disk marker is carried here.
type precreateOriginClaim struct {
	origin                      containerOrigin
	collection, directory, file pinFileIdentity
	digest                      [sha256.Size]byte
}

func (precreateOriginClaim) String() string {
	return "native precreate origin metadata [not authority]"
}
func (c precreateOriginClaim) GoString() string { return c.String() }

// Only the continuously retained original reservation can supply this claim.
// The ordinary metadata constructor is private; there is no cold reconstruction
// entry or original-create/history inference. This call performs no effects.
func (r *NativeReservation) precreateClaim() (precreateOriginClaim, error) {
	if err := r.Verify(); err != nil {
		return precreateOriginClaim{}, err
	}
	var origin containerOrigin
	if err := json.Unmarshal(r.origin, &origin); err != nil {
		return precreateOriginClaim{}, err
	}
	c := precreateOriginClaim{origin: origin, collection: r.collectionID, directory: r.directoryID, file: r.originID, digest: sha256.Sum256(r.origin)}
	if err := c.validate(origin); err != nil {
		return precreateOriginClaim{}, err
	}
	if err := r.Verify(); err != nil {
		return precreateOriginClaim{}, err
	}
	return c, nil
}

func (c precreateOriginClaim) validate(expected containerOrigin) error {
	for _, token := range []string{expected.DiskID, expected.DiskUUID} {
		if token == "" || len(token) > 256 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return errors.New("precreate origin requires bounded native disk identity tokens")
		}
	}
	if layout.ValidateExistingSpaceName(expected.Space) != nil || layout.ValidateName("agentbox", expected.Box) != nil || expected.DiskID == "" || expected.DiskUUID == "" || len(expected.DiskID) > 256 || len(expected.DiskUUID) > 256 || !lowerHex(expected.Incarnation, 16) || !lowerHex(expected.Qualification, 32) || !lowerHex(expected.HelperSHA256, 32) || c.origin != expected {
		return errors.New("precreate origin does not match independently selected native scope")
	}
	for _, id := range []pinFileIdentity{c.collection, c.directory, c.file} {
		if id.device == 0 || id.inode == 0 || id.mount == 0 || id.device != c.collection.device || id.mount != c.collection.mount {
			return errors.New("precreate origin claim lacks one original filesystem identity")
		}
	}
	raw, err := json.Marshal(expected)
	if err != nil || sha256.Sum256(raw) != c.digest {
		return errors.New("precreate origin claim differs from expected origin bytes")
	}
	return nil
}

// precreateOriginObservation is the parent's separate descriptor observation.
// It has no reference to the creator's NativeReservation, and no authority to
// publish/launch/stop/remove anything. It must never become a generic mode or
// missing-origin=>legacy fallback. The actual native parent must supply its
// independently derived verified root/scope/current storage+kernel+worker and
// live receipt/effect permission before using the observation in that protocol.
type precreateOriginObservation struct {
	root                                   string
	claim                                  precreateOriginClaim
	control, collection, directory, origin *os.File
	controlID                              pinFileIdentity
	uid, gid                               int
	closed                                 bool
	// Nil in production. Project fixtures change real mappings at the cut
	// after the original FD read and before the final independent recheck.
	afterRead func() error
}

func (*precreateOriginObservation) String() string {
	return "independent precreate observation [not authority]"
}
func (p *precreateOriginObservation) GoString() string { return p.String() }

func openPrecreateControl(root string, uid, gid int) (*os.File, pinFileIdentity, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, pinFileIdentity{}, errors.New("precreate observer requires canonical independently selected root")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, pinFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), "precreate-control")
	id, err := pinIdentity(fd, uid, gid, true)
	if err != nil {
		_ = file.Close()
		return nil, pinFileIdentity{}, err
	}
	return file, id, nil
}

// This private ordinary-filesystem seam allows uid/gid/root project fixtures.
// It does not perform native/current-kernel admission. Production wiring must
// use only the parent-derived protected native root and qualified expected
// scope; worker-reported paths or zero/unknown inventories are not substitutes.
func observePrecreateOriginAt(root string, c precreateOriginClaim, expected containerOrigin, uid, gid int) (_ *precreateOriginObservation, err error) {
	if err := c.validate(expected); err != nil {
		return nil, err
	}
	p := &precreateOriginObservation{root: root, claim: c, uid: uid, gid: gid}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	p.control, p.controlID, err = openPrecreateControl(root, uid, gid)
	if err != nil {
		return nil, err
	}
	var id pinFileIdentity
	p.collection, id, err = openPinDirectory(int(p.control.Fd()), "containers", false, uid, gid)
	if err != nil {
		return nil, err
	}
	if id != c.collection || id.device != p.controlID.device || id.mount != p.controlID.mount {
		return nil, errors.New("parent precreate collection differs from original claim or native root")
	}
	p.directory, id, err = openPinDirectory(int(p.collection.Fd()), c.origin.Incarnation, false, uid, gid)
	if err != nil {
		return nil, err
	}
	if id != c.directory {
		return nil, errors.New("parent precreate directory differs from original claim")
	}
	fd, err := unix.Openat2(int(p.directory.Fd()), "origin.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	p.origin = os.NewFile(uintptr(fd), "precreate-origin")
	if err := p.check(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *precreateOriginObservation) check() error {
	if err := p.checkDirectories(); err != nil {
		return err
	}
	before, err := pinIdentity(int(p.origin.Fd()), p.uid, p.gid, false)
	if err != nil || before != p.claim.file {
		return errors.New("parent precreate original origin identity changed")
	}
	if _, err := p.origin.Seek(0, io.SeekStart); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(p.origin, 4097))
	expected, _ := json.Marshal(p.claim.origin)
	if err != nil || len(raw) > 4096 || !bytes.Equal(raw, expected) || sha256.Sum256(raw) != p.claim.digest {
		return errors.New("parent precreate original origin bytes changed")
	}
	if p.afterRead != nil {
		if err := p.afterRead(); err != nil {
			return err
		}
	}
	after, err := pinIdentity(int(p.origin.Fd()), p.uid, p.gid, false)
	if err != nil || after != before {
		return errors.New("parent precreate original origin changed during read")
	}
	fd, err := unix.Openat2(int(p.directory.Fd()), "origin.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return err
	}
	id, statErr := pinIdentity(fd, p.uid, p.gid, false)
	closeErr := unix.Close(fd)
	if statErr != nil || closeErr != nil || id != p.claim.file {
		return errors.New("parent precreate named origin changed")
	}
	return p.checkDirectories()
}

func (p *precreateOriginObservation) checkDirectories() error {
	if p == nil || p.closed || p.control == nil || p.collection == nil || p.directory == nil || p.origin == nil {
		return errors.New("precreate origin observation is closed/incomplete")
	}
	control, id, err := openPrecreateControl(p.root, p.uid, p.gid)
	if err != nil {
		return err
	}
	if closeErr := control.Close(); closeErr != nil {
		return closeErr
	}
	if id != p.controlID {
		return errors.New("precreate native root mapping changed")
	}
	for _, entry := range []struct {
		file   *os.File
		parent int
		name   string
		id     pinFileIdentity
	}{
		{p.control, unix.AT_FDCWD, p.root, p.controlID},
		{p.collection, int(p.control.Fd()), "containers", p.claim.collection},
		{p.directory, int(p.collection.Fd()), p.claim.origin.Incarnation, p.claim.directory},
	} {
		id, err := pinIdentity(int(entry.file.Fd()), p.uid, p.gid, true)
		if err != nil || id != entry.id {
			return errors.New("precreate retained directory changed")
		}
		// openat2 protects all components for the absolute root, and refuses
		// links/mount crossings for each child of its pinned parent.
		var named *os.File
		if entry.parent == unix.AT_FDCWD {
			named, id, err = openPrecreateControl(entry.name, p.uid, p.gid)
		} else {
			named, id, err = openPinDirectory(entry.parent, entry.name, false, p.uid, p.gid)
		}
		if err != nil {
			return err
		}
		closeErr := named.Close()
		if closeErr != nil || id != entry.id {
			return errors.New("precreate named directory changed")
		}
	}
	// An initial pre-CID reservation has precisely its original origin file.
	// This is only a shape check, never historical proof that Create did not run.
	listing, _, err := openPinDirectory(int(p.directory.Fd()), ".", false, p.uid, p.gid)
	if err != nil {
		return err
	}
	names, readErr := listing.Readdirnames(2)
	closeErr := listing.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil || len(names) != 1 || names[0] != "origin.json" {
		return errors.New("precreate reservation has conflicting initial members")
	}
	return nil
}

func (p *precreateOriginObservation) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	var err error
	for _, f := range []*os.File{p.origin, p.directory, p.collection, p.control} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	p.origin, p.directory, p.collection, p.control = nil, nil, nil, nil
	return err
}
