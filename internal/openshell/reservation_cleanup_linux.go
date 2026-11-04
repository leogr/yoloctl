package openshell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// NativeReservation is same-call bookkeeping, never permission to stop/remove
// a container or adopt a directory after restart. Only the creator receives its
// retained descriptors. No secret is exposed by formatting this handle.
type NativeReservation struct {
	parent, directory *os.File
	origin            []byte
	identity          unix.Stat_t
	// These identities come from the original retained directory FDs and
	// exclusively created origin FD, never a later metadata reconstruction.
	collectionID, directoryID, originID pinFileIdentity
	originalOrigin                      *os.File // continuously held: an unlinked inode cannot be recycled
	uid, gid                            int
	owners                              runtimeOwners
	name                                string
}

func (*NativeReservation) String() string      { return "OpenShell native reservation [redacted]" }
func (r *NativeReservation) GoString() string  { return r.String() }
func (r *NativeReservation) Directory() string { return r.directory.Name() }
func (r *NativeReservation) Close() error {
	if r == nil {
		return nil
	}
	var err error
	if r.originalOrigin != nil {
		err = r.originalOrigin.Close()
		r.originalOrigin = nil
	}
	if r.directory != nil {
		err = errors.Join(err, r.directory.Close())
		r.directory = nil
	}
	if r.parent != nil {
		err = errors.Join(err, r.parent.Close())
		r.parent = nil
	}
	return err
}

