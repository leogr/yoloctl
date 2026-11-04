package operator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// RegistrationInventory is a complete bounded observation of registration
// IDENTITIES, including other incarnations and boxes. It grants no deletion,
// listener/stream, container ownership or negative-consumer authority. Client
// and socket members still need their independent full withdrawal preflight.
// A missing root is an error, never a fabricated empty inventory.
type RegistrationInventory struct {
	mu                sync.Mutex
	root              *os.File
	rootID            endpointFileID
	open              func() (*os.File, error)
	entries           map[string]registrationIdentity
	uid, gid          uint32
	closed            bool
	failure, closeErr error
}

type registrationIdentity struct {
	tagged              RegistrationIdentity
	directory, identity endpointFileID
	raw                 []byte // complete original nonsecret tagged identity
}

func (*RegistrationInventory) String() string   { return "<operator registration identities>" }
func (*RegistrationInventory) GoString() string { return "<operator registration identities>" }

// ObserveRegistrationInventory runs before lifecycle effects. verifyCurrent
// belongs to admitted native storage/kernel/owner checks, not desired config.
func ObserveRegistrationInventory(ctx context.Context, verifyCurrent func(context.Context) error) (*RegistrationInventory, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("operator inventory requires native VM-root infrastructure")
	}
	return observeRegistrationInventory(ctx, openRegistrationInventoryRoot, 0, 0, verifyCurrent)
}

