package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type runtimeCleanupStep struct {
	parent               *os.File // borrowed original full-inventory directory
	parentKey, name, key string
	directory            bool
}

// This is a suffix of one live completeNativeContainerRemoval invocation,
// never a reservation, persisted receipt or recovery/adoption mechanism. Only
// positive unlink AND parent-fsync replies advance its expected remaining set.
type nativeRuntimeCleanup struct {
	namespace                         *nativeNamespaceCleanup   // same original complete retirement
	full                              *NativeContainerInventory // borrowed; never refreshed during deletion
	parent                            *os.File                  // borrowed retained containers parent, outside deleted tree
	parentIdentity                    pinFileIdentity
	name                              string
	records                           []RuntimeRecord
	expected                          map[string]namespaceInventoryMember
	raw                               map[string][]byte // only nonsecret origin/current/immutable records
	steps                             []runtimeCleanupStep
	flexibleSize                      map[string]bool
	attempted, rootRemoved, completed bool
	failure                           error
}

// Capture BEFORE rm, after the actual all-generation retirement. Namespace
// deletion and its independent live rm ACK are still required before use.
func prepareNativeRuntimeCleanup(ctx context.Context, n *nativeNamespaceCleanup, ack *nativeContainerStopACK) (*nativeRuntimeCleanup, error) {
	if n == nil || n.retirement == nil || n.retirement.closed || !n.retirement.retired || n.retirement.scope == nil || n.attempted {
		return nil, errors.New("private cleanup requires original live retirement before namespace deletion")
	}
	r, s := n.retirement, n.retirement.scope
	if err := ack.coversInventory(r.full, s.cid); err != nil {
		return nil, err
	}
	if err := s.checkRuntime(ctx); err != nil {
		return nil, err
	}
	if err := verifyNativeNamespaceCleanupBare(ctx, s); err != nil {
		return nil, err
	}
	return snapshotRuntimeCleanupAt(ctx, n, s.containers, s.containersIdentity, s.selected.Launch.Binding.Incarnation)
}

// The shared planner reads actual role/no-follow/mount checked owned files.
// Project fixtures supply ordinary owned directories and scripted protocol
// callbacks; only the constructor above supplies admitted native scope.
func snapshotRuntimeCleanupAt(ctx context.Context, n *nativeNamespaceCleanup, parent *os.File, parentID pinFileIdentity, name string) (*nativeRuntimeCleanup, error) {
	if n == nil || n.retirement == nil || n.retirement.closed || !n.retirement.retired || n.retirement.full == nil || parent == nil || !runtimeCleanupComponent(name) {
		return nil, errors.New("private cleanup lacks original full inventory and fixed parent/member")
	}
	i := n.retirement.full
	if err := i.Check(ctx); err != nil {
		return nil, err
	}
	id, err := pinIdentity(int(parent.Fd()), i.owners.rootUID, i.owners.rootGID, true)
	if err != nil {
		return nil, err
	}
	if id != parentID || id.device != i.identity.device || id.mount != i.identity.mount {
		return nil, errors.New("private cleanup parent is outside original native filesystem")
	}
	p := &nativeRuntimeCleanup{namespace: n, full: i, parent: parent, parentIdentity: parentID, name: name, records: i.Records(), expected: map[string]namespaceInventoryMember{}, raw: map[string][]byte{}, flexibleSize: map[string]bool{}}
	// No auth/bootstrap/key body is opened just to authorize unlink. The exact
	// immutable anchor bytes were independently checked in the full inventory.
	p.raw["origin.json"], err = json.Marshal(generationOrigin(i.current))
	if err != nil {
		return nil, err
	}
	p.raw["current.json"], err = json.Marshal(i.current)
	if err != nil {
		return nil, err
	}
	for _, record := range p.records {
		p.raw["generations/"+record.Generation+"/record.json"] = bytes.Clone(i.rawRecords[record.Generation])
	}
	var nodes = map[string]*reservationNode{}
	if err := runtimeCleanupMembers(i.tree, "", i.identity.mount, p.expected, nodes); err != nil {
		return nil, err
	}
	// All non-record generation leaves/directories precede EVERY generation
	// record. Keep global current/origin anchors until all generations are gone.
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		if key != "" && key != "generations" && key != "origin.json" && key != "current.json" && !runtimeCleanupRecord(key) && (!strings.HasPrefix(key, "generations/") || strings.Count(key, "/") != 1) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(a, b int) bool {
		da, db := strings.Count(keys[a], "/"), strings.Count(keys[b], "/")
		if da != db {
			return da > db
		}
		return keys[a] < keys[b]
	})
	add := func(key string) error {
		node := nodes[key]
		if node == nil {
			return errors.New("private cleanup omitted original fixed member")
		}
		parentKey := ""
		if at := strings.LastIndexByte(key, '/'); at >= 0 {
			parentKey = key[:at]
		}
		owner := nodes[parentKey]
		if owner == nil || owner.file == nil || node.parent != int(owner.file.Fd()) {
			return errors.New("private cleanup step lacks original retained parent")
		}
		p.steps = append(p.steps, runtimeCleanupStep{owner.file, parentKey, node.name, key, node.file != nil})
		return nil
	}
	for _, key := range keys {
		if err := add(key); err != nil {
			return nil, err
		}
	}
	for _, record := range p.records {
		if err := add("generations/" + record.Generation + "/record.json"); err != nil {
			return nil, err
		}
		if err := add("generations/" + record.Generation); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"generations", "current.json", "origin.json"} {
		if err := add(key); err != nil {
			return nil, err
		}
	}
	p.steps = append(p.steps, runtimeCleanupStep{parent, "", name, "", true})
	if err := p.checkExpected(ctx); err != nil {
		return nil, err
	}
	if err := i.Check(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func runtimeCleanupComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}
