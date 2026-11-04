package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"sync"

	"golang.org/x/sys/unix"
)

const nativeOriginRecordLimit = 1024
const nativeOriginRecordBytes = 1 << 20
const nativeOriginRecordTotalBytes = 32 << 20

type nativeOriginRecordFile struct {
	file     *os.File
	identity pinFileIdentity
	raw      []byte
	record   RuntimeRecord
}
type nativeOriginRecordGeneration struct {
	name      string
	directory *os.File
	identity  pinFileIdentity
	value     nativeOriginRecordFile
}

// Immutable record PRIMARY observation only, NOT full runtime/private-file
// schema validation, original creator/ACK, namespace/current-reference proof or
// classifier. Borrowed original catalog handles are NEVER closed here.
type nativeOriginRecords struct {
	mu                      sync.Mutex
	source                  *nativeOriginScope
	catalog                 *nativeOriginCatalog
	entryIndex              int
	entry                   nativeOriginCatalogEntry // copied origin/raw+IDs; directory/file BORROWED
	lifetime                context.Context
	resolve                 func(containerOrigin) (QualifiedRelease, error)
	release                 QualifiedRelease
	uid, gid                int
	rootReader, generations *os.File // OWN distinct directory descriptions
	generationsID           pinFileIdentity
	current                 nativeOriginRecordFile // OWN current file
	names                   []string
	values                  []nativeOriginRecordGeneration // OWN generation dirs/record files
	active, closed          bool
	failure, closeErr       error
}

func (*nativeOriginRecords) String() string {
	return "native immutable record observations [not authority]"
}
func (r *nativeOriginRecords) GoString() string { return r.String() }

