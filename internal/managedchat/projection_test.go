package managedchat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestProjectionIsExplicitAndIdentityBound(t *testing.T) {
	s, i := fixture(t)
	r := ensure(t, s, i)
	box := Box{Name: "coder", ID: i.Declarations["coder"]}
	r, err := s.Enroll(box, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.Projection(box); err != nil || p != nil {
		t.Fatal("dormant room projected", p, err)
	}
	if _, err := s.EnableRuntime(r.Generation, Inventory{}); err == nil {
		t.Fatal("enabled on partial inventory")
	}
	r, err = s.EnableRuntime(r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Projection(box)
	if err != nil || p == nil || p.Server || p.BoxID != box.ID || p.MemberID != r.Members[0].ID {
		t.Fatal(p, err)
	}
	if len(p.Binds) != 2 {
		t.Fatal("participant got broad projection", p.Binds)
	}
	for _, b := range p.Binds {
		if !b.ReadOnly || b.Target != EndpointMount && b.Target != IdentityMount {
			t.Fatal("participant authority widened", b)
		}
	}
	if wrong, err := s.Projection(Box{Name: box.Name, ID: NewID()}); err != nil || wrong != nil {
		t.Fatal("name reuse inherited membership", wrong, err)
	}
	server, err := s.Projection(r.Server)
	if err != nil || server == nil || !server.Server || len(server.Binds) != 5 {
		t.Fatal(server, err)
	}
	if _, err := s.Projection(Box{Name: r.Server.Name, ID: NewID()}); err == nil {
		t.Fatal("unrelated server adopted")
	}
	if _, err := s.Revoke(p.MemberID, r.Generation, i); err != nil {
		t.Fatal(err)
	}
	if revoked, err := s.Projection(box); err != nil || revoked != nil {
		t.Fatal("revoked membership projected", revoked, err)
	}
	if _, err := os.Stat(p.Binds[1].Source); err != nil {
		t.Fatal("desired revoke removed recovery leaf", err)
	}
}

func TestProjectionRefusesUnpreparedRestoreAndMissingToken(t *testing.T) {
	s, i := fixture(t)
	r := ensure(t, s, i)
	r, err := s.EnableRuntime(r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	r.Prepared = false
	if err := s.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Projection(r.Server); err == nil {
		t.Fatal("unprepared restore projected")
	}
	r.Prepared = true
	if err := s.save(r); err != nil {
		t.Fatal(err)
	}
	leaf, _ := layout.ChatMemberRelative(r.ID, r.Human.ID)
	if err := os.Remove(filepath.Join(s.Disk, leaf, "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Projection(r.Server); err == nil {
		t.Fatal("missing token silently rebuilt")
	}
}

func TestEndpointPreparationNeverReplacesOrFollows(t *testing.T) {
	root := t.TempDir()
	p := &Projection{RoomID: NewID()}
	if err := PrepareEndpoints(root, p, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "chat", p.RoomID, "existing-socket-marker")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareEndpoints(root, p, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "retained" {
		t.Fatal("endpoint replaced", err)
	}
	root = t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "chat")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareEndpoints(root, p, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("followed endpoint parent symlink")
	}
}
