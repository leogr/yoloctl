package managedchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/pelletier/go-toml/v2"
)

func TestHumanNicknamePersistsWithoutReplacingIdentity(t *testing.T) {
	s, i, r, _ := renderedFixture(t)
	before := r.Human
	_, token := credential(t, s, r, r.Human.ID)
	next, err := s.SetHumanNickname("Person_2", r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != r.Generation+1 || next.Human.ID != before.ID || next.Human.Name != before.Name || next.Human.Nickname != "Person_2" || next.ID != r.ID {
		t.Fatal("nickname replaced durable identity", next)
	}
	if _, err := s.Render(next); err != nil {
		t.Fatal(err)
	}
	leaf, _ := layout.ChatMemberRelative(r.ID, r.Human.ID)
	raw, err := os.ReadFile(filepath.Join(s.Disk, leaf, ClientConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var client clientDocument
	if err := toml.Unmarshal(raw, &client); err != nil {
		t.Fatal(err)
	}
	account, _ := AuthAccount(before.ID)
	if client.Nick != "Person_2" || client.Account != account || client.TokenFile != "token" {
		t.Fatal("client settings changed authority", client)
	}
	_, retained := credential(t, s, next, next.Human.ID)
	if !bytes.Equal(token, retained) || bytes.Contains(raw, token) {
		t.Fatal("nickname rotated or exposed a credential")
	}
	raw, err = os.ReadFile(filepath.Join(s.Disk, leaf, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap Bootstrap
	if err := json.Unmarshal(raw, &bootstrap); err != nil || bootstrap.Identities[0].Nick != client.Nick || bootstrap.Identities[0].Name != before.Name || bootstrap.Identities[1].BoxID != r.Members[0].BoxID {
		t.Fatal("trusted metadata disagrees with client settings", err)
	}
	again, err := s.SetHumanNickname("Person_2", next.Generation, i)
	if err != nil || again.Generation != next.Generation {
		t.Fatal("unchanged nickname rewrote generation", err)
	}
	if _, err := s.SetHumanNickname("next", r.Generation, i); err == nil {
		t.Fatal("stale settings overwrote newer nickname")
	}
}

func TestNicknameRejectsInvalidAndReservedNamesWithoutWriting(t *testing.T) {
	s, i, r, _ := renderedFixture(t)
	account, _ := AuthAccount(r.Members[0].ID)
	before, _ := r.Marshal()
	for _, nick := range []string{"", "two words", "9starts", "-bad", "nick\nQUIT", "nick\x00", "é", strings.Repeat("x", 25), "CODER", account} {
		if _, err := s.SetHumanNickname(nick, r.Generation, i); err == nil {
			t.Fatal("unsafe nickname accepted", nick)
		}
		got, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := got.Marshal()
		if !bytes.Equal(before, raw) {
			t.Fatal("rejected nickname changed the declaration")
		}
	}
	i.Complete = false
	if _, err := s.SetHumanNickname("safe", r.Generation, i); err == nil {
		t.Fatal("partial inventory authorized settings")
	}
}

func TestNicknameRetainsAssignedNameWhenMemberUsesFallback(t *testing.T) {
	s, i := fixture(t)
	r := ensure(t, s, i)
	i.Declarations["PERSON"] = NewID()
	r, err := s.Enroll(Box{Name: "PERSON", ID: i.Declarations["PERSON"]}, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	memberAccount, _ := AuthAccount(r.Members[0].ID)
	nicks, err := RoomNicknames(r)
	if err != nil || nicks[r.Human.ID] != "person" || nicks[r.Members[0].ID] != memberAccount {
		t.Fatal("case-folded enrollment did not keep the human's assigned name", err)
	}
	r, err = s.SetHumanNickname("person", r.Generation, i)
	if err != nil {
		t.Fatal("saving the human's assigned name rejected an unassigned preferred name", err)
	}
	nicks, err = RoomNicknames(r)
	if err != nil || nicks[r.Human.ID] != "person" || nicks[r.Members[0].ID] != memberAccount {
		t.Fatal("saving nickname changed assigned names", err)
	}
}

func TestNicknamePublicationFailurePreservesRecovery(t *testing.T) {
	s, i, r, _ := renderedFixture(t)
	base, _ := layout.ChatRoomRelative(r.ID)
	serverPath := filepath.Join(s.Disk, base, "server/config/server.toml")
	before, _ := os.ReadFile(serverPath)
	next, err := s.SetHumanNickname("newname", r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	s.write = func(string, string, []byte, os.FileMode, int, int) error {
		return errors.New("interrupted publication")
	}
	if _, err := s.Render(next); err == nil {
		t.Fatal("publication failure hidden")
	}
	after, _ := os.ReadFile(serverPath)
	if !bytes.Equal(before, after) {
		t.Fatal("failed client publication replaced living server config")
	}
	if err := s.VerifyPublished(next); err == nil {
		t.Fatal("ordinary server start could load stale authority after publication failure")
	}
	got, err := s.Load()
	if err != nil || got.Human.Nickname != "newname" || got.Generation != next.Generation {
		t.Fatal("desired recovery metadata lost", err)
	}
	s.write = nil
	if _, err := s.Render(got); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyPublished(got); err != nil {
		t.Fatal("explicit render failed to recover publication", err)
	}
}

func TestPublishedServerConfigurationCannotFollowLinks(t *testing.T) {
	s, _, r, _ := renderedFixture(t)
	base, _ := layout.ChatRoomRelative(r.ID)
	path := filepath.Join(s.Disk, base, "server/config/server.toml")
	retained := path + ".retained"
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(retained, path); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyPublished(r); err == nil {
		t.Fatal("linked server snapshot authorized a start")
	}
}

func TestChannelOperatorGrantRevokeAndIndependentImport(t *testing.T) {
	s, i, r, oldProof := renderedFixture(t)
	m := r.Members[0]
	r, err := s.SetOperators(map[string]bool{m.ID: true}, r.Generation, i)
	if err != nil || !r.Members[0].ChannelOperator {
		t.Fatal("operator grant not recorded", err)
	}
	proof, err := s.Render(r)
	if err != nil || proof.ConfigSHA256 == oldProof.ConfigSHA256 {
		t.Fatal("operator ACL not rendered", err)
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	raw, _ := os.ReadFile(filepath.Join(s.Disk, base, "server/config/server.toml"))
	var doc serverDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	human, _ := AuthAccount(r.Human.ID)
	member, _ := AuthAccount(m.ID)
	if !slices.Equal(doc.Channels[0].Operators, []string{human, member}) {
		t.Fatal("operator ACL is not immutable-account based")
	}
	r, err = s.SetHumanNickname("newhuman", r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := r.Reidentify(NewID())
	if err != nil || imported.Members[0].ChannelOperator || imported.Human.Nickname != "newhuman" || imported.Members[0].ID == m.ID {
		t.Fatal("import cloned live authority or lost display preference", err)
	}
	r, err = s.Revoke(m.ID, r.Generation, i)
	if err != nil || r.Members[0].ChannelOperator {
		t.Fatal("revoked member retained desired operator grant", err)
	}
	if _, err := s.SetOperators(map[string]bool{m.ID: true}, r.Generation, i); err == nil {
		t.Fatal("revoked identity regained operator grant")
	}
}

func TestOperatorSnapshotRejectsPartialAndReusedNames(t *testing.T) {
	s, i, r, _ := renderedFixture(t)
	m := r.Members[0]
	partial := i
	partial.Complete = false
	if _, err := s.SetOperators(map[string]bool{m.ID: true}, r.Generation, partial); err == nil {
		t.Fatal("partial inventory authorized role mutation")
	}
	i.Declarations[m.Name] = NewID()
	if _, err := s.SetOperators(map[string]bool{m.ID: true}, r.Generation, i); err == nil {
		t.Fatal("same-name replacement inherited role")
	}
	got, err := s.Load()
	if err != nil || got.Generation != r.Generation || got.Members[0].ChannelOperator {
		t.Fatal("rejected role mutation changed durable state", err)
	}
}
