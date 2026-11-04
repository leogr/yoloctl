package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// nativeRegistrationCustody retains the ACTUAL original directory and staging
// file through publication. It supplies no parent execution, origin, receipt,
// listener, request, CID or cleanup authority. There is no public constructor
// or native caller yet. The future original parent must claim the scanner and
// verify origin/storage/effect permission before even directory visibility.
// Existing/cold registrations cannot be reconstructed through this fresh path.
type nativeRegistrationCustody struct {
	mu                                       sync.Mutex
	root, directory, file                    *os.File
	rootID, directoryID, fileID              endpointFileID
	identity                                 RegistrationIdentity
	raw                                      []byte
	openRoot                                 func() (*os.File, error)
	verify                                   func(context.Context) error
	uid, gid                                 uint32
	created, publishTried, published, closed bool
	staging                                  string
	failure, closeErr                        error
	rename                                   func(int, string, string) error
}

// createNativeRegistrationCustody performs only an exclusive original mkdir.
// openRoot must return a fresh owned handle to the independently verified
// fixed registration root; verify is the original parent's live admission.
// uid/gid and opener are private ordinary-filesystem fixture seams, not public
// caller-selected native roots. Root creation is a separate prerequisite.
// After ANY mkdir attempt return the original partial owner even on failure;
// never open/adopt EEXIST, retry, or erase evidence to manufacture a fresh ACK.
func createNativeRegistrationCustody(ctx context.Context, openRoot func() (*os.File, error), identity RegistrationIdentity, uid, gid int, verify func(context.Context) error) (_ *nativeRegistrationCustody, err error) {
	if openRoot == nil || verify == nil || uid < 0 || gid < 0 || identity.Mode != RegistrationOpenShellNative {
		return nil, errors.New("native registration lacks original infrastructure inputs")
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verify(ctx); err != nil {
		return nil, err
	}
	p := &nativeRegistrationCustody{identity: identity, openRoot: openRoot, verify: verify, uid: uint32(uid), gid: uint32(gid), rename: renameLegacyIdentityNoReplace}
	p.root, err = openRoot()
	if err != nil || p.root == nil {
		return nil, errors.Join(err, errors.New("native registration root unavailable"), p.Close())
	}
	p.rootID, err = endpointID(int(p.root.Fd()), "")
	if err != nil || !p.trustedRoot(p.rootID) {
		return nil, errors.Join(err, errors.New("native registration root untrusted"), p.Close())
	}
	if err := p.checkRoot(ctx); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	// From here the original effect may have occurred. Retain all partial
	// state/handles and the error; Close later releases handles only, not names.
	fail := func(err error) (*nativeRegistrationCustody, error) { p.failure = err; return p, err }
	if err := unix.Mkdirat(int(p.root.Fd()), identity.Incarnation, 0o700); err != nil {
		return fail(err)
	}
	p.created = true
	p.directory, err = openEndpointDirectory(int(p.root.Fd()), identity.Incarnation, true)
	if err != nil {
		return fail(err)
	}
	// The future caller's singleton claim must exclude every other legitimate
	// creator/retirer for this name across mkdir and this FIRST acquisition.
	// No pathname metadata test can defeat an arbitrary root replacement in
	// that gap. Do not present this private primitive without that ownership
	// premise as an independently authenticated native creation grant.
	fresh, err := endpointID(int(p.directory.Fd()), "")
	if err != nil || fresh.mode&unix.S_IFMT != unix.S_IFDIR || fresh.mode&0o7777 != 0o700 || fresh.uid != p.uid || fresh.gid != p.gid || fresh.mount != p.rootID.mount {
		return fail(errors.Join(err, errors.New("fresh native directory acquisition changed")))
	}
	named, err := endpointID(int(p.root.Fd()), identity.Incarnation)
	if err != nil || !fresh.sameDirectory(named) {
		return fail(errors.New("fresh native directory name changed before setup"))
	}
	if err := p.directory.Chmod(0o755); err != nil {
		return fail(err)
	}
	p.directoryID, err = endpointID(int(p.directory.Fd()), "")
	if err != nil || !p.trustedDirectory(p.directoryID) {
		return fail(errors.Join(err, errors.New("fresh native registration directory untrusted")))
	}
	if err := p.directory.Sync(); err != nil {
		return fail(err)
	}
	if err := p.root.Sync(); err != nil {
		return fail(err)
	}
	if err := p.check(ctx); err != nil {
		return fail(err)
	}
	return p, nil
}

func (p *nativeRegistrationCustody) trustedRoot(id endpointFileID) bool {
	return id.mode&unix.S_IFMT == unix.S_IFDIR && id.mode&0o7022 == 0 && id.uid == p.uid && id.gid == p.gid
}
func (p *nativeRegistrationCustody) trustedDirectory(id endpointFileID) bool {
	return id.mode&unix.S_IFMT == unix.S_IFDIR && id.mode&0o7777 == 0o755 && id.uid == p.uid && id.gid == p.gid && id.mount == p.rootID.mount
}

func (p *nativeRegistrationCustody) checkRoot(ctx context.Context) error {
	if p.closed || p.root == nil || p.openRoot == nil || p.verify == nil {
		return errors.New("native registration root custody expired")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.verify(ctx); err != nil {
		return err
	}
	retained, err := endpointID(int(p.root.Fd()), "")
	if err != nil || !p.trustedRoot(retained) || !p.rootID.sameDirectory(retained) {
		return errors.New("original native registration root changed")
	}
	current, err := p.openRoot()
	if err != nil || current == nil {
		var closeErr error
		if current != nil {
			closeErr = current.Close()
		}
		return errors.Join(err, closeErr, errors.New("current native registration root unavailable"))
	}
	id, statErr := endpointID(int(current.Fd()), "")
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !p.rootID.sameDirectory(id) {
		return errors.Join(statErr, closeErr, errors.New("native registration root mapping changed"))
	}
	return ctx.Err()
}

func (p *nativeRegistrationCustody) checkDirectory(ctx context.Context) error {
	if err := p.checkRoot(ctx); err != nil {
		return err
	}
	if !p.created || p.directory == nil {
		return errors.New("native registration has no original created directory")
	}
	fd := int(p.directory.Fd())
	retained, err := endpointID(fd, "")
	if err != nil || !p.trustedDirectory(retained) || !p.directoryID.sameDirectory(retained) {
		return errors.New("retained native registration directory changed")
	}
	named, err := endpointID(int(p.root.Fd()), p.identity.Incarnation)
	if err != nil || !p.directoryID.sameDirectory(named) {
		return errors.New("native registration directory mapping changed")
	}
	return nil
}

func (p *nativeRegistrationCustody) check(ctx context.Context) error {
	if p.failure != nil {
		return p.failure
	}
	if err := p.checkDirectory(ctx); err != nil {
		return err
	}
	listing, err := openEndpointDirectory(int(p.directory.Fd()), ".", true)
	if err != nil {
		return err
	}
	names, readErr := listing.Readdirnames(2)
	closeErr := listing.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !p.publishTried {
		if len(names) != 0 {
			return errors.New("fresh native registration has unexpected members")
		}
		return nil
	}
	name := p.staging
	if p.published {
		name = "identity.json"
	}
	if p.file == nil || name == "" || len(names) != 1 || names[0] != name {
		return errors.New("native identity publication membership changed")
	}
	before, err := endpointID(int(p.file.Fd()), "")
	if err != nil || before != p.fileID || before.mode&unix.S_IFMT != unix.S_IFREG || before.mode&0o7777 != 0o644 || before.uid != p.uid || before.gid != p.gid || before.links != 1 || before.mount != p.directoryID.mount {
		return errors.New("original native identity file changed")
	}
	named, err := endpointID(int(p.directory.Fd()), name)
	if err != nil || named != before {
		return errors.New("native identity name no longer maps to original file")
	}
	buf := make([]byte, len(p.raw)+1)
	n, readErr := p.file.ReadAt(buf, 0)
	if n != len(p.raw) || !errors.Is(readErr, io.EOF) || !bytes.Equal(buf[:n], p.raw) {
		return errors.New("original native identity bytes changed")
	}
	after, err := endpointID(int(p.file.Fd()), "")
	if err != nil || after != before {
		return errors.New("native identity changed during read")
	}
	return p.checkDirectory(ctx)
}

// publishIdentity retains the original exclusive staging FD across the actual
// no-replace rename and directory fsync. Matching bytes reopened by name never
// become an original publication ACK. Failure is sticky and leaves exact
// original partial state for the original receipt/owner, with no cleanup/retry.
func (p *nativeRegistrationCustody) publishIdentity(ctx context.Context) (err error) {
	if p == nil {
		return errors.New("native registration custody unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(ctx); err != nil {
		p.failure = err
		return err
	}
	if p.publishTried {
		return errors.New("native identity publication was already attempted")
	}
	p.publishTried = true
	defer func() {
		if err != nil {
			p.failure = err
		}
	}()
	p.raw, err = json.Marshal(p.identity)
	if err != nil || len(p.raw) > 4096 {
		return errors.Join(err, errors.New("native identity publication exceeds bound"))
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	p.staging = ".write-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(int(p.directory.Fd()), p.staging, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	p.file = os.NewFile(uintptr(fd), "original native operator identity")
	original, err := endpointID(fd, "")
	if err != nil || original.mode&unix.S_IFMT != unix.S_IFREG || original.links != 1 || original.uid != p.uid || original.gid != p.gid || original.mount != p.directoryID.mount {
		return errors.Join(err, errors.New("original staging file acquisition changed"))
	}
	if n, err := p.file.Write(p.raw); err != nil || n != len(p.raw) {
		return errors.Join(err, io.ErrShortWrite)
	}
	if err := p.file.Chmod(0o644); err != nil {
		return err
	}
	if err := p.file.Sync(); err != nil {
		return err
	}
	p.fileID, err = endpointID(fd, "")
	if err != nil || !sameNativePublicationFile(original, p.fileID) {
		return errors.Join(err, errors.New("original staging file changed during preparation"))
	}
	if err := p.check(ctx); err != nil {
		return err
	}
	if err := p.rename(int(p.directory.Fd()), p.staging, "identity.json"); err != nil {
		return err
	}
	p.published = true
	p.staging = ""
	// Refresh ONLY this continuously retained original inode after our own
	// acknowledged rename. Never refresh it from a matching pathname object.
	p.fileID, err = endpointID(fd, "")
	if err != nil || !sameNativePublicationFile(original, p.fileID) {
		return errors.Join(err, errors.New("original identity file changed across publication"))
	}
	if err := p.directory.Sync(); err != nil {
		return err
	}
	return p.check(ctx)
}

func (p *nativeRegistrationCustody) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("native registration custody unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.check(ctx)
	if err != nil {
		p.failure = err
	}
	return err
}

func sameNativePublicationFile(a, b endpointFileID) bool {
	return a.dev == b.dev && a.ino == b.ino && a.mount == b.mount && a.uid == b.uid && a.gid == b.gid && a.links == 1 && b.links == 1
}

func (p *nativeRegistrationCustody) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	for _, file := range []*os.File{p.file, p.directory, p.root} {
		if file != nil {
			p.closeErr = errors.Join(p.closeErr, file.Close())
		}
	}
	p.file, p.directory, p.root = nil, nil, nil
	return p.closeErr // never unlink, reconstruct a cleanup ACK or clear failure
}
