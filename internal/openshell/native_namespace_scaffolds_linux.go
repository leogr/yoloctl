package openshell

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const nativeNamespaceScaffoldMembers = 32768

type nativeNamespaceScaffoldEntry struct {
	incarnation  string
	bareMetadata bool // exact empty source profile ONLY; no historical/Box/ACK meaning
}
type nativeNamespaceScaffoldView struct {
	prefix            nativeOriginAncestryState
	collectionMissing bool // exact ENOENT beneath original control, when control exists
	entries           []nativeNamespaceScaffoldEntry
}
type nativeNamespaceScaffoldNode struct {
	file, parent *os.File // owns file ONLY; parent is borrowed or another owned node
	name         string
	identity     pinFileIdentity
	directory    bool
}
type nativeNamespaceScaffoldRoster struct {
	node    int
	names   []string
	members map[string]namespaceInventoryMember
}

// Passive top-level SOURCE METADATA only. Payload-bearing generations remain
// UNEXAMINED; this is not the namespace/origin union or mode classifier. An
// empty scaffold supplies no Box identity, old outcome, consumer absence, ACK,
// cleanup permission or guarantee about any kernel-held reference.
// Borrow only the SAME original scope/control; own only the FDs opened here.
// A future composed owner must keep this object live through its final check.
// No public SDK caller/dispatch is installed in this slice.
type nativeNamespaceScaffolds struct {
	mu                sync.Mutex
	scope             *nativeOriginScope
	control           *os.File // BORROWED from scope's original ancestry; never closed here
	controlID         pinFileIdentity
	uid, gid          int
	view              nativeNamespaceScaffoldView
	nodes             []nativeNamespaceScaffoldNode
	rosters           []nativeNamespaceScaffoldRoster
	complete, closed  bool
	failure, closeErr error
}

func (*nativeNamespaceScaffolds) String() string {
	return "namespace scaffold metadata [payload unexamined]"
}
func (p *nativeNamespaceScaffolds) GoString() string { return p.String() }

