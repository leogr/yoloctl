package managedchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/pelletier/go-toml/v2"
)

func renderedFixture(t *testing.T) (Store, Inventory, *Room, Rendered) {
	t.Helper()
	s, i := fixture(t)
	r := ensure(t, s, i)
	r, err := s.Enroll(Box{Name: "coder", ID: i.Declarations["coder"]}, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.EnableRuntime(r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	return s, i, r, proof
}

func TestRenderUnixOnlyPrivateSnapshotsAndTrustedIdentity(t *testing.T) {
	s, _, r, proof := renderedFixture(t)
	base, _ := layout.ChatRoomRelative(r.ID)
	raw, err := os.ReadFile(filepath.Join(s.Disk, base, "server/config/server.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var server serverDocument
	if err := toml.Unmarshal(raw, &server); err != nil {
		t.Fatal(err)
	}
	if server.Generation != r.Generation || server.ListenUnix != EndpointMount+"/irc.sock" || server.ControlUnix != ControlMount+"/control.sock" || server.Database != ServerDataMount+"/airc.sqlite3" {
		t.Fatal("wrong server contract", server)
	}
	if bytes.Contains(raw, []byte("listen =")) || bytes.Contains(raw, []byte("allow_insecure")) {
		t.Fatal("TCP fallback rendered")
	}
	if len(server.Accounts) != 2 || len(server.Channels) != 1 || len(server.Channels[0].Operators) != 1 {
		t.Fatal("wrong authority")
	}
	for _, m := range append([]Member{r.Human}, r.Members...) {
		_, token := credential(t, s, r, m.ID)
		if bytes.Contains(raw, token) {
			t.Fatal("plaintext token in server config")
		}
		leaf, _ := layout.ChatMemberRelative(r.ID, m.ID)
		clientRaw, err := os.ReadFile(filepath.Join(s.Disk, leaf, ClientConfigFile))
		if err != nil {
			t.Fatal(err)
		}
		var client clientDocument
		if err := toml.Unmarshal(clientRaw, &client); err != nil {
			t.Fatal(err)
		}
		account, _ := AuthAccount(m.ID)
		if client.Account != account || client.Account == m.Name || client.TokenFile != "token" || client.Unix != server.ListenUnix {
			t.Fatal("identity or transport not scoped", client)
		}
		mappingRaw, err := os.ReadFile(filepath.Join(s.Disk, leaf, "identity.json"))
		if err != nil {
			t.Fatal(err)
		}
		var mapping Bootstrap
		if err := json.Unmarshal(mappingRaw, &mapping); err != nil {
			t.Fatal(err)
		}
		human, _ := AuthAccount(r.Human.ID)
		if mapping.HumanAccount != human || mapping.SelfAccount != account || mapping.Generation != r.Generation || mapping.RoomID != r.ID || mapping.DiskID != r.DiskID || len(mapping.Identities) != 2 {
			t.Fatal("untrusted identity mapping", mapping)
		}
		if bytes.Contains(clientRaw, token) || bytes.Contains(mappingRaw, token) {
			t.Fatal("plaintext credential escaped token file")
		}
		for _, name := range []string{ClientConfigFile, "identity.json"} {
			info, err := os.Stat(filepath.Join(s.Disk, leaf, name))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("nonprivate generated file", err)
			}
		}
	}
	before, _ := os.ReadFile(filepath.Join(s.Disk, base, "server/config/server.toml"))
	expected, err := s.Expected(r)
	if err != nil || !reflect.DeepEqual(proof, expected) {
		t.Fatal("proof is nondeterministic", proof, expected, err)
	}
	after, _ := os.ReadFile(filepath.Join(s.Disk, base, "server/config/server.toml"))
	if !bytes.Equal(before, after) {
		t.Fatal("status computation changed configuration")
	}
}

func TestRenderRevokeExcludesACLButPreservesRecoveryAndRejectsStale(t *testing.T) {
	s, i, r, proof := renderedFixture(t)
	old := *r
	m := r.Members[0]
	r, err := s.Revoke(m.ID, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(&old); err == nil {
		t.Fatal("stale snapshot overwrote desired ACL")
	}
	next, err := s.Render(r)
	if err != nil {
		t.Fatal(err)
	}
	if next.ConfigSHA256 == proof.ConfigSHA256 || len(next.Accounts) != 1 {
		t.Fatal("revocation was not rendered")
	}
	_, token := credential(t, s, r, m.ID)
	if len(token) != 64 {
		t.Fatal("recovery token changed")
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	raw, _ := os.ReadFile(filepath.Join(s.Disk, base, "server/config/server.toml"))
	account, _ := AuthAccount(m.ID)
	if bytes.Contains(raw, []byte(account)) {
		t.Fatal("revoked account remains in ACL")
	}
}

func TestRenderFailurePreservesLastServerSnapshot(t *testing.T) {
	s, i, r, _ := renderedFixture(t)
	base, _ := layout.ChatRoomRelative(r.ID)
	file := filepath.Join(s.Disk, base, "server/config/server.toml")
	before, _ := os.ReadFile(file)
	r, err := s.Revoke(r.Members[0].ID, r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	s.write = func(_, rel string, _ []byte, _ os.FileMode, _, _ int) error {
		return errors.New("injected client write failure: " + rel)
	}
	if _, err := s.Render(r); err == nil {
		t.Fatal("write failure ignored")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("failed preparation replaced server snapshot")
	}
}

func TestImmutableAccountEncodingCollisionFailsClosed(t *testing.T) {
	first, err := AuthAccount(strings.Repeat("0", 32))
	if err != nil || len(first) != 24 {
		t.Fatal(first, err)
	}
	second, _ := AuthAccount(strings.Repeat("0", 31) + "1")
	if first != second {
		t.Fatal("fixture no longer tests truncated identity collision")
	}
	s, i, r, _ := renderedFixture(t)
	r.Human.ID = strings.Repeat("0", 32)
	r.Members[0].ID = strings.Repeat("0", 31) + "1"
	// Prepare fresh leaves for this deliberately impossible random-ID outcome.
	if err := s.Prepare(r); err != nil {
		t.Fatal(err)
	}
	if err := s.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(r); err == nil {
		t.Fatal("ambiguous authentication accounts accepted")
	}
	_ = i
}