func runtimeCleanupRecord(key string) bool {
	return strings.HasPrefix(key, "generations/") && strings.Count(key, "/") == 2 && strings.HasSuffix(key, "/record.json")
}

// Descriptor observation never interprets os.File.Name as a filesystem path.
func runtimeCleanupFDMember(file *os.File) (namespaceInventoryMember, error) {
	if file == nil {
		return namespaceInventoryMember{}, errors.New("private cleanup lost directory descriptor")
	}
	var sx unix.Statx_t
	const need = unix.STATX_MNT_ID | unix.STATX_INO | unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_UID | unix.STATX_GID | unix.STATX_NLINK | unix.STATX_SIZE
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, need, &sx); err != nil {
		return namespaceInventoryMember{}, err
	}
	dev := unix.Mkdev(sx.Dev_major, sx.Dev_minor)
	if sx.Mask&need != need || sx.Mnt_id == 0 || dev == 0 || sx.Ino == 0 || sx.Mode&unix.S_IFMT != unix.S_IFDIR {
		return namespaceInventoryMember{}, errors.New("private cleanup lacks complete directory identity")
	}
	return namespaceInventoryMember{pinFileIdentity{dev, sx.Ino, sx.Mnt_id}, uint32(sx.Mode), sx.Uid, sx.Gid, uint64(sx.Nlink), int64(sx.Size)}, nil
}

func runtimeCleanupMembers(node *reservationNode, key string, mount uint64, members map[string]namespaceInventoryMember, nodes map[string]*reservationNode) error {
	if node == nil {
		return errors.New("private cleanup lacks inventory tree")
	}
	var m namespaceInventoryMember
	var err error
	if key == "" {
		m, err = runtimeCleanupFDMember(node.file)
	} else {
		m, err = namespaceInventoryMemberAt(node.parent, node.name)
	}
	if err != nil {
		return err
	}
	s := node.identity
	if m.identity.device != s.Dev || m.identity.inode != s.Ino || m.identity.mount != mount || m.mode != s.Mode || m.uid != s.Uid || m.gid != s.Gid || m.links != uint64(s.Nlink) || m.size != s.Size {
		return errors.New("private cleanup inventory changed while capturing members")
	}
	if _, exists := members[key]; exists {
		return errors.New("private cleanup repeated member")
	}
	members[key] = m
	if nodes != nil {
		nodes[key] = node
	}
	for _, child := range node.children {
		next := child.name
		if key != "" {
			next = key + "/" + next
		}
		if err := runtimeCleanupMembers(child, next, mount, members, nodes); err != nil {
			return err
		}
	}
	return nil
}

func (p *nativeRuntimeCleanup) matches(key string, observed namespaceInventoryMember) bool {
	expected, exists := p.expected[key]
	if p.flexibleSize[key] {
		expected.size = observed.size
	}
	return exists && expected == observed
}

