package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"golang.org/x/sys/unix"
)

const nativeHistoricalManifestLimit = 1 << 20

type nativeHistoricalReleaseIdentity struct {
	file     pinFileIdentity
	mode     uint32
	uid, gid uint32
	links    uint64
	size     int64 // regular manifest ONLY; directory growth is not source replacement
}
type nativeHistoricalReleaseNode struct {
	file      *os.File // OWNED, including each separately retained mounted ancestor
	name      string   // one fixed child; first name is fixed "/" in production
	directory bool
	identity  nativeHistoricalReleaseIdentity
}

// Primary installed DOCUMENT observation only, NOT independent launch,
// historical-operation, original publication/custody, receipt or ACK authority.
// The private caller must pass the EXACT original checked origin copy while
// its catalog dir/file remain retained, and retain this independent reader
// through aggregate finalization. No active-scope requirement/circular resolver.
// This object borrows only context/boundary, and closes no origin/scope FD.
type nativeHistoricalRelease struct {
	mu                sync.Mutex
	lifetime          context.Context
	origin            containerOrigin
	uid, gid          int
	verifyCurrent     func(context.Context) error // original external storage/owner callback
	checkBoundary     func() error                // pure original context/layout mapping, no external callback
	nodes             []nativeHistoricalReleaseNode
	raw               []byte
	release           QualifiedRelease
	complete, closed  bool
	failure, closeErr error
}

func (*nativeHistoricalRelease) String() string {
	return "historical qualification document [not authority]"
}
func (p *nativeHistoricalRelease) GoString() string { return p.String() }

// Called only by an actual private original-origin validation closure. Its
// boundary is the SAME original nativeOriginObservationBoundary, not a later
// Current lookup or a caller-selected path/hash. No initializer/download/source
// refresh is used. Actual original-entry binding/finalization remain the caller's
// responsibility; possession of this primary object grants no native route.
func captureNativeHistoricalRelease(ctx context.Context, boundary *nativeOriginObservationBoundary, origin containerOrigin) (*nativeHistoricalRelease, error) {
	if ctx == nil || boundary == nil {
		return nil, errors.New("historical qualification lacks original native boundary")
	}
	if err := boundary.check(); err != nil {
		return nil, err
	}
	current := boundary.snapshot
	if origin.Space != current.Space || origin.DiskID != current.DiskID || origin.DiskUUID != current.DiskUUID {
		return nil, errors.New("historical origin differs from original selected native disk")
	}
	return captureNativeHistoricalReleaseWith(ctx, origin, 0, 0, boundary.verify, boundary.check, func() ([]nativeHistoricalReleaseNode, error) {
		// RuntimeDirectory performs canonical recorded-qualification validation.
		if _, err := RuntimeDirectory(origin.Qualification); err != nil {
			return nil, err
		}
		return openNativeHistoricalReleaseChain("/", []string{"opt", "openshell", "runtime", origin.Qualification}, 0, 0)
	})
}

