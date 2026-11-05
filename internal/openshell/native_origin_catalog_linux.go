package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/leogr/yoloctl/internal/layout"
	"golang.org/x/sys/unix"
)

const nativeOriginCatalogLimit = 1024
const nativeOriginBytesLimit = 4096
const nativeOriginReadBudget = 10 * time.Second

type nativeOriginCatalogSelection struct{ space, diskID, diskUUID string }
type nativeOriginCatalogEntry struct {
	origin              containerOrigin
	raw                 []byte
	directory, file     *os.File // own current original observation, NOT creator custody
	directoryID, fileID pinFileIdentity
}

// PRIMARY NONSECRET METADATA OBSERVATION ONLY. This catalog never constructs a
// NativeReservation, NativePrecreateRead, receipt, cold restoration or ACK.
// Empty origins do NOT mean total native absence: namespace-pins, registration,
// runtime/CID/projection, partial state and receipt scopes remain independent.
type nativeOriginCatalog struct {
	mu                   sync.Mutex
	rootPath             string
	root, collection     *os.File
	rootID, collectionID pinFileIdentity
	selection            nativeOriginCatalogSelection
	uid, gid             int
	verifyCurrent        func(context.Context) error
	validateHistorical   func(containerOrigin) error
	names                []string
	entries              []nativeOriginCatalogEntry
	complete, closed     bool
	failure, closeErr    error
}

func (*nativeOriginCatalog) String() string     { return "native origin catalog [metadata only]" }
func (p *nativeOriginCatalog) GoString() string { return p.String() }

// Private ordinary-filesystem seam. A future native wrapper must derive root
// only from layout.OpenShellControlDir and bind selection/current storage from
// the original admitted parent. Historical validation MUST qualify the recorded
// pair; never substitute the current helper/source hash. This first slice
// requires an EXISTING protected control/containers root. ENOENT is an error,
// never fabricated absence; that needs a separate parent-anchored observation.
func observeNativeOriginCatalogAt(ctx context.Context, rootPath string, selection nativeOriginCatalogSelection, uid, gid int, verifyCurrent func(context.Context) error, validateHistorical func(containerOrigin) error) (_ *nativeOriginCatalog, err error) {
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if uid < 0 || gid < 0 || verifyCurrent == nil || validateHistorical == nil {
		return nil, errors.New("native origin catalog lacks independently selected scope/current/historical readers")
	}
	if err := selection.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyCurrent(ctx); err != nil {
		return nil, err
	}
	p := &nativeOriginCatalog{rootPath: rootPath, selection: selection, uid: uid, gid: gid, verifyCurrent: verifyCurrent, validateHistorical: validateHistorical}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	p.root, p.rootID, err = openPrecreateControl(rootPath, uid, gid)
	if err != nil {
		return nil, err
	}
	p.collection, p.collectionID, err = openPinDirectory(int(p.root.Fd()), "containers", false, uid, gid)
	if err != nil {
		return nil, err
	}
	if p.collectionID.device != p.rootID.device || p.collectionID.mount != p.rootID.mount {
		return nil, errors.New("native origin collection leaves selected control filesystem")
	}
	p.names, err = nativeOriginCatalogNames(ctx, p.collection)
	if err != nil {
		return nil, err
	}
	for _, name := range p.names {
		// Retain every partial owned handle before the next fallible operation.
		p.entries = append(p.entries, nativeOriginCatalogEntry{})
		entry := &p.entries[len(p.entries)-1]
		entry.directory, entry.directoryID, err = openPinDirectory(int(p.collection.Fd()), name, false, uid, gid)
		if err != nil {
			return nil, err
		}
		if entry.directoryID.device != p.rootID.device || entry.directoryID.mount != p.rootID.mount {
			return nil, errors.New("native incarnation leaves selected control filesystem")
		}
		fd, openErr := unix.Openat2(int(entry.directory.Fd()), "origin.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
		if openErr != nil {
			return nil, openErr
		} // no origin is UNKNOWN, not legacy
		entry.file = os.NewFile(uintptr(fd), "native-origin-observation")
		entry.fileID, err = pinIdentity(fd, uid, gid, false)
		if err != nil {
			return nil, err
		}
		if entry.fileID.device != p.rootID.device || entry.fileID.mount != p.rootID.mount {
			return nil, errors.New("native origin leaves selected control filesystem")
		}
		entry.raw, err = readNativeCatalogOrigin(entry.file)
		if err != nil {
			return nil, err
		}
		entry.origin, err = decodeNativeCatalogOrigin(entry.raw, name, selection)
		if err != nil {
			return nil, err
		}
		if err := validateHistorical(entry.origin); err != nil {
			return nil, err
		}
	}
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	p.complete = true
	return p, nil
}

func (s nativeOriginCatalogSelection) validate() error {
	if layout.ValidateExistingSpaceName(s.space) != nil {
		return errors.New("native origin catalog has invalid selected space")
	}
	for _, token := range []string{s.diskID, s.diskUUID} {
		if token == "" || len(token) > 256 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return errors.New("native origin catalog has invalid selected disk identity")
		}
	}
	return nil
}

