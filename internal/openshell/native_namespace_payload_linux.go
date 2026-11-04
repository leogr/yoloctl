package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const nativeNamespacePayloadBytes = 32 << 20 // PER INCARNATION; aggregate owner still required
const nativeNamespacePayloadFiles = 4 * nativeOriginRecordLimit

type nativeNamespacePayloadGeneration struct {
	generation, recordedCID, recordSHA256, kernelBootID string
	kind                                                uint8 //3 origin+pin,4 no-init ONLY; not a mode/ACK/classification
	phase                                               namespacePinPhase
	retiredSchema                                       int                      //0 means marker absent, NOT missing current effects
	leaf                                                namespaceInventoryMember // copied statx ONLY; never a typed namespace
}
type nativeNamespacePayloadView struct {
	origin      containerOrigin // copied exact seven original fields
	generations []nativeNamespacePayloadGeneration
}
type nativeNamespacePayloadNode struct {
	file, parent *os.File // OWN file only; parent borrowed or another OWN node
	name         string
	directory    bool
	identity     pinFileIdentity
	member       namespaceInventoryMember
	raw          []byte
}
type nativeNamespacePayloadGroup struct {
	record                                    RuntimeRecord // cloned original immutable selector
	kind                                      uint8
	origin, directory, state, retired, noinit int //owned node indexes; -1 absent
	names                                     []string
	members                                   map[string]namespaceInventoryMember
}

// One-incarnation PRIMARY stored metadata observer, not a scope-wide union,
// actual/current CID, typed namespace, original effect/Stop/remove ACK, managed
// reference census or classifier. It closes only its OWN new descriptors.
// Original source/scaffold/record owners MUST remain live through finalization.
type nativeNamespacePayload struct {
	mu                                 sync.Mutex
	lifetime                           context.Context
	source                             *nativeOriginScope
	scaffold                           *nativeNamespaceScaffolds //BORROWED
	records                            *nativeOriginRecords      //BORROWED
	origin                             containerOrigin
	uid, gid                           int
	incarnation, origins, pins, noinit nativeNamespaceScaffoldNode //BORROWED originals
	groups                             []nativeNamespacePayloadGroup
	nodes                              []nativeNamespacePayloadNode //OWNED immediately on acquisition
	view                               nativeNamespacePayloadView
	storedBytes                        int
	complete, closed                   bool
	failure, closeErr                  error
}

func (*nativeNamespacePayload) String() string     { return "namespace payload metadata [not authority]" }
func (p *nativeNamespacePayload) GoString() string { return p.String() }

