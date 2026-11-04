package managedchat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

// Store is used only below a disk already verified by the controller for UUID,
// ownership marker and live mount. There is no host path fallback. Callers must
// hold the native space mutation lock, except the narrowly read-only
// ReadManifestSnapshot used by stream authorization. No operation deletes files
// or workloads.
type Store struct {
	Disk   string
	DiskID string
	UID    int
	GID    int
	// write is a fault-injection seam; production uses durable no-follow writes.
	write func(root, rel string, raw []byte, mode os.FileMode, uid, gid int) error
	// Record, when set, receives each exact room manifest postimage before it
	// is published; its error aborts the write.
	Record func(raw []byte) error
}

func (s Store) validate() error {
	if !filepath.IsAbs(s.Disk) || filepath.Clean(s.Disk) != s.Disk || s.UID < 0 || s.GID < 0 {
		return errors.New("invalid native chat store context")
	}
	return layout.ValidateIdentity(s.DiskID)
}

func (s Store) put(rel string, raw []byte, uid, gid int) error {
	write := s.write
	if write == nil {
		write = safepath.WriteFileOwned
	}
	return write(s.Disk, rel, raw, 0o600, uid, gid)
}

func privateFile(root, rel string, limit int64, uid, gid int) ([]byte, error) {
	raw, info, err := safepath.ReadFileInfo(root, rel, limit)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(uid) || st.Gid != uint32(gid) || info.Mode().Perm() != 0o600 || st.Nlink != 1 {
		return nil, errors.New("managed chat file is not private to its expected owner")
	}
	return raw, nil
}

func (s Store) Load() (*Room, error) {
	return s.readManifest()
}

// ReadManifestSnapshot reads only the atomically replaced native room manifest,
// without the space lock. The descriptor-pinned bounded no-follow read retains
// strict decoding, owner/mode/link and disk checks. It never reads credentials,
// prepares storage or establishes applied permission: AIRC still enforces its
// loaded ACL. Mutation callers must use Load under the native mutation lock.
func (s Store) ReadManifestSnapshot() (*Room, error) {
	return s.readManifest()
}

// VerifiedManifest returns the exact room manifest bytes for a read-only
// recovery check, with Load's private-file and disk-identity checks plus the
// prepared-storage invariants a mutation requires. Callers hold the native
// mutation lock. It writes nothing and returns no credential.
func (s Store) VerifiedManifest() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := safepath.VerifyPrivateDirectory(s.Disk, "config", os.Geteuid(), os.Getegid()); err != nil {
		return nil, errors.New("managed chat native configuration store is unavailable")
	}
	raw, err := privateFile(s.Disk, layout.ChatManifest, MaxDocument, os.Geteuid(), os.Getegid())
	if err != nil {
		return nil, err
	}
	r, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if r.DiskID != s.DiskID || !r.Prepared {
		return nil, errors.New("managed chat room is not prepared on this disk")
	}
	if err := s.Verify(r); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s Store) readManifest() (*Room, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := safepath.VerifyPrivateDirectory(s.Disk, "config", os.Geteuid(), os.Getegid()); err != nil {
		return nil, errors.New("managed chat native configuration store is unavailable")
	}
	raw, err := privateFile(s.Disk, layout.ChatManifest, MaxDocument, os.Geteuid(), os.Getegid())
	if err != nil {
		return nil, err
	}
	r, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if r.DiskID != s.DiskID {
		return nil, errors.New("managed chat belongs to a different native disk")
	}
	return r, nil
}

func (s Store) save(r *Room) error {
	if r.DiskID != s.DiskID {
		return errors.New("managed chat disk identity mismatch")
	}
	raw, err := r.Marshal()
	if err != nil {
		return err
	}
	if s.Record != nil {
		if err := s.Record(raw); err != nil {
			return err
		}
	}
	return s.put(layout.ChatManifest, raw, os.Geteuid(), os.Getegid())
}

