package managedchat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

func fixture(t *testing.T) (Store, Inventory) {
	t.Helper()
	disk := t.TempDir()
	if err := os.Mkdir(filepath.Join(disk, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	return Store{Disk: disk, DiskID: NewID(), UID: os.Geteuid(), GID: os.Getegid()}, Inventory{
		Complete: true, Declarations: map[string]string{"coder": NewID()}, Runtime: map[string]bool{},
	}
}

func ensure(t *testing.T, s Store, inventory Inventory) *Room {
	t.Helper()
	r, err := s.Ensure("", "person", inventory)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func credential(t *testing.T, s Store, r *Room, id string) (string, []byte) {
	t.Helper()
	rel, err := layout.ChatMemberRelative(r.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.Disk, rel, "token")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, raw
}

func TestOwnershipReservationAndPrivateCredentials(t *testing.T) {
	s, inventory := fixture(t)
	r := ensure(t, s, inventory)
	if !r.Prepared || r.Server.Name != DefaultBox || r.Channel != DefaultChannel || r.Generation != 1 {
		t.Fatalf("unexpected room: %+v", r)
	}
	p, secret := credential(t, s, r, r.Human.ID)
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 || len(secret) != 64 {
		t.Fatal("credential is not a private 256-bit token", err)
	}
	raw, _ := r.Marshal()
	if strings.Contains(string(raw), string(secret)) {
		t.Fatal("credential leaked into declaration")
	}
	again := ensure(t, s, inventory)
	_, next := credential(t, s, again, again.Human.ID)
	if again.ID != r.ID || string(secret) != string(next) {
		t.Fatal("idempotent ensure rotated identity or token")
	}
	if _, err := s.Ensure("other", "person", inventory); err == nil {
		t.Fatal("singleton owner was replaced")
	}
	copy := s
	copy.DiskID = NewID()
	if _, err := copy.Load(); err == nil {
		t.Fatal("foreign disk adopted room")
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	if err := os.WriteFile(filepath.Join(s.Disk, base, "owner.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ensure("", "person", inventory); err == nil {
		t.Fatal("foreign ownership marker adopted")
	}
}

func TestConflictsAndIncompleteInventoryDoNotWrite(t *testing.T) {
	for _, kind := range []string{"declaration", "runtime", "partial", "nil declarations", "nil runtime"} {
		t.Run(kind, func(t *testing.T) {
			s, i := fixture(t)
			switch kind {
			case "declaration":
				i.Declarations[DefaultBox] = NewID()
			case "runtime":
				i.Runtime[DefaultBox] = true
			case "partial":
				i.Complete = false
			case "nil declarations":
				i.Declarations = nil
			case "nil runtime":
				i.Runtime = nil
			}
			if _, err := s.Ensure("", "person", i); err == nil {
				t.Fatal("unsafe inventory accepted")
			}
			if _, err := os.Lstat(filepath.Join(s.Disk, layout.ChatManifest)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed preflight published room")
			}
		})
	}
}

func TestRevokeByBoxNameOrMemberID(t *testing.T) {
	s, i := fixture(t)
	hexName := NewID() // a box name that is also valid member-ID syntax
	i.Declarations[hexName] = NewID()
	r := ensure(t, s, i)
	var err error
	for _, name := range []string{"coder", hexName} {
		if r, err = s.Enroll(Box{name, i.Declarations[name]}, r.Generation, i); err != nil {
			t.Fatal(err)
		}
	}
	coder, hexBox := r.Members[0], r.Members[1]
	for arg, want := range map[string]string{
		"person":   `"person" is the room's human member`,
		"chat":     `"chat" is the chat server`,
		"nobody":   `no active chat member named "nobody"`,
		"Bad\x1b":  `no active chat member named "Bad\x1b"`,
		r.Human.ID: "member identity is not declared",
		hexName:    "member identity is not declared", // a 32-hex argument is always an ID
	} {
		if _, err := s.Revoke(arg, r.Generation, i); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("revoke %q: %v, want %q", arg, err, want)
		}
	}
	if got, err := s.Load(); err != nil || got.Generation != r.Generation {
		t.Fatalf("refused revocations changed desired state: %v", err)
	}
	// A destroyed or replaced box's still-declared membership resolves by name.
	i.Declarations["coder"] = NewID()
	if r, err = s.Revoke("coder", r.Generation, i); err != nil {
		t.Fatal(err)
	}
	if !r.Members[0].Revoked || r.Members[0].ID != coder.ID || r.Members[1].Revoked {
		t.Fatalf("name revoked the wrong membership: %+v", r.Members)
	}
	if _, err := s.Revoke("coder", r.Generation, i); err == nil || !strings.Contains(err.Error(), "revoked or never enrolled") {
		t.Fatalf("revoked name resolved again: %v", err)
	}
	// After explicit revocation the name is reusable; it then names the new membership.
	if r, err = s.Enroll(Box{"coder", i.Declarations["coder"]}, r.Generation, i); err != nil {
		t.Fatal(err)
	}
	if m, err := r.RevokeTarget("coder"); err != nil || m.ID != r.Members[2].ID {
		t.Fatalf("reused name resolved to %+v, %v", m, err)
	}
	generation := r.Generation
	if r, err = s.Revoke(coder.ID, r.Generation, i); err != nil || r.Generation != generation {
		t.Fatalf("revoking a revoked ID is not idempotent: %v", err)
	}
	if r, err = s.Revoke(hexBox.ID, r.Generation, i); err != nil || !r.Members[1].Revoked {
		t.Fatalf("32-hex-named box not revocable by its member ID: %v", err)
	}
}

func TestDesiredEnrollmentRevocationAndNameReuse(t *testing.T) {
	s, i := fixture(t)
	r := ensure(t, s, i)
	r, err := s.Enroll(Box{"coder", i.Declarations["coder"]}, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	member := r.Members[0]
	_, secret := credential(t, s, r, member.ID)
	_, humanSecret := credential(t, s, r, r.Human.ID)
	if string(secret) == string(humanSecret) {
		t.Fatal("members share authentication")
	}
	if _, err := s.Enroll(Box{"coder", i.Declarations["coder"]}, 1, i); err == nil {
		t.Fatal("stale generation accepted")
	}
	i.Declarations["coder"] = NewID() // deleted/recreated, same display name/account
	if _, err := s.Enroll(Box{"coder", i.Declarations["coder"]}, r.Generation, i); err == nil {
		t.Fatal("same-name replacement inherited membership")
	}
	i.Complete = false
	if _, err := s.Revoke(member.ID, r.Generation, i); err == nil {
		t.Fatal("partial inventory authorized mutation")
	}
	i.Complete = true
	r, err = s.Revoke(member.ID, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Members[0].Revoked {
		t.Fatal("revocation not retained")
	}
	r, err = s.Enroll(Box{"coder", i.Declarations["coder"]}, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	_, newSecret := credential(t, s, r, r.Members[1].ID)
	if r.Members[1].ID == member.ID || string(newSecret) == string(secret) {
		t.Fatal("explicit replacement reused old authentication")
	}
	// Failed metadata publication cannot authorize the already prepared leaf.
	i.Declarations["second"] = NewID()
	s.write = func(root, rel string, raw []byte, mode os.FileMode, uid, gid int) error {
		if rel == layout.ChatManifest {
			return errors.New("injected pre-commit failure")
		}
		return safepath.WriteFileOwned(root, rel, raw, mode, uid, gid)
	}
	if _, err := s.Enroll(Box{"second", i.Declarations["second"]}, r.Generation, i); err == nil {
		t.Fatal("injected failure ignored")
	}
	after, err := s.Load()
	if err != nil || after.Generation != r.Generation || len(after.Members) != 2 {
		t.Fatal("failed enrollment changed authorization", err)
	}
}

func TestInterruptedPreparationRetainsReservationAndUncertainCommit(t *testing.T) {
	s, i := fixture(t)
	s.write = func(root, rel string, raw []byte, mode os.FileMode, uid, gid int) error {
		if strings.HasSuffix(rel, "owner.json") {
			return errors.New("injected preparation failure")
		}
		return safepath.WriteFileOwned(root, rel, raw, mode, uid, gid)
	}
	if _, err := s.Ensure("", "person", i); err == nil {
		t.Fatal("injected failure ignored")
	}
	reserved, err := s.Load()
	if err != nil || reserved.Prepared {
		t.Fatal("lost incomplete reservation", err)
	}
	s.write = nil
	prepared := ensure(t, s, i)
	if reserved.ID != prepared.ID || reserved.Human.ID != prepared.Human.ID {
		t.Fatal("recovery replaced reserved identities")
	}
	s.write = func(root, rel string, raw []byte, mode os.FileMode, uid, gid int) error {
		if err := safepath.WriteFileOwned(root, rel, raw, mode, uid, gid); err != nil {
			return err
		}
		if rel == layout.ChatManifest {
			return &atomicfile.CommitError{Err: errors.New("injected directory fsync failure")}
		}
		return nil
	}
	_, err = s.Enroll(Box{"coder", i.Declarations["coder"]}, prepared.Generation, i)
	var uncertain *atomicfile.CommitError
	if !errors.As(err, &uncertain) {
		t.Fatal("post-publication uncertainty hidden", err)
	}
	after, err := s.Load()
	if err != nil || len(after.Members) != 1 || after.Generation != 2 {
		t.Fatal("uncertain outcome cannot be inspected", err)
	}
}

func TestNoFollowAndNoMissingStateReplacement(t *testing.T) {
	for _, entry := range []string{"manifest", "config", "member", "token", "data", "missing token"} {
		t.Run(entry, func(t *testing.T) {
			s, i := fixture(t)
			r := ensure(t, s, i)
			base, _ := layout.ChatRoomRelative(r.ID)
			member, _ := layout.ChatMemberRelative(r.ID, r.Human.ID)
			rel := map[string]string{"manifest": layout.ChatManifest, "config": "config", "member": member,
				"token": filepath.Join(member, "token"), "data": filepath.Join(base, "server/data"), "missing token": filepath.Join(member, "token")}[entry]
			p := filepath.Join(s.Disk, rel)
			if err := os.Rename(p, p+"-saved"); err != nil {
				t.Fatal(err)
			}
			if entry != "missing token" {
				if err := os.Symlink(p+"-saved", p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Ensure("", "person", i); err == nil {
				t.Fatal("substituted or missing committed state adopted")
			}
			if entry == "missing token" {
				if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing committed token was replaced")
				}
			}
		})
	}
}

func TestStrictDeclarationAndPaths(t *testing.T) {
	s, i := fixture(t)
	r := ensure(t, s, i)
	raw, _ := r.Marshal()
	for _, malformed := range [][]byte{append(append([]byte(nil), raw...), []byte("{}")...), []byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"unknown":true`, 1))} {
		if _, err := Parse(malformed); err == nil {
			t.Fatal("non-strict declaration accepted")
		}
	}
	for _, id := range []string{"../escape", strings.Repeat("A", 32), strings.Repeat("0", 31), ""} {
		if _, err := layout.ChatRoomRelative(id); err == nil {
			t.Fatal("unsafe native path accepted")
		}
		if _, err := layout.ChatEndpointDir(id); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