// Private ordinary-filesystem seam ONLY. No public root/path/opener/arch/hash
// parameter or native admission backdoor is introduced. The production wrapper
// supplies the fixed chain above and actual native boundary callbacks.
func captureNativeHistoricalReleaseWith(ctx context.Context, origin containerOrigin, uid, gid int, verifyCurrent func(context.Context) error, checkBoundary func() error, open func() ([]nativeHistoricalReleaseNode, error)) (_ *nativeHistoricalRelease, result error) {
	if ctx == nil || uid < 0 || gid < 0 || verifyCurrent == nil || checkBoundary == nil || open == nil {
		return nil, errors.New("historical qualification has incomplete original lifetime/readers")
	}
	if (nativeOriginCatalogSelection{origin.Space, origin.DiskID, origin.DiskUUID}).validate() != nil || layout.ValidateName("agentbox", origin.Box) != nil || !lowerHex(origin.Incarnation, 16) || !lowerHex(origin.Qualification, 32) || !lowerHex(origin.HelperSHA256, 32) {
		return nil, errors.New("historical qualification has invalid exact origin fields")
	}
	lifetime := ctx
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p := &nativeHistoricalRelease{lifetime: lifetime, origin: origin, uid: uid, gid: gid, verifyCurrent: verifyCurrent, checkBoundary: checkBoundary}
	defer func() {
		if result != nil {
			result = errors.Join(result, p.Close())
		}
	}()
	if err := p.usable(ctx); err != nil {
		return nil, err
	}
	if err := verifyCurrent(ctx); err != nil {
		return nil, err
	}
	if err := p.usable(ctx); err != nil {
		return nil, err
	}
	var err error
	p.nodes, err = open() // returns EVERY partially owned handle even on failure
	if err != nil {
		return nil, err
	}
	if len(p.nodes) < 2 || p.nodes[len(p.nodes)-1].directory {
		return nil, errors.New("historical qualification lacks retained original manifest")
	}
	file := p.nodes[len(p.nodes)-1].file
	p.raw, err = readNativeHistoricalManifest(file)
	if err != nil {
		return nil, err
	}
	p.release, err = validateNativeHistoricalManifest(p.raw, origin)
	if err != nil {
		return nil, err
	}
	p.complete = true
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// Matches actual installer modes: protected root-owned ancestors, published
// release dir0555, manifest0444/single-link. Mounts may cross at ANY ancestor:
// retain and compare each original mount identity, never require one filesystem
// for / and /opt or imply that a legitimate installed bind is forbidden.
// The fixture-only root parameter is always "/" in the production wrapper.
func openNativeHistoricalReleaseChain(root string, components []string, uid, gid int) (nodes []nativeHistoricalReleaseNode, result error) {
	openStep := func(parent int, name string, directory bool, resolve uint64) error {
		flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK)
		if directory {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat2(parent, name, &unix.OpenHow{Flags: flags, Resolve: resolve})
		if err != nil {
			return err
		}
		node := nativeHistoricalReleaseNode{file: os.NewFile(uintptr(fd), "historical-release-original"), name: name, directory: directory}
		nodes = append(nodes, node)
		id, err := nativeHistoricalReleaseID(node.file, uid, gid, directory)
		if err != nil {
			return err
		}
		nodes[len(nodes)-1].identity = id
		return nil
	}
	if err := openStep(unix.AT_FDCWD, root, true, unix.RESOLVE_NO_SYMLINKS); err != nil {
		return nodes, err
	}
	for _, name := range components {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
			return nodes, errors.New("invalid fixed historical ancestor")
		}
		if err := openStep(int(nodes[len(nodes)-1].file.Fd()), name, true, unix.RESOLVE_BENEATH|unix.RESOLVE_NO_SYMLINKS); err != nil {
			return nodes, err
		}
	}
	if nodes[len(nodes)-1].identity.mode&07777 != 0555 {
		return nodes, errors.New("historical released directory is not published0555")
	}
	if err := openStep(int(nodes[len(nodes)-1].file.Fd()), "qualification.json", false, unix.RESOLVE_BENEATH|unix.RESOLVE_NO_SYMLINKS); err != nil {
		return nodes, err
	}
	return nodes, nil
}
func nativeHistoricalReleaseID(file *os.File, uid, gid int, directory bool) (nativeHistoricalReleaseIdentity, error) {
	if file == nil {
		return nativeHistoricalReleaseIdentity{}, errors.New("lost historical original descriptor")
	}
	var sx unix.Statx_t
	const need = unix.STATX_MNT_ID | unix.STATX_INO | unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_NLINK | unix.STATX_SIZE
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, need, &sx); err != nil {
		return nativeHistoricalReleaseIdentity{}, err
	}
	dev := unix.Mkdev(sx.Dev_major, sx.Dev_minor)
	if sx.Mask&need != need || dev == 0 || sx.Ino == 0 || sx.Mnt_id == 0 || sx.Uid != uint32(uid) || sx.Gid != uint32(gid) {
		return nativeHistoricalReleaseIdentity{}, errors.New("historical object has incomplete identity/owner")
	}
	mode := uint32(sx.Mode)
	if directory {
		if mode&unix.S_IFMT != unix.S_IFDIR || mode&07022 != 0 {
			return nativeHistoricalReleaseIdentity{}, errors.New("historical ancestor has invalid type/protection")
		}
	} else if mode != unix.S_IFREG|0444 || sx.Nlink != 1 || sx.Size == 0 || sx.Size > nativeHistoricalManifestLimit {
		return nativeHistoricalReleaseIdentity{}, errors.New("historical manifest has invalid type/mode/links/size")
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return nativeHistoricalReleaseIdentity{}, errors.Join(errors.New("historical descriptor lost CLOEXEC"), err)
	}
	id := nativeHistoricalReleaseIdentity{file: pinFileIdentity{dev, sx.Ino, sx.Mnt_id}, mode: mode, uid: sx.Uid, gid: sx.Gid}
	if !directory {
		id.links = uint64(sx.Nlink)
		id.size = int64(sx.Size)
	}
	return id, nil
}
func readNativeHistoricalManifest(file *os.File) ([]byte, error) {
	if file == nil {
		return nil, errors.New("historical manifest is unavailable")
	}
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, nativeHistoricalManifestLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > nativeHistoricalManifestLimit {
		return nil, errors.New("historical manifest exceeds bound")
	}
	return raw, nil
}
func validateNativeHistoricalManifest(raw []byte, origin containerOrigin) (QualifiedRelease, error) {
	if err := checkJSONKeys(raw); err != nil {
		return QualifiedRelease{}, err
	}
	if err := checkNativeHistoricalFields(raw, reflect.TypeFor[releaseDocument](), 0); err != nil {
		return QualifiedRelease{}, err
	}
	release, err := LoadQualifiedRelease(raw, origin.Qualification)
	if err != nil {
		return QualifiedRelease{}, err
	}
	if release.Identity() != origin.Qualification {
		return QualifiedRelease{}, errors.New("historical qualification differs from original origin")
	}
	if _, err := release.Platform(runtime.GOARCH); err != nil {
		return QualifiedRelease{}, err
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return QualifiedRelease{}, err
	}
	if helper.BinarySHA256 != origin.HelperSHA256 {
		return QualifiedRelease{}, errors.New("historical helper differs from original origin")
	}
	return release, nil // NO ValidateHelperSource/current HelperLaunch fabrication
}