// Capture returns ONLY a private owned observer. The enclosing composition
// must keep it alive through ALL external verifiers and final local record
// checks, then close child observers BEFORE original source expiry. Do not hide
// it in a nested callback whose Close precedes an outer verifier/finalizer.
func captureNativeOriginRecords(ctx context.Context, source *nativeOriginScope, incarnation string, resolve func(containerOrigin) (QualifiedRelease, error)) (_ *nativeOriginRecords, err error) {
	if ctx == nil || source == nil || resolve == nil || !lowerHex(incarnation, 16) {
		return nil, errors.New("record capture lacks original scope/incarnation/resolver")
	}
	lifetime := ctx
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if err := source.Check(ctx); err != nil {
		return nil, err
	}
	// Copy ONLY immutable metadata under the source lifetime lock. External
	// resolver runs after release, and exact entry rebind follows before I/O.
	source.mu.Lock()
	if err := source.usable(); err != nil {
		source.mu.Unlock()
		return nil, err
	}
	c := source.catalog
	if c == nil {
		source.mu.Unlock()
		return nil, errors.New("record source prefix/catalog unavailable")
	}
	c.mu.Lock()
	if c.closed || !c.complete || c.failure != nil {
		failure := errors.Join(errors.New("original catalog unavailable before resolver"), c.failure)
		c.mu.Unlock()
		source.mu.Unlock()
		return nil, failure
	}
	index := -1
	var entry nativeOriginCatalogEntry
	for i, e := range c.entries {
		if e.origin.Incarnation == incarnation {
			index = i
			entry = e
			entry.raw = bytes.Clone(e.raw)
			break
		}
	}
	uid, gid := c.uid, c.gid
	c.mu.Unlock()
	source.mu.Unlock()
	if index < 0 {
		return nil, errors.New("incarnation not in complete ORIGINAL catalog")
	}
	release, err := resolve(entry.origin)
	if err != nil {
		return nil, err
	}
	if err := nativeOriginRecordRelease(entry.origin, release); err != nil {
		return nil, err
	}
	if err := source.Check(ctx); err != nil {
		return nil, err
	}
	r := &nativeOriginRecords{source: source, catalog: c, entryIndex: index, entry: entry, lifetime: lifetime, resolve: resolve, release: release, uid: uid, gid: gid}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	source.mu.Lock()
	defer source.mu.Unlock() // protects ALL borrowed handle accesses
	if err := r.sourceMappings(ctx); err != nil {
		return nil, err
	}
	r.rootReader, err = nativeOriginRecordOpenDirectory(entry.directory, ".")
	if err != nil {
		return nil, err
	}
	id, err := nativeOriginRecordID(r.rootReader, uid, gid, true)
	if err != nil || id != entry.directoryID {
		return nil, errors.Join(errors.New("original record enumeration root changed"), err)
	}
	if err := nativeOriginRecordRootNames(ctx, r.rootReader); err != nil {
		return nil, err
	}
	r.current.file, err = nativeOriginRecordOpenFile(entry.directory, "current.json")
	if err != nil {
		return nil, err
	}
	r.current.identity, err = nativeOriginRecordID(r.current.file, uid, gid, false)
	if err != nil {
		return nil, err
	}
	remaining := nativeOriginRecordTotalBytes
	r.current.raw, err = nativeOriginRecordRead(ctx, r.current.file, &remaining)
	if err != nil {
		return nil, err
	}
	r.current.record, err = decodeNativeOriginRuntimeRecord(r.current.raw, r.entry.origin, release)
	if err != nil {
		return nil, err
	}
	r.generations, err = nativeOriginRecordOpenDirectory(entry.directory, "generations")
	if err != nil {
		return nil, err
	}
	r.generationsID, err = nativeOriginRecordID(r.generations, uid, gid, true)
	if err != nil {
		return nil, err
	}
	r.names, err = nativeOriginRecordNames(ctx, r.generations)
	if err != nil {
		return nil, err
	}
	if len(r.names) == 0 {
		return nil, errors.New("record source has no immutable generations")
	}
	selected := false
	for _, name := range r.names {
		r.values = append(r.values, nativeOriginRecordGeneration{name: name})
		value := &r.values[len(r.values)-1]
		value.directory, err = nativeOriginRecordOpenDirectory(r.generations, name)
		if err != nil {
			return nil, err
		}
		value.identity, err = nativeOriginRecordID(value.directory, uid, gid, true)
		if err != nil {
			return nil, err
		}
		value.value.file, err = nativeOriginRecordOpenFile(value.directory, "record.json")
		if err != nil {
			return nil, err
		}
		value.value.identity, err = nativeOriginRecordID(value.value.file, uid, gid, false)
		if err != nil {
			return nil, err
		}
		value.value.raw, err = nativeOriginRecordRead(ctx, value.value.file, &remaining)
		if err != nil {
			return nil, err
		}
		value.value.record, err = decodeNativeOriginRuntimeRecord(value.value.raw, r.entry.origin, release)
		if err != nil {
			return nil, err
		}
		if value.value.record.Generation != name || !nativeOriginRecordIntent(r.current.record, value.value.record) {
			return nil, errors.New("record generation/name/immutable intent differs")
		}
		if name == r.current.record.Generation {
			selected = bytes.Equal(value.value.raw, r.current.raw)
		}
	}
	if !selected {
		return nil, errors.New("complete generation roster omits exact current record")
	}
	r.active = true
	if err := r.checkLocal(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func nativeOriginRecordRelease(origin containerOrigin, release QualifiedRelease) error {
	if release.Identity() == "" || release.Identity() != origin.Qualification {
		return errors.New("historical release differs from ORIGINAL qualification")
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil || helper.BinarySHA256 != origin.HelperSHA256 {
		return errors.Join(errors.New("historical helper pair differs from ORIGINAL recorded helper"), err)
	}
	return nil // NO ValidateHelperSource, inspected HelperLaunch or protocol fabrication
}
func decodeNativeOriginRuntimeRecord(raw []byte, origin containerOrigin, release QualifiedRelease) (RuntimeRecord, error) {
	var record RuntimeRecord
	if len(raw) == 0 || len(raw) > nativeOriginRecordBytes || json.Unmarshal(raw, &record) != nil {
		return RuntimeRecord{}, errors.New("missing/unbounded/invalid runtime record")
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return RuntimeRecord{}, errors.New("runtime record not exact closed canonical producer encoding")
	}
	if err := nativeOriginRecordRelease(origin, release); err != nil {
		return RuntimeRecord{}, err
	}
	if err := record.Validate(release, origin.HelperSHA256); err != nil {
		return RuntimeRecord{}, err
	}
	if generationOrigin(record) != origin {
		return RuntimeRecord{}, errors.New("runtime record differs from ORIGINAL seven-field origin")
	}
	return record, nil
}
func nativeOriginRecordIntent(a, b RuntimeRecord) bool {
	return generationOrigin(a) == generationOrigin(b) && a.Launch.UID == b.Launch.UID && a.Launch.GID == b.Launch.GID && a.Launch.Workdir == b.Launch.Workdir && a.Launch.WorkspaceReadOnly == b.Launch.WorkspaceReadOnly && a.Launch.UnconfinedNetwork == b.Launch.UnconfinedNetwork
}
func nativeOriginRecordID(file *os.File, uid, gid int, directory bool) (pinFileIdentity, error) {
	if file == nil {
		return pinFileIdentity{}, errors.New("missing record descriptor")
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return pinFileIdentity{}, errors.Join(errors.New("record descriptor not close-on-exec"), err)
	}
	return pinIdentity(int(file.Fd()), uid, gid, directory)
}
func nativeOriginRecordOpenDirectory(parent *os.File, name string) (*os.File, error) {
	if parent == nil || name != "." && name != "generations" && !namespacePinGenerationName(name) {
		return nil, errors.New("invalid original record directory component")
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "native-record-directory"), nil
}
func nativeOriginRecordOpenFile(parent *os.File, name string) (*os.File, error) {
	if parent == nil || name != "current.json" && name != "record.json" && name != "origin.json" {
		return nil, errors.New("invalid original record file component")
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "native-record-file"), nil
}
func nativeOriginRecordRead(ctx context.Context, file *os.File, remaining *int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file == nil || remaining == nil || *remaining <= 0 {
		return nil, errors.New("record read/budget unavailable")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(min(nativeOriginRecordBytes, *remaining))+1))
	if err != nil || len(raw) == 0 || len(raw) > nativeOriginRecordBytes || len(raw) > *remaining {
		return nil, errors.Join(errors.New("record read empty/unbounded"), err)
	}
	*remaining -= len(raw)
	return raw, ctx.Err()
}
func nativeOriginRecordNames(ctx context.Context, directory *os.File) ([]string, error) {
	names, err := nativeOriginRecordDirectoryNames(ctx, directory)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !namespacePinGenerationName(name) {
			return nil, errors.New("unknown/orphan generation directory name")
		}
	}
	return names, nil
}
func nativeOriginRecordRootNames(ctx context.Context, directory *os.File) error {
	names, err := nativeOriginRecordDirectoryNames(ctx, directory)
	if err != nil {
		return err
	}
	if !slices.Equal(names, []string{"current.json", "generations", "origin.json"}) {
		return errors.New("partial/unknown immutable record root members")
	}
	return nil
}
func nativeOriginRecordDirectoryNames(ctx context.Context, directory *os.File) ([]string, error) {
	if directory == nil {
		return nil, errors.New("record enumeration unavailable")
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
			if name == "" || seen[name] || len(names) >= nativeOriginRecordLimit {
				return nil, errors.New("repeated/unbounded record roster")
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

func (r *nativeOriginRecords) usable() error {
	if r == nil || !r.active || r.closed || r.source == nil || r.catalog == nil || r.lifetime == nil {
		return errors.New("record observer expired/incomplete")
	}
	if r.failure != nil {
		return r.failure
	}
	if err := r.lifetime.Err(); err != nil {
		return errors.Join(errors.New("original record observer execution expired"), err)
	}
	return nil
}
func (r *nativeOriginRecords) Check(ctx context.Context) error {
	if r == nil {
		return errors.New("nil record observer")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.check(ctx)
	if err != nil {
		r.failure = err
	}
	return err
}
func (r *nativeOriginRecords) check(ctx context.Context) error {
	if err := r.usable(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("record check context unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	// All external work BEFORE the protected local immutable-record pass.
	if err := r.source.Check(ctx); err != nil {
		return err
	}
	release, err := r.resolve(r.entry.origin)
	if err != nil {
		return err
	}
	if err := nativeOriginRecordRelease(r.entry.origin, release); err != nil {
		return err
	}
	if !bytes.Equal(release.Manifest(), r.release.Manifest()) {
		return errors.New("historical resolver replaced ORIGINAL manifest")
	}
	if err := r.source.Check(ctx); err != nil {
		return err
	}
	r.source.mu.Lock()
	defer r.source.mu.Unlock()
	return r.checkLocal(ctx) // holds source lifetime through every borrowed access
}
func (r *nativeOriginRecords) checkLocal(ctx context.Context) error {
	if err := r.usable(); err != nil {
		return err
	}
	if err := r.sourceMappings(ctx); err != nil {
		return err
	}
	for pass := 0; pass < 2; pass++ {
		if err := r.checkDirectory(r.entry.directory, ".", r.rootReader, r.entry.directoryID); err != nil {
			return err
		}
		if err := nativeOriginRecordRootNames(ctx, r.rootReader); err != nil {
			return err
		}
		names, err := nativeOriginRecordNames(ctx, r.generations)
		if err != nil || !slices.Equal(names, r.names) {
			return errors.Join(errors.New("original record generation roster changed"), err)
		}
		remaining := 2 * nativeOriginRecordTotalBytes // bounded repeated byte reads; stored bytes remain32MiB
		if err := r.checkFile(ctx, r.entry.directory, "current.json", &r.current, &remaining); err != nil {
			return err
		}
		for i := range r.values {
			value := &r.values[i]
			if err := r.checkDirectory(r.generations, value.name, value.directory, value.identity); err != nil {
				return err
			}
			if err := r.checkFile(ctx, value.directory, "record.json", &value.value, &remaining); err != nil {
				return err
			}
			if err := r.checkDirectory(r.generations, value.name, value.directory, value.identity); err != nil {
				return err
			}
		}
		names, err = nativeOriginRecordNames(ctx, r.generations)
		if err != nil || !slices.Equal(names, r.names) {
			return errors.Join(errors.New("original record roster changed across bytes"), err)
		}
		if err := r.checkFile(ctx, r.entry.directory, "current.json", &r.current, &remaining); err != nil {
			return err
		}
		if err := nativeOriginRecordRootNames(ctx, r.rootReader); err != nil {
			return err
		}
		if err := r.checkDirectory(r.entry.directory, ".", r.rootReader, r.entry.directoryID); err != nil {
			return err
		}
		if err := r.checkDirectory(r.entry.directory, "generations", r.generations, r.generationsID); err != nil {
			return err
		}
		if err := r.sourceMappings(ctx); err != nil {
			return err
		}
	}
	if err := r.usable(); err != nil {
		return err
	}
	return ctx.Err()
}
func (r *nativeOriginRecords) checkFile(ctx context.Context, parent *os.File, name string, value *nativeOriginRecordFile, remaining *int) error {
	before, err := nativeOriginRecordID(value.file, r.uid, r.gid, false)
	if err != nil || before != value.identity {
		return errors.Join(errors.New("original record file handle changed"), err)
	}
	raw, err := nativeOriginRecordRead(ctx, value.file, remaining)
	if err != nil || !bytes.Equal(raw, value.raw) {
		return errors.Join(errors.New("original record bytes changed"), err)
	}
	if _, err := decodeNativeOriginRuntimeRecord(raw, r.entry.origin, r.release); err != nil {
		return err
	}
	named, err := nativeOriginRecordOpenFile(parent, name)
	if err != nil {
		return err
	}
	id, identityErr := nativeOriginRecordID(named, r.uid, r.gid, false)
	closeErr := named.Close()
	after, err := nativeOriginRecordID(value.file, r.uid, r.gid, false)
	if identityErr != nil || closeErr != nil || err != nil || id != value.identity || after != before {
		return errors.Join(errors.New("named/original record mapping changed"), identityErr, closeErr, err)
	}
	return nil
}
func (r *nativeOriginRecords) checkDirectory(parent *os.File, name string, original *os.File, want pinFileIdentity) error {
	id, err := nativeOriginRecordID(original, r.uid, r.gid, true)
	if err != nil || id != want {
		return errors.Join(errors.New("original record directory changed"), err)
	}
	named, err := nativeOriginRecordOpenDirectory(parent, name)
	if err != nil {
		return err
	}
	actual, identityErr := nativeOriginRecordID(named, r.uid, r.gid, true)
	closeErr := named.Close()
	if identityErr != nil || closeErr != nil || actual != want {
		return errors.Join(errors.New("named record directory changed"), identityErr, closeErr)
	}
	return nil
}

// Caller holds source.mu; no source.Check/external callback is invoked here.
func (r *nativeOriginRecords) sourceMappings(ctx context.Context) error {
	s := r.source
	if err := s.usable(); err != nil {
		return err
	}
	if s.catalog != r.catalog {
		return errors.New("ORIGINAL catalog object replaced")
	}
	if err := s.finalAncestryMappings(ctx); err != nil {
		return err
	}
	c := r.catalog
	c.mu.Lock()
	err := func() error {
		if c.closed || !c.complete || c.failure != nil || c.uid != r.uid || c.gid != r.gid || r.entryIndex < 0 || r.entryIndex >= len(c.entries) {
			return errors.Join(errors.New("original record catalog entry expired"), c.failure)
		}
		entry := &c.entries[r.entryIndex]
		if entry.directory != r.entry.directory || entry.file != r.entry.file || entry.origin != r.entry.origin || entry.directoryID != r.entry.directoryID || entry.fileID != r.entry.fileID || !bytes.Equal(entry.raw, r.entry.raw) {
			return errors.New("ORIGINAL borrowed entry replaced")
		}
		if err := c.checkDirectories(); err != nil {
			return err
		}
		if err := c.checkMemberDirectory(entry); err != nil {
			return err
		}
		before, err := nativeOriginRecordID(entry.file, r.uid, r.gid, false)
		if err != nil || before != entry.fileID {
			return errors.Join(errors.New("borrowed origin handle changed"), err)
		}
		raw, err := readNativeCatalogOrigin(entry.file)
		if err != nil || !bytes.Equal(raw, r.entry.raw) {
			return errors.Join(errors.New("borrowed original origin bytes changed"), err)
		}
		named, err := nativeOriginRecordOpenFile(entry.directory, "origin.json")
		if err != nil {
			return err
		}
		id, identityErr := nativeOriginRecordID(named, r.uid, r.gid, false)
		closeErr := named.Close()
		after, err := nativeOriginRecordID(entry.file, r.uid, r.gid, false)
		if identityErr != nil || closeErr != nil || err != nil || id != entry.fileID || after != before {
			return errors.Join(errors.New("borrowed origin named/original identity changed"), identityErr, closeErr, err)
		}
		if err := c.checkMemberDirectory(entry); err != nil {
			return err
		}
		return c.checkDirectories()
	}()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return s.finalAncestryMappings(ctx)
}
func (r *nativeOriginRecords) Records(ctx context.Context) (RuntimeRecord, []RuntimeRecord, error) {
	if r == nil {
		return RuntimeRecord{}, nil, errors.New("nil record observer")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.check(ctx); err != nil {
		r.failure = err
		return RuntimeRecord{}, nil, err
	}
	// Copy while the SAME original source remains live. No further external
	// callback or borrowed-FD access follows this source lifetime check.
	r.source.mu.Lock()
	defer r.source.mu.Unlock()
	if err := r.source.usable(); err != nil {
		r.failure = err
		return RuntimeRecord{}, nil, err
	}
	records := make([]RuntimeRecord, len(r.values))
	for i, value := range r.values {
		records[i] = cloneRuntimeRecord(value.value.record)
	}
	if err := r.usable(); err != nil {
		r.failure = err
		return RuntimeRecord{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		r.failure = err
		return RuntimeRecord{}, nil, err
	}
	return cloneRuntimeRecord(r.current.record), records, nil
}
func (r *nativeOriginRecords) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.active, r.closed = false, true
	for i := len(r.values) - 1; i >= 0; i-- {
		value := &r.values[i]
		for _, file := range []*os.File{value.value.file, value.directory} {
			if file != nil {
				r.closeErr = errors.Join(r.closeErr, file.Close())
			}
		}
		value.value.file, value.directory = nil, nil
		value.value.raw = nil
	}
	for _, file := range []*os.File{r.generations, r.current.file, r.rootReader} {
		if file != nil {
			r.closeErr = errors.Join(r.closeErr, file.Close())
		}
	}
	r.generations, r.current.file, r.rootReader = nil, nil, nil
	r.current.raw = nil
	r.entry.directory, r.entry.file = nil, nil // DROP borrowed pointers; NEVER Close
	return r.closeErr
}