func decodeNativeCatalogOrigin(raw []byte, name string, selected nativeOriginCatalogSelection) (containerOrigin, error) {
	fail := func() (containerOrigin, error) {
		return containerOrigin{}, errors.New("native origin is missing/partial/unknown or differs from selected native scope")
	}
	if selected.validate() != nil || len(raw) == 0 || len(raw) > nativeOriginBytesLimit || !lowerHex(name, 16) {
		return fail()
	}
	var origin containerOrigin
	if json.Unmarshal(raw, &origin) != nil {
		return fail()
	}
	// The existing producer writes these exact seven fields via json.Marshal.
	// Canonical byte equality closes duplicate/folded/unknown/null/trailing
	// representations without rewriting any retained origin to make it match.
	canonical, err := json.Marshal(origin)
	if err != nil || !bytes.Equal(raw, canonical) {
		return fail()
	}
	if origin.Space != selected.space || origin.DiskID != selected.diskID || origin.DiskUUID != selected.diskUUID || origin.Incarnation != name || layout.ValidateName("agentbox", origin.Box) != nil || !lowerHex(origin.Qualification, 32) || !lowerHex(origin.HelperSHA256, 32) {
		return fail()
	}
	return origin, nil // supported historical pair is checked independently
}

func readNativeCatalogOrigin(file *os.File) ([]byte, error) {
	if file == nil {
		return nil, errors.New("native origin observation lost original file")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, nativeOriginBytesLimit+1))
	if err != nil || len(raw) == 0 || len(raw) > nativeOriginBytesLimit {
		return nil, errors.Join(errors.New("native origin read is empty/unbounded"), err)
	}
	return raw, nil
}

