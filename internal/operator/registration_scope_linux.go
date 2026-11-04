package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

type RegistrationScopeKind uint8

const (
	RegistrationScopeRootPresent RegistrationScopeKind = iota + 1
	RegistrationScopePrefixAbsent
)

type RegistrationScopeObservation struct {
	Kind             RegistrationScopeKind
	MissingComponent string                 //fixed run/yoloctl/operators ONLY, not an effect/ACK
	Identities       []RegistrationIdentity //ALL same-box, including keep
}
type registrationScopeNode struct {
	file *os.File
	name string
	id   endpointFileID
}

// Own protected original prefix FDs and optional actual root inventory only.
// No path/expected-empty/current-CID factory. Missing is exact ENOENT beneath
// an existing retained original parent, never a failed whole-path open.
type RegistrationScope struct {
	mu                sync.Mutex
	lifetime          context.Context
	verifyCurrent     func(context.Context) error
	nodes             []registrationScopeNode
	inventory         *RegistrationInventory
	missing           int //fixed component index1..3; zero means actual operators root present
	uid, gid          uint32
	closed            bool
	failure, closeErr error
}

func ObserveRegistrationScope(ctx context.Context, verifyCurrent func(context.Context) error) (*RegistrationScope, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, errors.New("operator scope requires original native infrastructure")
	}
	return observeRegistrationScopeAt(ctx, "/", 0, 0, verifyCurrent)
}
func observeRegistrationScopeAt(ctx context.Context, rootPath string, uid, gid int, verifyCurrent func(context.Context) error) (_ *RegistrationScope, err error) {
	if ctx == nil || verifyCurrent == nil || uid < 0 || gid < 0 || !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return nil, errors.New("registration scope lacks original lifetime/current storage")
	}
	p := &RegistrationScope{lifetime: ctx, verifyCurrent: verifyCurrent, uid: uint32(uid), gid: uint32(gid)}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	if err = verifyCurrent(ctx); err != nil {
		return nil, err
	}
	fd, err := unix.Open(rootPath, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	p.nodes = append(p.nodes, registrationScopeNode{file: os.NewFile(uintptr(fd), "operator-prefix-original"), name: rootPath})
	p.nodes[0].id, err = endpointID(fd, "")
	if err != nil || !p.trusted(p.nodes[0].id) {
		return nil, errors.Join(err, errors.New("operator original root untrusted"))
	}
	for index, name := range []string{"run", "yoloctl", "operators"} {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		parent := p.nodes[len(p.nodes)-1].file
		id, lookupErr := endpointID(int(parent.Fd()), name)
		if lookupErr == unix.ENOENT {
			p.missing = index + 1
			break
		}
		if lookupErr != nil || !p.trusted(id) {
			return nil, errors.Join(lookupErr, errors.New("operator prefix unavailable/untrusted"))
		}
		fd, openErr := unix.Openat2(int(parent.Fd()), name, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
		if openErr != nil {
			return nil, openErr
		}
		node := registrationScopeNode{file: os.NewFile(uintptr(fd), "operator-prefix-original"), name: name, id: id}
		p.nodes = append(p.nodes, node)
		own, idErr := endpointID(fd, "")
		if idErr != nil || !id.sameDirectory(own) {
			return nil, errors.Join(idErr, errors.New("operator prefix changed during first capture"))
		}
	}
	if p.missing == 0 {
		original := p.nodes[len(p.nodes)-1]
		p.inventory, err = observeRegistrationInventory(ctx, func() (*os.File, error) {
			// Original retained parent, never a fresh current-root adoption. Existing
			// inventory mapping checks repeat this exact current-name relation.
			return openEndpointDirectory(int(p.nodes[len(p.nodes)-2].file.Fd()), "operators", false)
		}, uid, gid, verifyCurrent)
		if err != nil {
			return nil, err
		}
		if !original.id.sameDirectory(p.inventory.rootID) {
			return nil, errors.New("operator FIRST root was replaced before inventory")
		}
	}
	if err = p.check(ctx, true); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *RegistrationScope) trusted(id endpointFileID) bool {
	return id.mode&unix.S_IFMT == unix.S_IFDIR && id.uid == p.uid && id.gid == p.gid && id.mode&0o7022 == 0
}
func (p *RegistrationScope) local(ctx context.Context) error {
	if p == nil || p.closed || p.lifetime == nil || len(p.nodes) == 0 || p.verifyCurrent == nil {
		return errors.New("original registration scope unavailable")
	}
	if p.failure != nil {
		return p.failure
	}
	if err := errors.Join(p.lifetime.Err(), ctx.Err()); err != nil {
		return err
	}
	for i, node := range p.nodes {
		id, err := endpointID(int(node.file.Fd()), "")
		if err != nil || !p.trusted(id) || !node.id.sameDirectory(id) {
			return errors.Join(err, errors.New("original operator prefix handle changed"))
		}
		parent := unix.AT_FDCWD
		if i > 0 {
			parent = int(p.nodes[i-1].file.Fd())
		}
		named, err := endpointID(parent, node.name)
		if err != nil || !node.id.sameDirectory(named) {
			return errors.Join(err, errors.New("original operator prefix mapping changed"))
		}
	}
	if p.missing != 0 {
		if p.inventory != nil || p.missing < 1 || p.missing > 3 || len(p.nodes) != p.missing {
			return errors.New("operator absent-prefix original selector changed")
		}
		name := []string{"run", "yoloctl", "operators"}[p.missing-1]
		if _, err := endpointID(int(p.nodes[len(p.nodes)-1].file.Fd()), name); err != unix.ENOENT {
			return errors.Join(err, errors.New("original missing operator prefix no longer absent"))
		}
	} else {
		if p.inventory == nil || len(p.nodes) != 4 {
			return errors.New("operator present-root inventory missing")
		}
		p.inventory.mu.Lock()
		defer p.inventory.mu.Unlock()
		if p.inventory.closed || p.inventory.failure != nil || !p.nodes[3].id.sameDirectory(p.inventory.rootID) {
			return errors.Join(p.inventory.failure, errors.New("original registration inventory expired/replaced"))
		}
		if err := p.inventory.checkMapping(); err != nil {
			return err
		}
		if err := p.inventory.checkEntries(ctx); err != nil {
			return err
		}
		if err := p.inventory.checkMapping(); err != nil {
			return err
		}
	}
	return errors.Join(p.lifetime.Err(), ctx.Err())
}
func (p *RegistrationScope) check(ctx context.Context, external bool) error {
	if ctx == nil {
		return errors.New("operator scope context missing")
	}
	if err := p.local(ctx); err != nil {
		return err
	}
	if external {
		if err := p.verifyCurrent(ctx); err != nil {
			return err
		}
	}
	return p.local(ctx) //ALL original prefixes + exact absence/complete entries
}
func (p *RegistrationScope) CheckOriginal(ctx context.Context) error {
	if p == nil {
		return errors.New("nil registration scope")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.check(ctx, false)
	if err != nil {
		p.failure = err
	}
	return err
}
func (p *RegistrationScope) Observation(ctx context.Context, box string) (RegistrationScopeObservation, error) {
	if p == nil {
		return RegistrationScopeObservation{}, errors.New("nil registration scope")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if layout.ValidateName("agentbox", box) != nil {
		return RegistrationScopeObservation{}, errors.New("invalid selected operator box")
	}
	if err := p.check(ctx, true); err != nil {
		p.failure = err
		return RegistrationScopeObservation{}, err
	}
	if p.missing != 0 {
		return RegistrationScopeObservation{Kind: RegistrationScopePrefixAbsent, MissingComponent: []string{"run", "yoloctl", "operators"}[p.missing-1]}, nil
	}
	identities, err := p.inventory.IdentitiesForBox(box, "")
	if err != nil {
		p.failure = err
		return RegistrationScopeObservation{}, err
	}
	return RegistrationScopeObservation{Kind: RegistrationScopeRootPresent, Identities: identities}, nil
}
func (p *RegistrationScope) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	if p.inventory != nil {
		p.closeErr = errors.Join(p.closeErr, p.inventory.Close())
	}
	for i := len(p.nodes) - 1; i >= 0; i-- {
		p.closeErr = errors.Join(p.closeErr, p.nodes[i].file.Close())
	}
	return p.closeErr
}