// Incomplete inventory is deletion-stage observation only. It must equal the
// original complete set minus positively acknowledged in-call unlinks.
func (p *nativeRuntimeCleanup) checkExpected(ctx context.Context) error {
	if p == nil || p.full == nil || p.full.root == nil || p.parent == nil {
		return errors.New("private cleanup lost original descriptors")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := pinIdentity(int(p.parent.Fd()), p.full.owners.rootUID, p.full.owners.rootGID, true)
	if err != nil {
		return err
	}
	if id != p.parentIdentity {
		return errors.New("private cleanup selected foreign containers parent")
	}
	if p.rootRemoved {
		if len(p.expected) != 0 || len(p.raw) != 0 {
			return errors.New("private cleanup advanced root before original members")
		}
		var st unix.Stat_t
		err := unix.Fstatat(int(p.parent.Fd()), p.name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if !errors.Is(err, unix.ENOENT) {
			return errors.New("private cleanup named root reappeared or absence could not be verified")
		}
		return nil // verification AFTER original unlink+fsync, never rm authority
	}
	named, err := namespaceInventoryMemberAt(int(p.parent.Fd()), p.name)
	if err != nil {
		return err
	}
	if !p.matches("", named) {
		return errors.New("private cleanup native incarnation mapping changed")
	}
	remaining := 1024
	tree, err := inventoryReservation(ctx, p.full.root, "", p.full.identity.device, p.full.owners, &remaining)
	if err != nil {
		return err
	}
	defer tree.close()
	if err := verifyContainerInventoryMounts(tree); err != nil {
		return err
	}
	members := map[string]namespaceInventoryMember{}
	nodes := map[string]*reservationNode{}
	if err := runtimeCleanupMembers(tree, "", p.full.identity.mount, members, nodes); err != nil {
		return err
	}
	if len(members) != len(p.expected) {
		return errors.New("private cleanup changed expected remaining member coverage")
	}
	for key, m := range members {
		if !p.matches(key, m) {
			return errors.New("private cleanup remaining identity/role/links/size changed")
		}
	}
	for key, raw := range p.raw {
		node := nodes[key]
		if node == nil {
			return errors.New("private cleanup lost immutable anchor")
		}
		at := strings.LastIndexByte(key, '/')
		parentKey := ""
		if at >= 0 {
			parentKey = key[:at]
		}
		parent := nodes[parentKey]
		if parent == nil || parent.file == nil {
			return errors.New("private cleanup lost immutable anchor parent")
		}
		observed, err := runtimeReadAt(parent.file, node.name, p.full.owners.rootUID, p.full.owners.rootGID, 1<<20)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, observed) {
			return errors.New("private cleanup immutable anchor bytes changed")
		}
	}
	return nil
}

func (p *nativeRuntimeCleanup) verifyStep(step runtimeCleanupStep) error {
	if step.parent == nil || !runtimeCleanupComponent(step.name) {
		return errors.New("private cleanup lost fixed step")
	}
	if step.key == "origin.json" && (len(p.expected) != 2 || len(p.raw) != 1) {
		return errors.New("private cleanup origin must be the last leaf")
	}
	if step.key == "current.json" {
		if _, exists := p.expected["generations"]; exists {
			return errors.New("private cleanup current precedes complete generation removal")
		}
	}
	if runtimeCleanupRecord(step.key) {
		for key := range p.expected {
			if strings.HasPrefix(key, "generations/") && strings.Count(key, "/") >= 2 && !runtimeCleanupRecord(key) {
				return errors.New("private cleanup record precedes generation role data")
			}
		}
	}
	if step.key == "" {
		if !step.directory || step.parent != p.parent || step.name != p.name || len(p.expected) != 1 || len(p.raw) != 0 {
			return errors.New("private cleanup cannot remove root before all anchors")
		}
	} else {
		key := step.name
		if step.parentKey != "" {
			key = step.parentKey + "/" + key
		}
		if key != step.key {
			return errors.New("private cleanup changed fixed member path")
		}
		observed, err := runtimeCleanupFDMember(step.parent)
		if err != nil {
			return err
		}
		if !p.matches(step.parentKey, observed) {
			return errors.New("private cleanup selected foreign step parent")
		}
	}
	observed, err := namespaceInventoryMemberAt(int(step.parent.Fd()), step.name)
	if err != nil {
		return err
	}
	if !p.matches(step.key, observed) {
		return errors.New("private cleanup target changed original identity/role/links")
	}
	if step.directory {
		if observed.mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.New("private cleanup target is not original directory")
		}
		for key := range p.expected {
			if key != step.key && (step.key == "" || strings.HasPrefix(key, step.key+"/")) {
				return errors.New("private cleanup directory still has expected children")
			}
		}
	} else if raw, ok := p.raw[step.key]; ok {
		current, err := runtimeReadAt(step.parent, step.name, p.full.owners.rootUID, p.full.owners.rootGID, 1<<20)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, current) {
			return errors.New("private cleanup target anchor bytes changed")
		}
	}
	return nil
}