// RemoveAfterContainerRemoval removes this call's private runtime tree only
// AFTER the admitted caller has acknowledged removal of its own exact CID (or
// knows create never began) and
// stopped every owned forwarding worker. It never performs those effects,
// infers their history from absence, or removes issuer/artifact/account state.
// Failed checks retain the tree; a partial unlink/fsync failure is an error.
func (r *NativeReservation) RemoveAfterContainerRemoval(ctx context.Context) error {
	if r == nil || r.directory == nil || r.parent == nil {
		return errors.New("native reservation handle is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.verify(); err != nil {
		return err
	}
	// Inventory everything first, through no-follow/no-mount-crossing handles.
	// An unexpected member causes no deletion. All descriptors remain pinned
	// until deletion completes, so replacement cannot redirect traversal.
	remaining := 1024
	tree, err := inventoryReservation(ctx, r.directory, "", r.identity.Dev, r.owners, &remaining)
	if err != nil {
		return err
	}
	defer tree.close()
	if err := r.verify(); err != nil {
		return err
	}
	if err := tree.remove(ctx); err != nil {
		return err
	}
	if err := r.verifyDirectory(); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(r.parent.Fd()), r.name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return r.parent.Sync()
}

func (r *NativeReservation) verifyDirectory() error {
	anchor, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(anchor) }()
	parent, err := unix.Openat2(anchor, strings.TrimPrefix(filepath.Dir(r.Directory()), "/"), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	var retained, selected unix.Stat_t
	if unix.Fstat(int(r.parent.Fd()), &retained) != nil || unix.Fstat(parent, &selected) != nil || !sameReservationNode(retained, selected) || selected.Mode&0o7777 != 0o700 || selected.Uid != uint32(r.uid) || selected.Gid != uint32(r.gid) {
		return errors.New("native reservation collection changed")
	}
	for _, fd := range []int{int(r.parent.Fd()), parent} {
		id, err := pinIdentity(fd, r.uid, r.gid, true)
		if err != nil || id != r.collectionID {
			return errors.New("native reservation original collection mount/identity changed")
		}
	}
	if id, err := pinIdentity(int(r.directory.Fd()), r.uid, r.gid, true); err != nil || id != r.directoryID {
		return errors.New("native reservation original directory mount/identity changed")
	}
	namedFD, err := unix.Openat2(parent, r.name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return err
	}
	namedID, identityErr := pinIdentity(namedFD, r.uid, r.gid, true)
	closeErr := unix.Close(namedFD)
	if identityErr != nil || closeErr != nil || namedID != r.directoryID {
		return errors.New("native reservation named original directory mount/identity changed")
	}

	var current unix.Stat_t
	if err := unix.Fstatat(int(r.parent.Fd()), r.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !sameReservationNode(current, r.identity) || current.Mode&0o7777 != 0o700 || current.Uid != uint32(r.uid) || current.Gid != uint32(r.gid) {
		return errors.New("native reservation directory was replaced or changed")
	}
	return nil
}

// Verify checks the continuously retained directory and nonsecret origin.
// It is observation only and never substitutes for create/stop/remove ACKs.
func (r *NativeReservation) Verify() error {
	if r == nil || r.directory == nil || r.parent == nil {
		return errors.New("native reservation handle is closed")
	}
	return r.verify()
}

func (r *NativeReservation) verify() error {
	if r == nil || r.originalOrigin == nil {
		return errors.New("native reservation lost its original origin descriptor")
	}
	if id, err := pinIdentity(int(r.originalOrigin.Fd()), r.uid, r.gid, false); err != nil || id != r.originID {
		return errors.New("native reservation original origin descriptor changed")
	}
	if err := r.verifyDirectory(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(r.directory.Fd()), "origin.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "origin.json")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := privateFileOwner(info, r.uid, r.gid); err != nil {
		return err
	}
	before, err := pinIdentity(fd, r.uid, r.gid, false)
	if err != nil || before != r.originID {
		return errors.New("native reservation original origin inode/mount changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, r.origin) {
		return errors.New("native reservation origin changed")
	}
	after, err := pinIdentity(fd, r.uid, r.gid, false)
	if err != nil || after != before {
		return errors.New("native reservation original origin changed during read")
	}
	named, err := unix.Openat2(int(r.directory.Fd()), "origin.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return err
	}
	namedID, identityErr := pinIdentity(named, r.uid, r.gid, false)
	closeErr := unix.Close(named)
	if identityErr != nil || closeErr != nil || namedID != r.originID {
		return errors.New("native reservation origin mapping changed during read")
	}
	return nil
}

type reservationNode struct {
	file     *os.File
	parent   int
	name     string
	identity unix.Stat_t
	children []*reservationNode
}

func (n *reservationNode) close() {
	for _, child := range n.children {
		child.close()
	}
	if n.file != nil {
		_ = n.file.Close()
	}
}
func sameReservationNode(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode&unix.S_IFMT == b.Mode&unix.S_IFMT
}

// The allowlist describes fresh PrepareNative/activation/forward bookkeeping.
// A new on-disk schema must extend it intentionally; generic recursive cleanup
// is never allowed. Boundary/supervisor owners match their selected role identities;
// their directory names, modes, devices and no-link constraints stay fixed.
func reservationGeneration(name string) bool {
	return len(name) == 49 && name[0] == 'g' && lowerHex(name[1:], 24)
}

// safepath.createTempAt uses this exact 16-byte lower-hex nonce format.
// Only root-owned status/publication parents admit these steady-state writer
// intermediates; this never authorizes arbitrary .tmp files or other parents.
func runtimeAtomicTemporary(name string) bool {
	const prefix = ".yoloctl-"
	const suffix = ".tmp"
	return len(name) == len(prefix)+32+len(suffix) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) && lowerHex(name[len(prefix):len(prefix)+32], 16)
}

func reservationMember(path string) (directory, socket, allowed bool) {
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 1:
		return path == "generations", false, path == "origin.json" || path == "current.json" || path == "generations"
	case 2:
		return true, false, parts[0] == "generations" && reservationGeneration(parts[1])
	case 3:
		if parts[0] != "generations" || !reservationGeneration(parts[1]) {
			return
		}
		return parts[2] != "record.json", false, parts[2] == "record.json" || parts[2] == "boundary" || parts[2] == "supervisor" || parts[2] == "status"
	case 4:
		if parts[0] != "generations" || !reservationGeneration(parts[1]) {
			return
		}
		switch parts[2] {
		case "boundary":
			return false, false, parts[3] == "bootstrap.json" || parts[3] == "server.crt" || parts[3] == "server.key"
		case "supervisor":
			return false, false, parts[3] == "runtime-descriptor.json" || parts[3] == "auth.json" || parts[3] == "policy.yaml" || parts[3] == "sandbox-policy.rego"
		case "status":
			return parts[3] == "forwards", parts[3] == activationSocketName, parts[3] == "runtime.json" || parts[3] == "activation.key" || parts[3] == "activation.json" || parts[3] == activationSocketName || parts[3] == "forwards" || runtimeAtomicTemporary(parts[3])
		}
	case 5:
		if parts[0] == "generations" && reservationGeneration(parts[1]) && parts[2] == "status" && parts[3] == "forwards" {
			name := parts[4]
			return false, false, (strings.HasSuffix(name, ".json") && lowerHex(strings.TrimSuffix(name, ".json"), 16)) || (strings.HasSuffix(name, ".lock") && lowerHex(strings.TrimSuffix(name, ".lock"), 16)) || runtimeAtomicTemporary(name)
		}
	}
	return
}

// runtimeReservationRule follows prepareRuntimeSharedAt and the pinned fork
// e8605009: runtime_initiated.rs control bind, lib.rs readiness bind,
// supervisor-process/src/ssh.rs and supervisor-network/src/l7/tls.rs.
// Socket permissions before their final chmod, and readiness's inherited
// umask, are ordinary permission bits only; protected parents and exact role
// owners remain mandatory. No child view or writable project is traversed.
type runtimeReservationRule struct {
	kind             uint32
	permissions      uint32
	mask             uint32
	partialDirectory bool
}

func reservationTemporaryTLS(name string) bool {
	// tempfile3.27.0 defaults: .tmp + six alphanumeric characters, no suffix.
	// https://docs.rs/tempfile/3.27.0/src/tempfile/lib.rs.html#227-235
	// https://raw.githubusercontent.com/Stebalien/tempfile/v3.27.0/src/util.rs
	if len(name) != 10 || !strings.HasPrefix(name, ".tmp") {
		return false
	}
	for _, c := range name[4:] {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}
func runtimeReservationMember(path string) (runtimeReservationRule, bool) {
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[0] != "generations" || !reservationGeneration(parts[1]) || parts[2] != "status" {
		return runtimeReservationRule{}, false
	}
	rest := parts[3:]
	directory := runtimeReservationRule{kind: unix.S_IFDIR, permissions: 0o700, mask: 0o7777, partialDirectory: true}
	socket := runtimeReservationRule{kind: unix.S_IFSOCK, mask: 0o7000}
	// Rust NamedTempFile uses mode0600 subject to umask; group/other access,
	// execute and special bits are never accepted, including partial writes.
	tls := runtimeReservationRule{kind: unix.S_IFREG, mask: 0o7177}
	switch rest[0] {
	case "runtime-control":
		if len(rest) == 1 {
			directory.permissions = 0o711
			return directory, true
		}
		if len(rest) == 2 && rest[1] == "control.sock" {
			return socket, true
		}
	case "runtime-supervisor":
		if len(rest) == 1 {
			return directory, true
		}
		if len(rest) == 2 {
			switch rest[1] {
			case "tmp", "ssh", "proxy-tls":
				return directory, true
			case "ready.sock":
				return socket, true
			}
		}
		if len(rest) == 3 {
			switch rest[1] {
			case "ssh":
				if rest[2] == "ssh.sock" {
					return socket, true
				}
			case "proxy-tls":
				if rest[2] == "openshell-ca.pem" || rest[2] == "ca-bundle.pem" || reservationTemporaryTLS(rest[2]) {
					return tls, true
				}
			case "tmp":
				if reservationTemporaryTLS(rest[2]) {
					return tls, true
				}
			}
		}
	}
	return runtimeReservationRule{}, false
}

func inventoryReservation(ctx context.Context, directory *os.File, path string, device uint64, owners runtimeOwners, remaining *int) (*reservationNode, error) {
	return inventoryReservationView(ctx, directory, path, device, owners, remaining, false)
}

// Ownership capture never opens transient observation files of a still-live
// runtime. It retains immutable files, directory anchors and stable forward
// locks. A complete strict inventory after original Stop/inactivity is still
// required before removal; this filtered tree cannot supply that authority.
func inventoryReservationOwnership(ctx context.Context, directory *os.File, path string, device uint64, owners runtimeOwners, remaining *int) (*reservationNode, error) {
	return inventoryReservationView(ctx, directory, path, device, owners, remaining, true)
}
func inventoryReservationView(ctx context.Context, directory *os.File, path string, device uint64, owners runtimeOwners, remaining *int, ownership bool) (_ *reservationNode, err error) {
	// Duplicate with a new directory offset; the retained reservation FD must
	// also work after an earlier refusal without relying on a consumed offset.
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	node := &reservationNode{file: os.NewFile(uintptr(fd), path)}
	defer func() {
		if err != nil {
			node.close()
		}
	}()
	if err := unix.Fstat(fd, &node.identity); err != nil {
		return nil, err
	}
	names, err := node.file.Readdirnames(*remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > *remaining {
		return nil, errors.New("native cleanup inventory exceeds bound")
	}
	*remaining -= len(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		relative := name
		if path != "" {
			relative = path + "/" + name
		}

		isDir, isSocket, allowed := reservationMember(relative)
		rule, runtimeOwned := runtimeReservationMember(relative)
		if runtimeOwned {
			allowed = true
			isDir = rule.kind == unix.S_IFDIR
			isSocket = rule.kind == unix.S_IFSOCK
		}
		if !allowed {
			return nil, errors.New("unexpected native cleanup member")
		}
		if ownership && !isDir && mutableRuntimeObservation(relative) {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
		mode := uint32(unix.S_IFREG)
		permissions := uint32(0o600)
		mask := uint32(0o7777)
		if isDir {
			mode = unix.S_IFDIR
			permissions = 0o700
		} else if isSocket {
			mode = unix.S_IFSOCK
		}
		expectedUID, expectedGID := owners.rootUID, owners.rootGID
		parts := strings.Split(relative, "/")
		if len(parts) >= 3 && parts[2] == "boundary" {
			expectedUID, expectedGID = owners.boundaryUID, owners.boundaryGID
		}
		if len(parts) >= 3 && parts[2] == "supervisor" {
			expectedUID, expectedGID = owners.supervisorUID, owners.supervisorGID
		}
		partialRoot := false
		partialSupervisor := false
		if runtimeOwned {
			mode, permissions, mask = rule.kind, rule.permissions, rule.mask
			expectedUID, expectedGID = owners.supervisorUID, owners.supervisorGID
			// createRuntimeDirectoryAt creates root mode0700, changes owner,
			// then chmods. Failure between those steps leaves owned partial
			// state. Only these exact initializer directories admit it.
			partialRoot = rule.partialDirectory && stat.Uid == uint32(owners.rootUID) && stat.Gid == uint32(owners.rootGID) && stat.Mode&0o7077 == 0
			partialSupervisor = rule.partialDirectory && stat.Uid == uint32(expectedUID) && stat.Gid == uint32(expectedGID) && stat.Mode&0o7777 == 0o700
		}
		ownerOK := stat.Uid == uint32(expectedUID) && stat.Gid == uint32(expectedGID)
		modeOK := stat.Mode&mask == permissions
		if stat.Dev != device || stat.Mode&unix.S_IFMT != mode || (!isDir && stat.Nlink != 1) || ((!ownerOK || !modeOK) && !partialRoot && !partialSupervisor) {
			return nil, errors.New("native cleanup member has unexpected type/device/links/mode/role owner")
		}

		child := &reservationNode{parent: fd, name: name, identity: stat}
		if isDir {
			childFD, e := unix.Openat2(fd, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
			if e != nil {
				return nil, e
			}
			opened := os.NewFile(uintptr(childFD), name)
			child, e = inventoryReservationView(ctx, opened, relative, device, owners, remaining, ownership)
			_ = opened.Close()
			if e != nil {
				return nil, e
			}
			child.parent, child.name = fd, name
			if !sameReservationNode(child.identity, stat) {
				child.close()
				return nil, errors.New("native cleanup directory changed during inventory")
			}
		}
		node.children = append(node.children, child)
	}
	return node, nil
}
func (n *reservationNode) remove(ctx context.Context) error {
	for _, child := range n.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		var current unix.Stat_t
		if err := unix.Fstatat(child.parent, child.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if !sameReservationNode(current, child.identity) || current.Mode != child.identity.Mode || current.Uid != child.identity.Uid || current.Gid != child.identity.Gid || current.Nlink != child.identity.Nlink {
			return errors.New("native cleanup member changed after inventory")
		}
		flags := 0
		if child.file != nil {
			if err := child.remove(ctx); err != nil {
				return err
			}
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(child.parent, child.name, flags); err != nil {
			return err
		}
	}
	return n.file.Sync()
}