func nativeOriginCatalogNames(ctx context.Context, directory *os.File) ([]string, error) {
	if directory == nil {
		return nil, errors.New("native origin collection unavailable")
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	names := []string{}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, readErr := directory.Readdirnames(128)
		for _, name := range batch {
			if !lowerHex(name, 16) || seen[name] || len(names) >= nativeOriginCatalogLimit {
				return nil, errors.New("native collection has unknown/repeated/unbounded incarnation fragments")
			}
			seen[name] = true
			names = append(names, name)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	slices.Sort(names)
	return names, nil
}

func (p *nativeOriginCatalog) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil native origin catalog")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.complete || p.failure != nil {
		return errors.Join(errors.New("native origin catalog closed/incomplete/failed"), p.failure)
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p.failure = p.check(ctx)
	return p.failure
}

func (p *nativeOriginCatalog) check(ctx context.Context) error {
	if p == nil || p.closed || p.root == nil || p.collection == nil || p.verifyCurrent == nil || p.validateHistorical == nil {
		return errors.New("native origin catalog lacks original handles/readers")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := p.checkDirectories(); err != nil {
		return err
	}
	names, err := nativeOriginCatalogNames(ctx, p.collection)
	if err != nil || !slices.Equal(names, p.names) || len(p.entries) != len(p.names) {
		return errors.Join(errors.New("native origin collection changed/incomplete"), err)
	}
	for index := range p.entries {
		entry := &p.entries[index]
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.file == nil || entry.directory == nil || entry.origin.Incarnation != p.names[index] {
			return errors.New("native origin catalog lost original member")
		}
		if err := p.checkMemberDirectory(entry); err != nil {
			return err
		}
		before, err := pinIdentity(int(entry.file.Fd()), p.uid, p.gid, false)
		if err != nil || before != entry.fileID {
			return errors.Join(errors.New("native retained origin identity changed"), err)
		}
		raw, err := readNativeCatalogOrigin(entry.file)
		if err != nil || !bytes.Equal(raw, entry.raw) {
			return errors.Join(errors.New("native retained origin bytes changed"), err)
		}
		if _, err := decodeNativeCatalogOrigin(raw, entry.origin.Incarnation, p.selection); err != nil {
			return err
		}
		if err := p.validateHistorical(entry.origin); err != nil {
			return err
		}
		after, err := pinIdentity(int(entry.file.Fd()), p.uid, p.gid, false)
		if err != nil || after != before {
			return errors.Join(errors.New("native origin changed across read"), err)
		}
		fd, err := unix.Openat2(int(entry.directory.Fd()), "origin.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
		if err != nil {
			return err
		}
		named, identityErr := pinIdentity(fd, p.uid, p.gid, false)
		closeErr := unix.Close(fd)
		if identityErr != nil || closeErr != nil || named != entry.fileID {
			return errors.Join(errors.New("native named origin replaced"), identityErr, closeErr)
		}
		if err := p.checkMemberDirectory(entry); err != nil {
			return err
		}
	}
	names, err = nativeOriginCatalogNames(ctx, p.collection)
	if err != nil || !slices.Equal(names, p.names) {
		return errors.Join(errors.New("native origin membership changed across reads"), err)
	}
	if err := p.checkDirectories(); err != nil {
		return err
	}
	if err := p.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// External verification/history work must not leave a same-name member
	// replacement hidden behind the retained original directory descriptors.
	for index := range p.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.checkMemberDirectory(&p.entries[index]); err != nil {
			return err
		}
	}
	names, err = nativeOriginCatalogNames(ctx, p.collection)
	if err != nil || !slices.Equal(names, p.names) {
		return errors.Join(errors.New("native origin membership changed at completion"), err)
	}
	if err := p.checkDirectories(); err != nil {
		return err
	}
	return ctx.Err()
}

func (p *nativeOriginCatalog) checkMemberDirectory(entry *nativeOriginCatalogEntry) error {
	if entry == nil || entry.directory == nil {
		return errors.New("native origin member unavailable")
	}
	id, err := pinIdentity(int(entry.directory.Fd()), p.uid, p.gid, true)
	if err != nil || id != entry.directoryID {
		return errors.Join(errors.New("native original incarnation changed"), err)
	}
	directory, namedID, err := openPinDirectory(int(p.collection.Fd()), entry.origin.Incarnation, false, p.uid, p.gid)
	if err != nil {
		return err
	}
	closeErr := directory.Close()
	if closeErr != nil || namedID != entry.directoryID {
		return errors.Join(errors.New("native named incarnation changed"), closeErr)
	}
	return nil
}

func (p *nativeOriginCatalog) checkDirectories() error {
	actual, err := pinIdentity(int(p.root.Fd()), p.uid, p.gid, true)
	if err != nil || actual != p.rootID {
		return errors.Join(errors.New("native retained control root changed"), err)
	}
	current, id, err := openPrecreateControl(p.rootPath, p.uid, p.gid)
	if err != nil {
		return err
	}
	closeErr := current.Close()
	if closeErr != nil || id != p.rootID {
		return errors.Join(errors.New("native named control root changed"), closeErr)
	}
	actual, err = pinIdentity(int(p.collection.Fd()), p.uid, p.gid, true)
	if err != nil || actual != p.collectionID {
		return errors.Join(errors.New("native retained collection changed"), err)
	}
	current, id, err = openPinDirectory(int(p.root.Fd()), "containers", false, p.uid, p.gid)
	if err != nil {
		return err
	}
	closeErr = current.Close()
	if closeErr != nil || id != p.collectionID {
		return errors.Join(errors.New("native named collection changed"), closeErr)
	}
	return nil
}

// No keep argument: the classifier must see every same-box incarnation,
// including the exact current label/keep. Copied strings are NOT authority.
func (p *nativeOriginCatalog) ForBox(box string) ([]containerOrigin, error) {
	if p == nil {
		return nil, errors.New("nil native origin catalog")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.complete || p.failure != nil || layout.ValidateName("agentbox", box) != nil {
		return nil, errors.Join(errors.New("native origin catalog unavailable or invalid box"), p.failure)
	}
	result := []containerOrigin{}
	for _, entry := range p.entries {
		if entry.origin.Box == box {
			result = append(result, entry.origin)
		}
	}
	return result, nil
}

func (p *nativeOriginCatalog) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed, p.complete = true, false
	for i := range p.entries {
		for _, file := range []*os.File{p.entries[i].file, p.entries[i].directory} {
			if file != nil {
				p.closeErr = errors.Join(p.closeErr, file.Close())
			}
		}
		p.entries[i].file, p.entries[i].directory = nil, nil
		p.entries[i].raw = nil
	}
	for _, file := range []*os.File{p.collection, p.root} {
		if file != nil {
			p.closeErr = errors.Join(p.closeErr, file.Close())
		}
	}
	p.collection, p.root = nil, nil
	return p.closeErr
}