func (p *nativeRuntimeCleanup) removeAfterACK(ctx context.Context, ack *nativeContainerRemoveACK, remaining func(context.Context) error) error {
	if p != nil && p.attempted {
		return p.removeWithCurrentVerifier(ctx, ack, nil)
	}
	if p == nil || p.namespace == nil || p.namespace.retirement == nil || p.namespace.retirement.scope == nil || remaining == nil {
		return errors.New("private cleanup lacks original native/current verifier")
	}
	s := p.namespace.retirement.scope
	return p.removeWithCurrentVerifier(ctx, ack, func(c context.Context) error {
		if err := s.checkKernelStorage(c, remaining); err != nil {
			return err
		}
		return verifyNativeNamespaceCleanupBare(c, s)
	})
}

func (p *nativeRuntimeCleanup) checkClearedNamespaces(ctx context.Context) error {
	n := p.namespace
	if n.lock == nil {
		if n.expected != nil || len(n.steps) != 0 {
			return errors.New("confined private cleanup acquired namespace bookkeeping")
		}
		for _, record := range p.records {
			if record.Launch.UnconfinedNetwork {
				return errors.New("unconfined private cleanup lost original namespace lock")
			}
		}
		return nil
	}
	if n.expected == nil || len(n.expected.raw) != 0 {
		return errors.New("private cleanup namespace anchors remain")
	}
	for key := range n.expected.members {
		if key != ".lock" && key != "origins" && key != "pins" && key != noInitializationDirectory {
			return errors.New("private cleanup namespace generation metadata remains")
		}
	}
	return n.checkExpected(ctx)
}

func (p *nativeRuntimeCleanup) removeWithCurrentVerifier(ctx context.Context, ack *nativeContainerRemoveACK, current func(context.Context) error) (err error) {
	if p != nil && p.attempted {
		if p.failure != nil {
			return p.failure
		}
		return &NativeEffectsUncertain{Cause: errors.New("private runtime cleanup already attempted; no retry")}
	}
	if p == nil || p.namespace == nil || p.namespace.retirement == nil || p.namespace.retirement.closed || !p.namespace.retirement.retired || p.namespace.retirement.full != p.full || !p.namespace.completed || p.namespace.failure != nil || current == nil {
		return errors.New("private cleanup lacks original completed namespace removal/current verifier")
	}
	if err := ack.coversInventory(p.full, p.namespace.cid, true); err != nil {
		return err
	}
	if len(p.records) != len(p.full.records) {
		return errors.New("private cleanup lost original full record set")
	}
	records := map[string]string{}
	for _, record := range p.records {
		if records[record.Generation] != "" {
			return errors.New("private cleanup duplicated original record")
		}
		records[record.Generation] = recordIdentity(record)
	}
	for _, record := range p.full.records {
		if records[record.Generation] != recordIdentity(record) {
			return errors.New("private cleanup changed original record set")
		}
		delete(records, record.Generation)
	}
	if len(records) != 0 {
		return errors.New("private cleanup omitted original record")
	}
	p.attempted = true
	defer func() {
		if err != nil {
			err = &NativeEffectsUncertain{Cause: err}
			p.failure = err
		}
	}()
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := current(ctx); err != nil {
			return err
		}
		if err := p.checkClearedNamespaces(ctx); err != nil {
			return err
		}
		return p.checkExpected(ctx)
	}
	if err := verify(); err != nil {
		return err
	}
	for _, step := range p.steps {
		if err := verify(); err != nil {
			return err
		}
		if err := p.verifyStep(step); err != nil {
			return err
		}
		flags := 0
		if step.directory {
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(int(step.parent.Fd()), step.name, flags); err != nil {
			return err
		}
		if err := step.parent.Sync(); err != nil {
			return err
		}
		delete(p.expected, step.key)
		delete(p.raw, step.key)
		if step.key == "" {
			p.rootRemoved = true
			continue
		}
		p.flexibleSize[step.parentKey] = true
		if step.directory {
			parent, exists := p.expected[step.parentKey]
			if !exists || parent.links <= 2 {
				return errors.New("private cleanup lost original directory link accounting")
			}
			parent.links--
			p.expected[step.parentKey] = parent
		}
	}
	if err := verify(); err != nil {
		return err
	}
	p.completed = true
	return nil
}