// Preserve the exact reviewed raw/pretty encoding while refusing case-folded
// aliases at every structured manifest field. No decode-and-rewrite occurs.
// Dynamic map keys retain their actual release schema validation in Load.
func checkNativeHistoricalFields(raw []byte, kind reflect.Type, depth int) error {
	if depth > 32 {
		return errors.New("historical manifest exceeds field depth")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null historical manifest field")
	}
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		allowed := map[string]reflect.Type{}
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath != "" || name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			allowed[name] = field.Type
		}
		for name, value := range fields {
			field, ok := allowed[name]
			if !ok {
				return errors.New("unknown or aliased historical manifest field")
			}
			if err := checkNativeHistoricalFields(value, field, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for _, value := range fields {
			if err := checkNativeHistoricalFields(value, kind.Elem(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := checkNativeHistoricalFields(value, kind.Elem(), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
func (p *nativeHistoricalRelease) usable(ctx context.Context) error {
	if p == nil || p.closed || p.lifetime == nil || p.verifyCurrent == nil || p.checkBoundary == nil {
		return errors.New("historical document observation unavailable/closed")
	}
	if p.failure != nil {
		return p.failure
	}
	if err := nativeHistoricalContextError(p.lifetime); err != nil {
		return errors.Join(errors.New("historical original execution expired"), err)
	}
	if ctx == nil {
		return errors.New("historical check lacks context")
	}
	if err := nativeHistoricalContextError(ctx); err != nil {
		return err
	}
	return p.checkBoundary() // pure original context/layout; never external storage
}
func (p *nativeHistoricalRelease) check(ctx context.Context) error {
	if err := p.usable(ctx); err != nil {
		return err
	}
	if !p.complete || len(p.nodes) < 2 {
		return errors.New("historical document observation incomplete")
	}
	if err := p.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := p.usable(ctx); err != nil {
		return err
	}
	return p.checkOriginal(ctx)
}

// p.mu held. No external storage/history callback occurs here. A composed
// owner may run this phase only AFTER its independent original admission and
// all external verifiers, while every origin/document reader is still LIVE.
// This still supplies only primary installed-document metadata.
func (p *nativeHistoricalRelease) checkOriginal(ctx context.Context) error {
	if err := p.usable(ctx); err != nil {
		return err
	}
	if !p.complete || len(p.nodes) < 2 {
		return errors.New("historical document observation incomplete")
	}
	for pass := 0; pass < 2; pass++ {
		for i, node := range p.nodes {
			id, err := nativeHistoricalReleaseID(node.file, p.uid, p.gid, node.directory)
			if err != nil || id != node.identity {
				return errors.Join(errors.New("historical retained object changed"), err)
			}
			parent := unix.AT_FDCWD
			resolve := uint64(unix.RESOLVE_NO_SYMLINKS)
			if i > 0 {
				parent = int(p.nodes[i-1].file.Fd())
				resolve |= unix.RESOLVE_BENEATH
			}
			flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK)
			if node.directory {
				flags |= unix.O_DIRECTORY
			}
			fd, err := unix.Openat2(parent, node.name, &unix.OpenHow{Flags: flags, Resolve: resolve})
			if err != nil {
				return err
			}
			named := os.NewFile(uintptr(fd), "historical-release-named-check")
			actual, idErr := nativeHistoricalReleaseID(named, p.uid, p.gid, node.directory)
			closeErr := named.Close()
			if idErr != nil || closeErr != nil || actual != node.identity {
				return errors.Join(errors.New("historical original named mapping replaced"), idErr, closeErr)
			}
		}
		raw, err := readNativeHistoricalManifest(p.nodes[len(p.nodes)-1].file)
		if err != nil || !bytes.Equal(raw, p.raw) {
			return errors.Join(errors.New("historical original manifest bytes changed"), err)
		}
		if _, err := validateNativeHistoricalManifest(raw, p.origin); err != nil {
			return err
		}
	}
	return p.usable(ctx) // context/layout mapping only, no external callback
}
func (p *nativeHistoricalRelease) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil historical document observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	if ctx == nil {
		p.failure = errors.New("historical check lacks context")
		return p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p.failure = p.check(ctx)
	return p.failure
}

// Original-only final phase; private type, no new public authority API.
func (p *nativeHistoricalRelease) CheckOriginal(ctx context.Context) error {
	if p == nil {
		return errors.New("nil historical document observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	if ctx == nil {
		p.failure = errors.New("historical original check lacks context")
		return p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p.failure = p.checkOriginal(ctx)
	return p.failure
}
func (p *nativeHistoricalRelease) Release(ctx context.Context) (QualifiedRelease, error) {
	if p == nil {
		return QualifiedRelease{}, errors.New("nil historical document observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return QualifiedRelease{}, p.failure
	}
	if ctx == nil {
		p.failure = errors.New("historical read lacks context")
		return QualifiedRelease{}, p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if err := p.check(ctx); err != nil {
		p.failure = err
		return QualifiedRelease{}, err
	}
	return p.release, nil
}
func (p *nativeHistoricalRelease) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	for i := len(p.nodes) - 1; i >= 0; i-- {
		if p.nodes[i].file != nil {
			p.closeErr = errors.Join(p.closeErr, p.nodes[i].file.Close())
		}
	}
	return p.closeErr
}

// Preserve the original cause as well as standard cancellation identity.
func nativeHistoricalContextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("historical context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}