// Inventory is supplied by the controller's authoritative resolver, under the
// same mutation lock as the write. Incomplete/error inventories cannot reserve
// a server name or change membership. Runtime records are separate from files.
type Inventory struct {
	Complete     bool
	Declarations map[string]string
	Runtime      map[string]bool
}

func (i Inventory) Validate() error {
	if !i.Complete || i.Declarations == nil || i.Runtime == nil {
		return errors.New("managed chat requires a complete runtime and declaration inventory")
	}
	for name, id := range i.Declarations {
		if err := layout.ValidateName("agentbox", name); err != nil {
			return err
		}
		if id != "" {
			if err := layout.ValidateIdentity(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Ensure reserves ownership and prepares private storage, never creates/adopts
// a box or claims a server is running. Publication of the reservation precedes
// preparation, so an interrupted call retains its identities and can be inspected.
func (s Store) Ensure(server, human string, inventory Inventory) (*Room, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	if server == "" {
		server = DefaultBox
	}
	r, err := s.Load()
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(s.Disk, layout.ChatState)); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("native chat state exists without an ownership declaration; recovery required")
		}
		if _, exists := inventory.Declarations[server]; exists || inventory.Runtime[server] {
			return nil, errors.New("managed chat server name conflicts with an existing box")
		}
		r = &Room{Schema: 1, ID: NewID(), DiskID: s.DiskID, Server: Box{server, NewID()}, Channel: DefaultChannel,
			Generation: 1, Human: Member{ID: NewID(), Name: human}}
		if err := s.save(r); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if r.Server.Name != server || r.Human.Name != human {
		return nil, errors.New("managed chat already has a different owner configuration")
	}
	if id, exists := inventory.Declarations[server]; exists && id != r.Server.ID {
		return nil, errors.New("managed chat server declaration identity conflicts with its reservation")
	}
	// This foundation has no runtime projection/identity proof yet. Never adopt
	// a container by name while that integration is absent.
	if inventory.Runtime[server] {
		return nil, errors.New("managed chat runtime application is not available in this slice")
	}
	prepare := s.Prepare
	if r.Prepared {
		prepare = s.Verify
	}
	if err := prepare(r); err != nil {
		return nil, err
	}
	if !r.Prepared {
		r.Prepared = true
		if err := s.save(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Prepare materializes only the narrow leaves. Server configuration is derived
// by a future AIRC adapter, never guessed against an unsupported packaged version.
// It is safe to retry storage preparation; callers must not replay a lost RPC.
func (s Store) Prepare(r *Room) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.DiskID != s.DiskID {
		return errors.New("managed chat disk identity mismatch")
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	for _, rel := range []string{layout.ChatState, base, filepath.Join(base, "members"), filepath.Join(base, "server")} {
		if err := safepath.PrivateDirectory(s.Disk, rel, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
	}
	marker := filepath.Join(base, "owner.json")
	want, _ := json.Marshal(struct{ Room, Disk string }{r.ID, r.DiskID})
	raw, err := privateFile(s.Disk, marker, 4096, os.Geteuid(), os.Getegid())
	if errors.Is(err, os.ErrNotExist) {
		if err := s.put(marker, want, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if string(raw) != string(want) {
		return errors.New("managed chat native ownership marker mismatch")
	}
	for _, name := range []string{"config", "data"} {
		if err := safepath.PrivateDirectory(s.Disk, filepath.Join(base, "server", name), s.UID, s.GID); err != nil {
			return err
		}
	}
	for _, member := range append([]Member{r.Human}, r.Members...) {
		if member.Revoked {
			continue
		}
		if err := s.prepareMember(r.ID, member.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) prepareMember(room, member string) error {
	rel, err := layout.ChatMemberRelative(room, member)
	if err != nil {
		return err
	}
	if err := safepath.PrivateDirectory(s.Disk, rel, s.UID, s.GID); err != nil {
		return err
	}
	rel = filepath.Join(rel, "token")
	raw, err := privateFile(s.Disk, rel, 128, s.UID, s.GID)
	if errors.Is(err, os.ErrNotExist) {
		var secret [32]byte
		_, _ = rand.Read(secret[:])
		return s.put(rel, []byte(hex.EncodeToString(secret[:])), s.UID, s.GID)
	}
	if err != nil {
		return err
	}
	if token, err := hex.DecodeString(string(raw)); err != nil || len(token) != 32 {
		return errors.New("invalid managed chat credential file")
	}
	return nil
}

func (s Store) mutation(generation uint64, inventory Inventory) (*Room, error) {
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	r, err := s.Load()
	if err != nil {
		return nil, err
	}
	if !r.Prepared || r.Generation != generation {
		return nil, errors.New("managed chat storage is incomplete or desired generation changed; inspect before retrying")
	}
	if err := s.Verify(r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s Store) Enroll(box Box, generation uint64, inventory Inventory) (*Room, error) {
	if err := box.Validate(); err != nil {
		return nil, err
	}
	r, err := s.mutation(generation, inventory)
	if err != nil {
		return nil, err
	}
	if inventory.Declarations[box.Name] != box.ID {
		return nil, errors.New("enrollment requires the current box declaration identity")
	}
	for _, member := range r.Members {
		if member.Name != box.Name || member.Revoked {
			continue
		}
		if member.BoxID != box.ID {
			return nil, errors.New("box name was reused; revoke the old member identity explicitly before enrolling")
		}
		return r, nil
	}
	member := Member{ID: NewID(), Name: box.Name, BoxID: box.ID}
	r.Members = append(r.Members, member)
	r.enrollDefaultChannel(member.ID)
	r.Generation++
	if err := r.Validate(); err != nil {
		return nil, err
	}
	// Credentials precede the declaration commit. A failed commit leaves an
	// unreferenced private leaf, never an authorized member. No cleanup guesses.
	if err := s.prepareMember(r.ID, member.ID); err != nil {
		return nil, err
	}
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}

// RevokeTarget resolves a revoke argument. A 32-lowercase-hex argument is
// always an immutable member ID, revoked or not. Anything else names the one
// active box membership: validation keeps active names unique across members,
// the human and the server, and enrollment refuses a reused name until the old
// membership is revoked. The human and the server are not box memberships.
func (r *Room) RevokeTarget(arg string) (Member, error) {
	if layout.ValidateIdentity(arg) == nil {
		for _, m := range r.Members {
			if m.ID == arg {
				return m, nil
			}
		}
		return Member{}, errors.New("managed chat member identity is not declared")
	}
	switch arg {
	case r.Human.Name:
		return Member{}, fmt.Errorf("%q is the room's human member; chat revoke removes box memberships only", arg)
	case r.Server.Name:
		return Member{}, fmt.Errorf("%q is the chat server; chat revoke removes box memberships only", arg)
	}
	for _, m := range r.Members {
		if !m.Revoked && m.Name == arg {
			return m, nil
		}
	}
	return Member{}, fmt.Errorf("no active chat member named %q (revoked or never enrolled); yoloctl chat status lists members and IDs", arg)
}

// Revoke records desired revocation of a box name's active membership or an
// immutable member ID, resolved under the mutation. It retains recovery files
// and makes no claim about live ACL reload or terminating connections.
func (s Store) Revoke(target string, generation uint64, inventory Inventory) (*Room, error) {
	r, err := s.mutation(generation, inventory)
	if err != nil {
		return nil, err
	}
	member, err := r.RevokeTarget(target)
	if err != nil {
		return nil, err
	}
	memberID := member.ID
	for i := range r.Members {
		if r.Members[i].ID != memberID {
			continue
		}
		if r.Members[i].Revoked {
			return r, nil
		}
		r.Members[i].Revoked = true
		r.Members[i].ChannelOperator = false
		r.revokeChannels(memberID)
		r.Generation++
		if err := s.save(r); err != nil {
			return nil, err
		}
		return r, nil
	}
	return nil, errors.New("managed chat member identity is not declared")
}