func openRegistrationInventoryRoot() (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range []string{"", "run", "yoloctl", "operators"} {
		if component != "" {
			next, err := unix.Openat2(fd, component, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
			_ = unix.Close(fd)
			if err != nil {
				return nil, err
			}
			fd = next
		}
		var s unix.Stat_t
		if unix.Fstat(fd, &s) != nil || s.Uid != 0 || s.Gid != 0 || s.Mode&0o7022 != 0 {
			_ = unix.Close(fd)
			return nil, errors.New("operator inventory has an untrusted ancestor")
		}
	}
	return os.NewFile(uintptr(fd), "operator inventory root"), nil
}

func observeRegistrationInventory(ctx context.Context, open func() (*os.File, error), uid, gid int, verifyCurrent func(context.Context) error) (_ *RegistrationInventory, err error) {
	if open == nil || verifyCurrent == nil {
		return nil, errors.New("operator inventory lacks current mapping/owner checks")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyCurrent(ctx); err != nil {
		return nil, err
	}
	p := &RegistrationInventory{open: open, uid: uint32(uid), gid: uint32(gid)}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	p.root, err = open()
	if err != nil {
		return nil, err
	}
	if p.root == nil {
		return nil, errors.New("operator inventory lacks an original root")
	}
	p.rootID, err = endpointID(int(p.root.Fd()), "")
	if err != nil || !p.trustedRoot(p.rootID) {
		return nil, errors.New("operator inventory root is untrusted")
	}
	p.entries, err = p.capture(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.check(ctx, verifyCurrent); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *RegistrationInventory) trustedRoot(i endpointFileID) bool {
	return i.mode&unix.S_IFMT == unix.S_IFDIR && i.mode&0o7022 == 0 && i.uid == p.uid && i.gid == p.gid
}

func (p *RegistrationInventory) names() ([]string, error) {
	f, err := openEndpointDirectory(int(p.root.Fd()), ".", true)
	if err != nil {
		return nil, err
	}
	names, readErr := f.Readdirnames(1025)
	closeErr := f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(names) > 1024 {
		return nil, errors.New("operator registration identity inventory exceeds bound")
	}
	sort.Strings(names)
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			return nil, errors.New("operator identity inventory has duplicate directory entries")
		}
	}
	return names, nil
}

func (p *RegistrationInventory) capture(ctx context.Context) (map[string]registrationIdentity, error) {
	names, err := p.names()
	if err != nil {
		return nil, err
	}
	entries := make(map[string]registrationIdentity, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Validate a single actual incarnation component BEFORE path traversal.
		if (Endpoint{Box: "inventory", Incarnation: name}).Validate() != nil {
			return nil, errors.New("operator inventory contains an invalid registration name")
		}
		entry, err := p.readIdentity(name)
		if err != nil {
			return nil, err
		}
		entries[name] = entry
	}
	after, err := p.names()
	if err != nil {
		return nil, err
	}
	if len(after) != len(names) {
		return nil, errors.New("operator inventory membership changed")
	}
	for n := range names {
		if after[n] != names[n] {
			return nil, errors.New("operator inventory membership changed")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (p *RegistrationInventory) readIdentity(name string) (registrationIdentity, error) {
	var empty registrationIdentity
	dir, err := openEndpointDirectory(int(p.root.Fd()), name, true)
	if err != nil {
		return empty, err
	}
	defer func() { _ = dir.Close() }()
	dirID, err := endpointID(int(dir.Fd()), "")
	if err != nil || dirID.mode&0o7777 != 0o755 || dirID.uid != p.uid || dirID.gid != p.gid || dirID.mount != p.rootID.mount {
		return empty, errors.New("operator inventory registration directory is untrusted")
	}
	identity, err := endpointID(int(dir.Fd()), "identity.json")
	if err != nil || identity.mode&unix.S_IFMT != unix.S_IFREG || identity.mode&0o7777 != 0o644 || identity.uid != p.uid || identity.gid != p.gid || identity.links != 1 || identity.mount != dirID.mount {
		return empty, errors.New("operator inventory registration identity is untrusted")
	}
	fd, err := unix.Openat2(int(dir.Fd()), "identity.json", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return empty, err
	}
	f := os.NewFile(uintptr(fd), "operator registration identity")
	defer func() { _ = f.Close() }()
	before, err := endpointID(fd, "")
	if err != nil || before != identity {
		return empty, errors.New("operator identity changed before read")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return empty, errors.New("operator registration identity is unreadable or oversized")
	}
	after, err := endpointID(fd, "")
	if err != nil || after != before {
		return empty, errors.New("operator identity changed during read")
	}
	e, err := DecodeRegistrationIdentity(raw)
	if err != nil || e.Incarnation != name {
		return empty, errors.New("operator registration name/identity is ambiguous or invalid")
	}
	named, err := endpointID(int(p.root.Fd()), name)
	if err != nil || !dirID.sameDirectory(named) {
		return empty, errors.New("operator registration mapping changed during read")
	}
	return registrationIdentity{e, dirID, identity, raw}, nil
}

func (p *RegistrationInventory) checkMapping() error {
	retained, err := endpointID(int(p.root.Fd()), "")
	if err != nil || !p.trustedRoot(retained) || !p.rootID.sameDirectory(retained) {
		return errors.New("retained operator inventory root changed")
	}
	named, err := p.open()
	if err != nil {
		return err
	}
	if named == nil {
		return errors.New("named operator inventory root is unavailable")
	}
	id, statErr := endpointID(int(named.Fd()), "")
	closeErr := named.Close()
	if statErr != nil || closeErr != nil || !p.trustedRoot(id) || !p.rootID.sameDirectory(id) {
		return errors.New("named operator inventory root mapping changed")
	}
	return nil
}

func (p *RegistrationInventory) checkEntries(ctx context.Context) error {
	current, err := p.capture(ctx)
	if err != nil {
		return err
	}
	if len(current) != len(p.entries) {
		return errors.New("complete operator identity membership changed")
	}
	for name, old := range p.entries {
		now, ok := current[name]
		if !ok || now.tagged != old.tagged || !old.directory.sameDirectory(now.directory) || now.identity != old.identity || !bytes.Equal(now.raw, old.raw) {
			return errors.New("original operator registration identity changed")
		}
	}
	return nil
}

func (p *RegistrationInventory) check(ctx context.Context, verifyCurrent func(context.Context) error) (result error) {
	if p == nil {
		return errors.New("operator identity inventory is unavailable")
	}
	if p.failure != nil {
		return p.failure
	}
	defer func() {
		if result != nil {
			p.failure = result
		}
	}()
	if ctx == nil || p.closed || p.root == nil || p.open == nil || p.entries == nil || verifyCurrent == nil {
		return errors.New("operator identity inventory/current proof is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyCurrent(ctx); err != nil {
		return err
	}
	if err := p.checkMapping(); err != nil {
		return err
	}
	if err := p.checkEntries(ctx); err != nil {
		return err
	}
	if err := verifyCurrent(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.checkMapping(); err != nil {
		return err
	}
	// No external storage verifier follows this final live member validation.
	if err := p.checkEntries(ctx); err != nil {
		return err
	}
	if err := p.checkMapping(); err != nil {
		return err
	}
	return ctx.Err()
}

func (p *RegistrationInventory) CheckCurrent(ctx context.Context, verifyCurrent func(context.Context) error) error {
	if p == nil {
		return errors.New("operator identity inventory is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.check(ctx, verifyCurrent)
}

// IdentitiesForBox returns copied complete original tagged identities for every
// same-box incarnation except keep. Current labels cannot hide old identities.
// CheckCurrent is independent; this observation supplies no cleanup permission.
func (p *RegistrationInventory) IdentitiesForBox(box, keep string) ([]RegistrationIdentity, error) {
	if p == nil {
		return nil, errors.New("operator identity inventory is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identitiesForBox(box, keep)
}

func (p *RegistrationInventory) identitiesForBox(box, keep string) ([]RegistrationIdentity, error) {
	if p.failure != nil {
		return nil, p.failure
	}
	if p.closed || p.root == nil || p.open == nil || p.entries == nil {
		return nil, errors.New("operator identity inventory is closed or uncaptured")
	}
	if err := layout.ValidateName("agentbox", box); err != nil {
		return nil, err
	}
	if keep != "" {
		if err := (Endpoint{Box: box, Incarnation: keep}).Validate(); err != nil {
			return nil, err
		}
		if e, exists := p.entries[keep]; exists && e.tagged.Box != box {
			return nil, errors.New("kept operator incarnation belongs to another box")
		}
	}
	var entries []RegistrationIdentity
	for _, e := range p.entries {
		if e.tagged.Box == box && e.tagged.Incarnation != keep {
			entries = append(entries, e.tagged)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Incarnation < entries[j].Incarnation })
	return entries, nil
}

// ForBox is legacy-only. Even a kept native identity prevents this Endpoint-only
// API from silently erasing evidence of native ownership in the same-box set.
func (p *RegistrationInventory) ForBox(box, keep string) ([]Endpoint, error) {
	if p == nil {
		return nil, errors.New("operator identity inventory is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := p.identitiesForBox(box, keep)
	if err != nil {
		return nil, err
	}
	for _, e := range p.entries {
		if e.tagged.Box == box && e.tagged.Mode != RegistrationLegacy {
			return nil, ErrNativeRegistration
		}
	}
	var legacy []Endpoint
	for _, e := range entries {
		legacy = append(legacy, e.Endpoint)
	}
	return legacy, nil
}

func (p *RegistrationInventory) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	clear(p.entries)
	p.open = nil
	if p.root != nil {
		p.closeErr = p.root.Close()
		return p.closeErr
	}
	return nil
}