func observeNativeNamespacePayload(ctx context.Context, source *nativeOriginScope, scaffold *nativeNamespaceScaffolds, records *nativeOriginRecords) (_ *nativeNamespacePayload, result error) {
	if ctx == nil || source == nil || scaffold == nil || records == nil {
		return nil, errors.New("payload lacks SAME original context/sources")
	}
	p := &nativeNamespacePayload{lifetime: ctx, source: source, scaffold: scaffold, records: records}
	defer func() {
		if result != nil {
			result = errors.Join(result, p.Close())
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if err := p.external(ctx); err != nil {
		return nil, err
	}
	if err := p.withSources(ctx, func() error {
		if err := records.checkLocal(ctx); err != nil {
			return err
		}
		if err := nativeNamespacePayloadScaffoldLocal(ctx, scaffold); err != nil {
			return err
		}
		if err := p.capture(ctx); err != nil {
			return err
		}
		p.complete = true
		return p.local(ctx)
	}); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *nativeNamespacePayload) usable(ctx context.Context) error {
	if p == nil || p.closed || p.source == nil || p.scaffold == nil || p.records == nil || p.lifetime == nil {
		return errors.New("payload observer expired/incomplete")
	}
	if p.failure != nil {
		return p.failure
	}
	if err := p.lifetime.Err(); err != nil {
		return errors.Join(errors.New("payload original execution expired"), err, context.Cause(p.lifetime))
	}
	if ctx == nil {
		return errors.New("payload context missing")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}
func (p *nativeNamespacePayload) external(ctx context.Context) error {
	if err := p.usable(ctx); err != nil {
		return err
	}
	if err := p.scaffold.Check(ctx); err != nil {
		return err
	}
	return p.records.Check(ctx) //ALL external callbacks precede final local pass
}

// Fixed lock order: payload.mu -> records.mu -> scaffold.mu -> SAME source.mu
// -> catalog.mu OR ancestry.mu. No source.Check/resolver/external callback here.
// Existing external callbacks must never recursively acquire these observers.
func (p *nativeNamespacePayload) withSources(ctx context.Context, use func() error) error {
	if err := p.usable(ctx); err != nil {
		return err
	}
	r, s := p.records, p.scaffold
	r.mu.Lock()
	defer r.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	p.source.mu.Lock()
	defer p.source.mu.Unlock()
	if r.source != p.source || s.scope != p.source {
		return errors.New("payload borrowed sources differ from SAME original scope")
	}
	if err := r.usable(); err != nil {
		return err
	}
	if s.closed || !s.complete || s.failure != nil {
		return errors.Join(errors.New("original scaffold expired/incomplete"), s.failure)
	}
	if err := p.source.usable(); err != nil {
		return err
	}
	if err := p.usable(ctx); err != nil {
		return err
	}
	return use()
}
func nativeNamespacePayloadBorrow(s *nativeNamespaceScaffolds, parent *os.File, name string) (nativeNamespaceScaffoldNode, error) {
	var got nativeNamespaceScaffoldNode
	for _, node := range s.nodes {
		if node.parent == parent && node.name == name {
			if got.file != nil {
				return got, errors.New("duplicate borrowed scaffold selector")
			}
			got = node
		}
	}
	if got.file == nil || !got.directory {
		return got, errors.New("missing ORIGINAL scaffold directory selector")
	}
	return got, nil
}
func nativeNamespacePayloadSourceRoster(s *nativeNamespaceScaffolds, file *os.File) ([]string, error) {
	for _, roster := range s.rosters {
		if roster.node >= 0 && roster.node < len(s.nodes) && s.nodes[roster.node].file == file {
			return slices.Clone(roster.names), nil
		}
	}
	return nil, errors.New("missing complete ORIGINAL scaffold roster")
}
func (p *nativeNamespacePayload) capture(ctx context.Context) error {
	r, s := p.records, p.scaffold
	p.origin = r.entry.origin
	p.uid, p.gid = r.uid, r.gid
	if s.uid != p.uid || s.gid != p.gid || s.control == nil || !r.current.record.Launch.UnconfinedNetwork {
		return errors.New("payload original source/owner/unconfined scope differs")
	}
	found := 0
	for _, entry := range s.view.entries {
		if entry.incarnation == p.origin.Incarnation {
			found++
			if entry.bareMetadata {
				return errors.New("bare scaffold is a SEPARATE neutral observation, not a payload")
			}
		}
	}
	if found != 1 {
		return errors.New("payload incarnation missing/repeated in complete scaffold source")
	}
	root, err := nativeNamespacePayloadBorrow(s, s.control, "namespace-pins")
	if err != nil {
		return err
	}
	p.incarnation, err = nativeNamespacePayloadBorrow(s, root.file, p.origin.Incarnation)
	if err != nil {
		return err
	}
	p.origins, err = nativeNamespacePayloadBorrow(s, p.incarnation.file, "origins")
	if err != nil {
		return err
	}
	p.pins, err = nativeNamespacePayloadBorrow(s, p.incarnation.file, "pins")
	if err != nil {
		return err
	}
	incNames, err := nativeNamespacePayloadSourceRoster(s, p.incarnation.file)
	if err != nil {
		return err
	}
	if slices.Contains(incNames, noInitializationDirectory) {
		p.noinit, err = nativeNamespacePayloadBorrow(s, p.incarnation.file, noInitializationDirectory)
		if err != nil {
			return err
		}
	}
	groups := map[string]uint8{}
	for _, kind := range []struct {
		node nativeNamespaceScaffoldNode
		bit  uint8
	}{{p.origins, 1}, {p.pins, 2}, {p.noinit, 4}} {
		if kind.node.file == nil {
			continue
		}
		names, e := nativeNamespacePayloadSourceRoster(s, kind.node.file)
		if e != nil {
			return e
		}
		for _, name := range names {
			generation := name
			if kind.bit != 2 {
				generation = strings.TrimSuffix(name, ".json")
				if generation+".json" != name {
					return errors.New("invalid payload metadata suffix")
				}
			}
			if !namespacePinGenerationName(generation) {
				return errors.New("invalid payload generation")
			}
			groups[generation] |= kind.bit
		}
	}
	if len(r.values) == 0 || len(r.values) > nativeOriginRecordLimit || len(groups) != len(r.values) {
		return errors.New("payload omits/orphans retained original generations")
	}
	p.groups = make([]nativeNamespacePayloadGroup, len(r.values))
	p.view.origin = p.origin
	for i, value := range r.values {
		record := value.value.record
		if record.Generation != value.name || !record.Launch.UnconfinedNetwork || generationOrigin(record) != p.origin {
			return errors.New("payload original record selector differs")
		}
		kind := groups[value.name]
		if kind != 3 && kind != 4 {
			return errors.New("payload conflicting/incomplete/orphan group")
		}
		delete(groups, value.name)
		g := &p.groups[i]
		*g = nativeNamespacePayloadGroup{record: cloneRuntimeRecord(record), kind: kind, origin: -1, directory: -1, state: -1, retired: -1, noinit: -1}
		if kind == 4 {
			if p.noinit.file == nil {
				return errors.New("payload no-init parent missing")
			}
			g.noinit, err = p.add(ctx, p.noinit.file, value.name+".json", false, 4096)
		} else {
			g.origin, err = p.add(ctx, p.origins.file, value.name+".json", false, 4<<20)
			if err != nil {
				return err
			}
			// A generation can legitimately be a self-bind. NO blanket NO_XDEV here.
			g.directory, err = p.add(ctx, p.pins.file, value.name, true, 0)
			if err != nil {
				return err
			}
			used := 0
			g.names, g.members, err = nativeNamespaceScaffoldNames(ctx, p.nodes[g.directory].file, &used)
			if err != nil {
				return err
			}
			if !slices.Equal(g.names, []string{"namespace", "state.json"}) && !slices.Equal(g.names, []string{"namespace", "retired.json", "state.json"}) {
				return errors.New("payload generation has missing/unknown bookkeeping members")
			}
			if err = nativeNamespacePayloadLeaf(g.members["namespace"]); err != nil {
				return err
			}
			// namespace LEAF is NEVER opened/read/duplicated/adopted; only statx above.
			g.state, err = p.add(ctx, p.nodes[g.directory].file, "state.json", false, 4<<20)
			if err != nil {
				return err
			}
			if slices.Contains(g.names, "retired.json") {
				g.retired, err = p.add(ctx, p.nodes[g.directory].file, "retired.json", false, 4096)
			}
		}
		if err != nil {
			return err
		}
		view, e := p.decodeGroup(g)
		if e != nil {
			return e
		}
		p.view.generations = append(p.view.generations, view)
	}
	if len(groups) != 0 {
		return errors.New("payload contains orphan generation")
	}
	return nil
}
func (p *nativeNamespacePayload) add(ctx context.Context, parent *os.File, name string, directory bool, limit int) (int, error) {
	if parent == nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || len(p.nodes) >= nativeNamespacePayloadFiles {
		return -1, errors.New("payload invalid/unbounded fixed child")
	}
	flags := uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK)
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: flags, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return -1, err
	}
	p.nodes = append(p.nodes, nativeNamespacePayloadNode{file: os.NewFile(uintptr(fd), "namespace-payload-original"), parent: parent, name: name, directory: directory})
	index := len(p.nodes) - 1
	n := &p.nodes[index]
	n.identity, err = pinIdentity(fd, p.uid, p.gid, directory)
	if err != nil {
		return -1, err
	}
	n.member, err = namespaceInventoryMemberAt(int(parent.Fd()), name)
	if err != nil {
		return -1, err
	}
	if err = p.checkNode(n); err != nil {
		return -1, err
	}
	if !directory {
		remaining := nativeNamespacePayloadBytes - p.storedBytes
		n.raw, err = nativeNamespacePayloadRead(ctx, n.file, limit, remaining)
		if err != nil {
			return -1, err
		}
		p.storedBytes += len(n.raw)
	}
	return index, nil
}
func nativeNamespacePayloadRead(ctx context.Context, file *os.File, limit, remaining int) ([]byte, error) {
	if ctx == nil || file == nil || limit < 0 || remaining < 0 {
		return nil, errors.New("payload bytes unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	bound := min(limit, remaining)
	raw, err := io.ReadAll(io.NewSectionReader(file, 0, int64(bound)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > bound {
		return nil, errors.New("empty/unbounded payload metadata")
	}
	return raw, nil
}
func (p *nativeNamespacePayload) checkNode(n *nativeNamespacePayloadNode) error {
	if n.file == nil || n.parent == nil {
		return errors.New("payload lost owned/original handle")
	}
	id, err := pinIdentity(int(n.file.Fd()), p.uid, p.gid, n.directory)
	if err != nil || id != n.identity {
		return errors.Join(errors.New("payload original handle changed"), err)
	}
	flags, err := unix.FcntlInt(n.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return errors.Join(errors.New("payload lost CLOEXEC"), err)
	}
	named, err := namespaceInventoryMemberAt(int(n.parent.Fd()), n.name)
	if err != nil || named.identity != n.identity || named != n.member {
		return errors.Join(errors.New("payload original named mapping/metadata changed"), err)
	}
	return nil
}

// Source creates a0600 empty regular placeholder; a live Linux namespace leaf
// is a0444 regular namespace-like inode. This is only a finite METADATA profile,
// NOT an NSFS/type/placeholder proof. Do not impose a blanket leaf UID/dev/mount
// comparison or claim which alternative it is; actual typed checks are separate.
func nativeNamespacePayloadLeaf(member namespaceInventoryMember) error {
	if member.mode == unix.S_IFREG|0600 && member.size == 0 && member.links == 1 {
		return nil
	}
	if member.mode == unix.S_IFREG|0444 {
		return nil
	}
	return errors.New("payload namespace leaf has unknown metadata kind/mode")
}
func (p *nativeNamespacePayload) decodeGroup(g *nativeNamespacePayloadGroup) (nativeNamespacePayloadGeneration, error) {
	view := nativeNamespacePayloadGeneration{generation: g.record.Generation, kind: g.kind, recordSHA256: recordIdentity(g.record)}
	if g.kind == 4 {
		raw := p.nodes[g.noinit].raw
		var candidate namespaceNoInitialization
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return nativeNamespacePayloadGeneration{}, err
		}
		if err := namespaceRetiredCanonicalMetadata(raw, candidate); err != nil {
			return nativeNamespacePayloadGeneration{}, err
		}
		got, err := decodeNoInitialization(raw, g.record, candidate.ContainerID)
		if err != nil {
			return nativeNamespacePayloadGeneration{}, err
		}
		view.recordedCID, view.kernelBootID = got.ContainerID, got.KernelBootID
		return view, nil
	}
	originRaw, stateRaw := p.nodes[g.origin].raw, p.nodes[g.state].raw
	var candidate namespacePinOrigin
	if err := json.Unmarshal(originRaw, &candidate); err != nil {
		return nativeNamespacePayloadGeneration{}, err
	}
	if err := namespaceRetiredCanonicalMetadata(originRaw, candidate); err != nil {
		return nativeNamespacePayloadGeneration{}, err
	}
	cid := candidate.Record.Scope.ContainerID //STORED CID ONLY, not actual engine proof
	origin, err := decodeNamespacePinOrigin(originRaw, g.record, cid)
	if err != nil {
		return nativeNamespacePayloadGeneration{}, err
	}
	state, err := decodeNamespacePinRecord(stateRaw, g.record, cid, origin.Record.OriginSHA256)
	if err != nil {
		return nativeNamespacePayloadGeneration{}, err
	}
	if err := namespaceRetiredCanonicalMetadata(stateRaw, state); err != nil {
		return nativeNamespacePayloadGeneration{}, err
	}
	view.recordedCID, view.kernelBootID, view.phase, view.leaf = cid, state.Scope.KernelBootID, state.Phase, g.members["namespace"]
	if g.retired >= 0 {
		expected, e := namespaceRetiredBindingFromMetadata(originRaw, stateRaw, g.record, cid)
		if e != nil {
			return nativeNamespacePayloadGeneration{}, e
		}
		marker, e := decodeNamespaceRetiredMetadata(p.nodes[g.retired].raw, expected)
		if e != nil {
			return nativeNamespacePayloadGeneration{}, e
		}
		view.retiredSchema = marker.Schema
	}
	return view, nil //active foreign state without retired marker is VALID
}

// New-file factoring only: accepted scaffold.check's local retained scan,
// WITHOUT source.Check/external work. Caller holds scaffold.mu+source.mu.
func nativeNamespacePayloadScaffoldLocal(ctx context.Context, s *nativeNamespaceScaffolds) error {
	if s.closed || !s.complete || s.failure != nil || s.control == nil {
		return errors.Join(errors.New("payload scaffold unavailable"), s.failure)
	}
	id, err := pinIdentity(int(s.control.Fd()), s.uid, s.gid, true)
	if err != nil || id != s.controlID {
		return errors.Join(errors.New("payload original control changed"), err)
	}
	if s.view.collectionMissing {
		return errors.New("payload collection was originally missing")
	}
	for pass := 0; pass < 2; pass++ {
		for _, node := range s.nodes {
			if err := s.checkNode(node); err != nil {
				return err
			}
		}
		used := 0
		for _, roster := range s.rosters {
			if roster.node < 0 || roster.node >= len(s.nodes) {
				return errors.New("payload scaffold roster selector changed")
			}
			names, members, e := nativeNamespaceScaffoldNames(ctx, s.nodes[roster.node].file, &used)
			if e != nil {
				return e
			}
			if !slices.Equal(names, roster.names) || !maps.Equal(members, roster.members) {
				return errors.New("payload complete original scaffold roster changed")
			}
		}
	}
	if err := s.scope.usable(); err != nil {
		return err
	}
	return s.scope.finalAncestryMappings(ctx)
}
func (p *nativeNamespacePayload) local(ctx context.Context) error {
	if err := p.usable(ctx); err != nil {
		return err
	}
	if !p.complete || p.records.entry.origin != p.origin || len(p.groups) != len(p.records.values) || len(p.view.generations) != len(p.groups) {
		return errors.New("payload original immutable selectors changed/incomplete")
	}
	for pass := 0; pass < 2; pass++ {
		if err := p.records.checkLocal(ctx); err != nil {
			return err
		}
		if err := nativeNamespacePayloadScaffoldLocal(ctx, p.scaffold); err != nil {
			return err
		}
		remaining := nativeNamespacePayloadBytes
		for i := range p.nodes {
			n := &p.nodes[i]
			if err := p.checkNode(n); err != nil {
				return err
			}
			if !n.directory {
				limit := 4 << 20
				if n.name == "retired.json" || n.parent == p.noinit.file {
					limit = 4096
				}
				raw, err := nativeNamespacePayloadRead(ctx, n.file, limit, remaining)
				if err != nil || !bytes.Equal(raw, n.raw) {
					return errors.Join(errors.New("payload original metadata bytes changed"), err)
				}
				remaining -= len(raw)
			}
		}
		for i := range p.groups {
			g := &p.groups[i]
			value := p.records.values[i]
			if value.name != g.record.Generation || recordIdentity(value.value.record) != recordIdentity(g.record) {
				return errors.New("payload original qualified generation changed")
			}
			if g.kind == 3 {
				used := 0
				names, members, err := nativeNamespaceScaffoldNames(ctx, p.nodes[g.directory].file, &used)
				if err != nil {
					return err
				}
				if !slices.Equal(names, g.names) || !maps.Equal(members, g.members) {
					return errors.New("payload original generation roster/leaf metadata changed")
				}
				if err := nativeNamespacePayloadLeaf(members["namespace"]); err != nil {
					return err
				}
			}
			got, err := p.decodeGroup(g)
			if err != nil || got != p.view.generations[i] {
				return errors.Join(errors.New("payload original stored join changed"), err)
			}
		}
		for i := len(p.nodes) - 1; i >= 0; i-- {
			if err := p.checkNode(&p.nodes[i]); err != nil {
				return err
			}
		}
	}
	if err := p.records.sourceMappings(ctx); err != nil {
		return err
	}
	if err := p.source.finalAncestryMappings(ctx); err != nil {
		return err
	}
	return p.usable(ctx)
}
func (p *nativeNamespacePayload) check(ctx context.Context, external bool) (nativeNamespacePayloadView, error) {
	if err := p.usable(ctx); err != nil {
		return nativeNamespacePayloadView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativeOriginReadBudget)
	defer cancel()
	if external {
		if err := p.external(ctx); err != nil {
			return nativeNamespacePayloadView{}, err
		}
	}
	var got nativeNamespacePayloadView
	err := p.withSources(ctx, func() error {
		if err := p.local(ctx); err != nil {
			return err
		}
		// Only copied metadata assignment follows, never an arbitrary final callback.
		got = p.view
		got.generations = slices.Clone(p.view.generations)
		return p.usable(ctx)
	})
	if err != nil {
		return nativeNamespacePayloadView{}, err
	}
	return got, nil
}
func (p *nativeNamespacePayload) Check(ctx context.Context) error {
	if p == nil {
		return errors.New("nil payload observer")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.check(ctx, true)
	if err != nil {
		p.failure = err
	}
	return err
}

// No external callback; all SAME borrowed owners stay live under source.mu.
// Aggregate owner must independently run all historical/document/native checks
// BEFORE this phase and retain every observer until its final local completion.
func (p *nativeNamespacePayload) CheckOriginal(ctx context.Context) error {
	if p == nil {
		return errors.New("nil payload observer")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.check(ctx, false)
	if err != nil {
		p.failure = err
	}
	return err
}
func (p *nativeNamespacePayload) Observation(ctx context.Context) (nativeNamespacePayloadView, error) {
	if p == nil {
		return nativeNamespacePayloadView{}, errors.New("nil payload observer")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	got, err := p.check(ctx, true)
	if err != nil {
		p.failure = err
		return nativeNamespacePayloadView{}, err
	}
	return got, nil
}
func (p *nativeNamespacePayload) Close() error {
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
			p.nodes[i].file = nil
		}
		p.nodes[i].raw = nil
	}
	// Drop borrowed references, NEVER close source/scaffold/records or their FDs.
	p.source, p.scaffold, p.records = nil, nil, nil
	return p.closeErr
}