func observeNativeNamespaceScaffolds(ctx context.Context, scope *nativeOriginScope) (_ *nativeNamespaceScaffolds, result error) {
	if ctx == nil || scope == nil {
		return nil, errors.New("namespace scaffold reader lacks original context/scope")
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if err := scope.Check(ctx); err != nil {
		return nil, err
	}
	p := &nativeNamespaceScaffolds{scope: scope}
	defer func() {
		if result != nil {
			result = errors.Join(result, p.Close())
		}
	}()
	scope.mu.Lock()
	captureErr := p.capture(ctx)
	scope.mu.Unlock()
	if captureErr != nil {
		return nil, captureErr
	}
	p.complete = true
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// scope.mu is held for all borrowed-descriptor access, excluding outer expiry.
func (p *nativeNamespaceScaffolds) capture(ctx context.Context) error {
	if err := p.scope.usable(); err != nil {
		return err
	}
	p.view.prefix = p.scope.original.state
	a := p.scope.ancestry
	a.mu.Lock()
	p.uid, p.gid = a.uid, a.gid
	if len(a.nodes) >= 3 {
		p.control = a.nodes[2].file
		p.controlID = a.nodes[2].identity.file
	}
	a.mu.Unlock()
	// Missing containers does NOT skip the sibling collection. Only a missing
	// runtime/control prefix prevents opening namespace-pins beneath control.
	if p.control != nil {
		_, err := p.add(p.control, "namespace-pins", true)
		if errors.Is(err, unix.ENOENT) {
			p.view.collectionMissing = true
		} else if err != nil {
			return err
		} else {
			root := len(p.nodes) - 1
			used := 0
			names, err := p.roster(ctx, root, &used)
			if err != nil {
				return err
			}
			for _, inc := range names {
				if !lowerHex(inc, 16) {
					return errors.New("noncanonical namespace scaffold incarnation")
				}
				index, err := p.add(p.nodes[root].file, inc, true)
				if err != nil {
					return err
				}
				members, err := p.roster(ctx, index, &used)
				if err != nil {
					return err
				}
				if !slices.Equal(members, []string{".lock", "origins", "pins"}) && !slices.Equal(members, []string{".lock", "no-init", "origins", "pins"}) {
					return errors.New("partial/unknown namespace scaffold profile")
				}
				if _, err := p.add(p.nodes[index].file, ".lock", false); err != nil {
					return err
				}
				entry := nativeNamespaceScaffoldEntry{incarnation: inc, bareMetadata: true}
				for _, child := range members {
					if child == ".lock" {
						continue
					}
					dir, err := p.add(p.nodes[index].file, child, true)
					if err != nil {
						return err
					}
					generations, err := p.roster(ctx, dir, &used)
					if err != nil {
						return err
					}
					for _, name := range generations {
						generation := name
						if child != "pins" {
							if !strings.HasSuffix(name, ".json") {
								return errors.New("unknown namespace generation metadata name")
							}
							generation = strings.TrimSuffix(name, ".json")
						}
						if !namespacePinGenerationName(generation) {
							return errors.New("noncanonical namespace generation metadata")
						}
						member := p.rosters[len(p.rosters)-1].members[name]
						if member.uid != uint32(p.uid) || member.gid != uint32(p.gid) {
							return errors.New("namespace generation metadata owner changed")
						}
						if child == "pins" {
							// A healthy original generation directory may be a self-bind. Observe
							// its metadata only; never open it or adopt its NSFS child here.
							if member.mode != unix.S_IFDIR|0700 {
								return errors.New("invalid namespace pin generation directory")
							}
						} else if member.mode != unix.S_IFREG|0600 || member.links != 1 || member.identity.device != p.controlID.device || member.identity.mount != p.controlID.mount {
							return errors.New("invalid namespace generation origin/no-init file")
						}
					}
					if len(generations) != 0 {
						entry.bareMetadata = false
					}
				}
				p.view.entries = append(p.view.entries, entry)
			}
		}
	}
	return nil
}

func (p *nativeNamespaceScaffolds) add(parent *os.File, name string, directory bool) (int, error) {
	flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: flags, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return -1, err
	}
	node := nativeNamespaceScaffoldNode{file: os.NewFile(uintptr(fd), "namespace-scaffold-observation"), parent: parent, name: name, directory: directory}
	p.nodes = append(p.nodes, node)
	index := len(p.nodes) - 1
	id, err := pinIdentity(fd, p.uid, p.gid, directory)
	if err != nil {
		return -1, err
	}
	p.nodes[index].identity = id
	if id.device != p.controlID.device || id.mount != p.controlID.mount {
		return -1, errors.New("namespace scaffold crosses original native control filesystem")
	}
	if err := p.checkNode(p.nodes[index]); err != nil {
		return -1, err
	}
	return index, nil
}
func (p *nativeNamespaceScaffolds) checkNode(node nativeNamespaceScaffoldNode) error {
	if node.file == nil || node.parent == nil {
		return errors.New("namespace scaffold lost original handle")
	}
	id, err := pinIdentity(int(node.file.Fd()), p.uid, p.gid, node.directory)
	if err != nil || id != node.identity {
		return errors.Join(errors.New("namespace scaffold retained object changed"), err)
	}
	flags, err := unix.FcntlInt(node.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return errors.Join(errors.New("namespace scaffold lost CLOEXEC"), err)
	}
	member, err := namespaceInventoryMemberAt(int(node.parent.Fd()), node.name)
	if err != nil || member.identity != node.identity {
		return errors.Join(errors.New("namespace scaffold original named mapping replaced"), err)
	}
	if !node.directory {
		if member.size != 0 {
			return errors.New("namespace scaffold lock is not empty")
		}
		var b [1]byte
		n, e := node.file.ReadAt(b[:], 0)
		if n != 0 || !errors.Is(e, io.EOF) {
			return errors.Join(errors.New("namespace scaffold lock gained content"), e)
		}
	}
	return nil
}
func nativeNamespaceScaffoldNames(ctx context.Context, file *os.File, used *int) (names []string, members map[string]namespaceInventoryMember, result error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	fd, err := unix.Openat(int(file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	read := os.NewFile(uintptr(fd), "namespace-scaffold-roster")
	defer func() { result = errors.Join(result, read.Close()) }()
	remaining := min(nativeOriginCatalogLimit, nativeNamespaceScaffoldMembers-*used)
	if remaining < 0 {
		return nil, nil, errors.New("namespace scaffold aggregate member limit")
	}
	names, err = read.Readdirnames(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	if len(names) > remaining {
		return nil, nil, errors.New("namespace scaffold member limit")
	}
	*used += len(names)
	members = make(map[string]namespaceInventoryMember, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if _, ok := members[name]; ok {
			return nil, nil, errors.New("duplicate namespace scaffold member")
		}
		member, err := namespaceInventoryMemberAt(fd, name)
		if err != nil {
			return nil, nil, err
		}
		members[name] = member
	}
	slices.Sort(names)
	return names, members, ctx.Err()
}
func (p *nativeNamespaceScaffolds) roster(ctx context.Context, index int, used *int) ([]string, error) {
	names, members, err := nativeNamespaceScaffoldNames(ctx, p.nodes[index].file, used)
	if err != nil {
		return nil, err
	}
	p.rosters = append(p.rosters, nativeNamespaceScaffoldRoster{index, names, members})
	return names, nil
}
func (p *nativeNamespaceScaffolds) check(ctx context.Context) error {
	if p.closed || !p.complete || p.scope == nil {
		return errors.New("namespace scaffold observation unavailable/closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// All external storage/history work precedes the final live metadata scan.
	if err := p.scope.Check(ctx); err != nil {
		return err
	}
	// Keep the original borrowed source live through every local FD use.
	p.scope.mu.Lock()
	defer p.scope.mu.Unlock()
	if err := p.scope.usable(); err != nil {
		return err
	}
	if p.control != nil {
		id, err := pinIdentity(int(p.control.Fd()), p.uid, p.gid, true)
		if err != nil || id != p.controlID {
			return errors.Join(errors.New("namespace scaffold lost borrowed original control"), err)
		}
		if p.view.collectionMissing {
			var st unix.Stat_t
			if err := unix.Fstatat(int(p.control.Fd()), "namespace-pins", &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
				return errors.Join(errors.New("namespace scaffold collection appeared/absence unavailable"), err)
			}
		}
	}
	used := 0
	for pass := 0; pass < 2; pass++ {
		used = 0
		for _, node := range p.nodes {
			if err := p.checkNode(node); err != nil {
				return err
			}
		}
		for _, roster := range p.rosters {
			names, members, err := nativeNamespaceScaffoldNames(ctx, p.nodes[roster.node].file, &used)
			if err != nil {
				return err
			}
			if !slices.Equal(names, roster.names) || !maps.Equal(members, roster.members) {
				return errors.New("namespace scaffold complete original roster/metadata changed")
			}
		}
	}
	// Mapping-only original scope completion under the SAME held lifetime.
	// No external verifier after sibling validation; no atomic-snapshot claim.
	if err := p.scope.usable(); err != nil {
		return err
	}
	if err := p.scope.finalAncestryMappings(ctx); err != nil {
		return err
	}
	return ctx.Err()
}
func (p *nativeNamespaceScaffolds) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil namespace scaffold observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	if ctx == nil {
		p.failure = errors.New("missing namespace scaffold check context")
		return p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	p.failure = p.check(ctx)
	return p.failure
}
func (p *nativeNamespaceScaffolds) Observation(ctx context.Context) (nativeNamespaceScaffoldView, error) {
	if p == nil {
		return nativeNamespaceScaffoldView{}, errors.New("nil namespace scaffold observation")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return nativeNamespaceScaffoldView{}, p.failure
	}
	if ctx == nil {
		p.failure = errors.New("missing namespace scaffold observation context")
		return nativeNamespaceScaffoldView{}, p.failure
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if err := p.check(ctx); err != nil {
		p.failure = err
		return nativeNamespaceScaffoldView{}, err
	}
	v := p.view
	v.entries = slices.Clone(v.entries)
	return v, nil
}
func (p *nativeNamespaceScaffolds) Close() error {
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
		p.closeErr = errors.Join(p.closeErr, p.nodes[i].file.Close())
	}
	return p.closeErr
}
