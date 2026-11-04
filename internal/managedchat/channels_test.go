package managedchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/pelletier/go-toml/v2"
)

func TestChannelLegacyCompatibilityAndNoReadMigration(t *testing.T) {
	s, inventory := fixture(t)
	r := ensure(t, s, inventory)
	r, err := s.Enroll(Box{"coder", inventory.Declarations["coder"]}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(s.Disk, layout.ChatManifest))
	if err != nil {
		t.Fatal(err)
	}
	catalog := r.ChannelCatalog()
	if len(catalog) != 1 || catalog[0].ID != LegacyChannelID(r.ID) || len(catalog[0].Members) != 2 {
		t.Fatal("legacy effective catalog lost identity or membership")
	}
	if _, err := Parse(before); err != nil {
		t.Fatal(err)
	}
	// The no-op create and add must not upgrade legacy metadata either.
	r, err = s.CreateChannel(r.ID, DefaultChannel, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SetChannelMember(r.ID, catalog[0].ID, r.Members[0].ID, r.Members[0].BoxID, true, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(s.Disk, layout.ChatManifest))
	if r.Schema != 1 || r.Channels != nil || !bytes.Equal(before, after) || bytes.Contains(after, []byte(`"channels"`)) {
		t.Fatal("read or no-op migrated a legacy declaration")
	}
	catalog[0].Members[0] = NewID()
	if reflect.DeepEqual(catalog, r.ChannelCatalog()) {
		t.Fatal("effective catalog aliases room state")
	}
	for _, field := range []string{`null`, `[]`, `[{}]`} {
		bad := append(slices.Clone(before[:len(before)-1]), []byte(`,"channels":`+field+`}`)...)
		if _, err := Parse(bad); err == nil {
			t.Fatal("schema-1 new field accepted", field)
		}
	}
	r, err = s.Revoke(r.Members[0].ID, r.Generation, inventory)
	if err != nil || r.Schema != 1 || r.Channels != nil {
		t.Fatal("ordinary revocation migrated legacy state", err)
	}
}

func TestChannelCatalogStrictIdentityACLAndBounds(t *testing.T) {
	s, inventory := fixture(t)
	r := ensure(t, s, inventory)
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Room)
	}{
		{"empty", func(r *Room) { r.Channels = nil }},
		{"default missing", func(r *Room) { r.Channels = r.Channels[1:] }},
		{"duplicate id", func(r *Room) { r.Channels[1].ID = r.Channels[0].ID }},
		{"duplicate name", func(r *Room) { r.Channels[1].Name = DefaultChannel }},
		{"mixed case", func(r *Room) { r.Channels[1].Name = "#Build" }},
		{"list", func(r *Room) { r.Channels[1].Name = "#build,#space" }},
		{"invalid id", func(r *Room) { r.Channels[1].ID = "../other" }},
		{"human absent", func(r *Room) { r.Channels[1].Members = nil }},
		{"foreign member", func(r *Room) {
			r.Channels[1].Members = []string{NewID(), r.Human.ID}
			slices.Sort(r.Channels[1].Members)
		}},
		{"duplicate member", func(r *Room) { r.Channels[1].Members = []string{r.Human.ID, r.Human.ID} }},
		{"too many", func(r *Room) {
			for n := len(r.Channels); n <= MaxChannels; n++ {
				r.Channels = append(r.Channels, Channel{ID: NewID(), Name: fmt.Sprintf("#z%d", n), Members: []string{r.Human.ID}})
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(next)
			bad, _ := json.Marshal(next)
			if _, err := Parse(bad); err == nil {
				t.Fatal("invalid channel catalog accepted")
			}
		})
	}
	for _, invalid := range []string{"", "#", "#a,b", "#a b", "#a/b", "#A", "#a\n", "#é", "#" + strings.Repeat("a", 48)} {
		if err := ValidateChannelName(invalid); err == nil {
			t.Fatal("invalid canonical name accepted", invalid)
		}
	}
	if got, err := NormalizeChannelName("BuIlD_1-2"); err != nil || got != "#build_1-2" {
		t.Fatal(got, err)
	}
	if _, err := NormalizeChannelName("K"); err == nil {
		t.Fatal("Unicode fold bypassed ASCII input rule")
	}
}

func TestChannelAccessIsIndependentOfEnrollmentAndNameReuse(t *testing.T) {
	s, inventory, r, _ := renderedFixture(t)
	m := r.Members[0]
	_, originalToken := credential(t, s, r, m.ID)
	defaultID := r.ChannelCatalog()[0].ID
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	build := r.Channels[1]
	if r.Schema != 2 || r.Channels[0].ID != defaultID || !slices.Contains(build.Members, m.ID) {
		t.Fatal("creation did not preserve/snapshot identities")
	}
	r, err = s.SetChannelMember(r.ID, defaultID, m.ID, m.BoxID, false, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if r.Members[0].Revoked || slices.Contains(r.Channels[0].Members, m.ID) || !slices.Contains(r.Channels[1].Members, m.ID) {
		t.Fatal("channel removal changed room/other-channel enrollment")
	}
	_, afterToken := credential(t, s, r, m.ID)
	if !bytes.Equal(originalToken, afterToken) {
		t.Fatal("channel removal changed room credential")
	}
	gen := r.Generation
	r, err = s.SetChannelMember(r.ID, defaultID, m.ID, m.BoxID, false, gen, inventory)
	if err != nil || r.Generation != gen {
		t.Fatal("repeat channel remove was not a checked no-op", err)
	}
	inventory.Declarations["newcomer"] = NewID()
	r, err = s.Enroll(Box{"newcomer", inventory.Declarations["newcomer"]}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	newcomer := r.Members[1]
	if !slices.Contains(r.Channels[0].Members, newcomer.ID) || slices.Contains(r.Channels[1].Members, newcomer.ID) {
		t.Fatal("new enrollment inherited extra-channel access")
	}
	r, err = s.SetChannelMember(r.ID, build.ID, newcomer.ID, newcomer.BoxID, true, r.Generation, inventory)
	if err != nil || !slices.Contains(r.Channels[1].Members, newcomer.ID) {
		t.Fatal("explicit channel grant failed", err)
	}
	inventory.Declarations[m.Name] = NewID()
	if _, err := s.SetChannelMember(r.ID, build.ID, m.ID, m.BoxID, true, r.Generation, inventory); err == nil {
		t.Fatal("stale box incarnation received channel access")
	}
	r, err = s.SetChannelMember(r.ID, build.ID, m.ID, m.BoxID, false, r.Generation, inventory)
	if err != nil {
		t.Fatal("stale box incarnation could not have its access reduced", err)
	}
	r, err = s.Revoke(m.ID, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Channels {
		if slices.Contains(c.Members, m.ID) {
			t.Fatal("revoked ID retained channel access")
		}
	}
	r, err = s.Enroll(Box{m.Name, inventory.Declarations[m.Name]}, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	replacement := r.Members[2]
	_, newToken := credential(t, s, r, replacement.ID)
	if replacement.ID == m.ID || bytes.Equal(newToken, originalToken) || slices.Contains(r.Channels[1].Members, replacement.ID) {
		t.Fatal("name reuse inherited identity, authentication or extra-channel rights")
	}
}

func TestChannelRemovalCanReduceMissingOrReplacedDeclaration(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprint("replaced=", replaced), func(t *testing.T) {
			s, inventory, r, _ := renderedFixture(t)
			member := r.Members[0]
			_, token := credential(t, s, r, member.ID)
			r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			channelID := r.Channels[1].ID
			replacement := NewID()
			delete(inventory.Declarations, member.Name)
			if replaced {
				inventory.Declarations[member.Name] = replacement
			}
			if _, err := s.SetChannelMember(r.ID, channelID, member.ID, member.BoxID, true, r.Generation, inventory); err == nil {
				t.Fatal("missing/replaced declaration received access even as a no-op")
			}
			for _, ids := range [][4]string{{NewID(), channelID, member.ID, member.BoxID}, {r.ID, NewID(), member.ID, member.BoxID}, {r.ID, channelID, NewID(), member.BoxID}, {r.ID, channelID, member.ID, replacement}} {
				if _, err := s.SetChannelMember(ids[0], ids[1], ids[2], ids[3], false, r.Generation, inventory); err == nil {
					t.Fatal("removal accepted a substituted immutable selector", ids)
				}
			}
			generation := r.Generation
			r, err = s.SetChannelMember(r.ID, channelID, member.ID, member.BoxID, false, generation, inventory)
			if err != nil || r.Generation != generation+1 || r.Members[0].Revoked || slices.Contains(r.Channels[1].Members, member.ID) || !slices.Contains(r.Channels[0].Members, member.ID) {
				t.Fatal("access reduction failed or changed enrollment/other channels", err)
			}
			_, after := credential(t, s, r, member.ID)
			if !bytes.Equal(token, after) {
				t.Fatal("access reduction rotated a room credential")
			}
			if _, err := s.SetChannelMember(r.ID, channelID, member.ID, member.BoxID, false, generation, inventory); err == nil {
				t.Fatal("access reduction skipped generation precondition")
			}
			if again, err := s.SetChannelMember(r.ID, channelID, member.ID, member.BoxID, false, r.Generation, inventory); err != nil || again.Generation != r.Generation {
				t.Fatal("repeated reduction was not a checked no-op", err)
			}
		})
	}
}

func TestChannelMutationsRejectForeignPartialAndNoFollowState(t *testing.T) {
	for _, kind := range []string{"partial", "foreign room", "foreign channel", "foreign member", "foreign box", "stale generation", "server replaced", "manifest symlink", "token symlink", "wrong owner marker"} {
		t.Run(kind, func(t *testing.T) {
			s, inventory, r, _ := renderedFixture(t)
			roomID, channelID, memberID, boxID, generation := r.ID, r.ChannelCatalog()[0].ID, r.Members[0].ID, r.Members[0].BoxID, r.Generation
			before, _ := os.ReadFile(filepath.Join(s.Disk, layout.ChatManifest))
			switch kind {
			case "partial":
				inventory.Complete = false
			case "foreign room":
				roomID = NewID()
			case "foreign channel":
				channelID = NewID()
			case "foreign member":
				memberID = NewID()
			case "foreign box":
				boxID = NewID()
			case "stale generation":
				generation--
			case "server replaced":
				inventory.Declarations[r.Server.Name] = NewID()
			case "manifest symlink", "token symlink":
				path := filepath.Join(s.Disk, layout.ChatManifest)
				if kind == "token symlink" {
					path, _ = credential(t, s, r, memberID)
				}
				if err := os.Rename(path, path+".kept"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".kept", path); err != nil {
					t.Fatal(err)
				}
			case "wrong owner marker":
				base, _ := layout.ChatRoomRelative(r.ID)
				if err := os.WriteFile(filepath.Join(s.Disk, base, "owner.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SetChannelMember(roomID, channelID, memberID, boxID, false, generation, inventory); err == nil {
				t.Fatal("unsafe ACL mutation accepted")
			}
			after, _ := os.ReadFile(filepath.Join(s.Disk, layout.ChatManifest))
			if !bytes.Equal(before, after) {
				t.Fatal("failed preflight changed desired state")
			}
		})
	}
}

func TestChannelInterruptedWritesRetainInspectableDesiredState(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			s, inventory, r, _ := renderedFixture(t)
			base, _ := layout.ChatRoomRelative(r.ID)
			server := filepath.Join(s.Disk, base, "server/config", ServerConfigFile)
			before, _ := os.ReadFile(server)
			s.write = func(root, rel string, raw []byte, mode os.FileMode, uid, gid int) error {
				if rel != layout.ChatManifest {
					return safepath.WriteFileOwned(root, rel, raw, mode, uid, gid)
				}
				if !committed {
					return errors.New("injected before publication")
				}
				if err := safepath.WriteFileOwned(root, rel, raw, mode, uid, gid); err != nil {
					return err
				}
				return &atomicfile.CommitError{Err: errors.New("injected directory fsync failure")}
			}
			_, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
			if err == nil {
				t.Fatal("write failure hidden")
			}
			var uncertain *atomicfile.CommitError
			if errors.As(err, &uncertain) != committed {
				t.Fatal("publication uncertainty misclassified", err)
			}
			after, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if committed && (after.Schema != 2 || after.Generation != r.Generation+1) || !committed && (after.Schema != 1 || after.Generation != r.Generation) {
				t.Fatal("wrong recoverable declaration")
			}
			serverAfter, _ := os.ReadFile(server)
			if !bytes.Equal(before, serverAfter) {
				t.Fatal("desired write replaced living server snapshot")
			}
		})
	}
}

func TestChannelRenderPrivateACLIntersectionStableClientsAndPublication(t *testing.T) {
	s, inventory, r, oldProof := renderedFixture(t)
	m := r.Members[0]
	leaf, _ := layout.ChatMemberRelative(r.ID, m.ID)
	clientPath := filepath.Join(s.Disk, leaf, ClientConfigFile)
	clientBefore, _ := os.ReadFile(clientPath)
	_, tokenBefore := credential(t, s, r, m.ID)
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SetChannelMember(r.ID, r.Channels[1].ID, m.ID, m.BoxID, false, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Render(r)
	if err != nil || proof.ConfigSHA256 == oldProof.ConfigSHA256 {
		t.Fatal("channel ACL not published", err)
	}
	clientAfter, _ := os.ReadFile(clientPath)
	_, tokenAfter := credential(t, s, r, m.ID)
	if !bytes.Equal(clientBefore, clientAfter) || !bytes.Equal(tokenBefore, tokenAfter) {
		t.Fatal("catalog/ACL edit changed named-client identity or credential")
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	serverPath := filepath.Join(s.Disk, base, "server/config", ServerConfigFile)
	raw, _ := os.ReadFile(serverPath)
	var doc serverDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	account, _ := AuthAccount(m.ID)
	human, _ := AuthAccount(r.Human.ID)
	if len(doc.Accounts) != 2 || len(doc.Channels) != 2 || !slices.Contains(doc.Channels[0].Members, account) || slices.Contains(doc.Channels[1].Members, account) {
		t.Fatal("channel ACL was confused with room enrollment")
	}
	// The core slice supplies the verified operator accounts; intersection
	// must not grant a permitted operator access to an excluded channel.
	channels := renderChannels(r, []string{human, account}, []string{human, account})
	if len(channels[0].Operators) != 2 || !reflect.DeepEqual(channels[1].Operators, []string{human}) {
		t.Fatal("configured operator bypassed channel ACL")
	}
	identityRaw, _ := os.ReadFile(filepath.Join(s.Disk, leaf, "identity.json"))
	var identity Bootstrap
	if err := json.Unmarshal(identityRaw, &identity); err != nil || identity.Schema != 2 || identity.DefaultChannel != DefaultChannel || !reflect.DeepEqual(identity.Channels, r.ChannelCatalog()) {
		t.Fatal("missing trusted catalog metadata", err)
	}
	r, err = s.SetChannelMember(r.ID, r.Channels[1].ID, m.ID, m.BoxID, true, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	s.write = func(_, _ string, _ []byte, _ os.FileMode, _, _ int) error {
		return errors.New("injected client publication failure")
	}
	if _, err := s.Render(r); err == nil {
		t.Fatal("failed publication reported success")
	}
	kept, _ := os.ReadFile(serverPath)
	if !bytes.Equal(raw, kept) {
		t.Fatal("failed client publication replaced living server config")
	}
}

func TestChannelArchiveReidentificationDoesNotMutateSource(t *testing.T) {
	s, inventory, r, _ := renderedFixture(t)
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SetChannelMember(r.ID, r.Channels[0].ID, r.Members[0].ID, r.Members[0].BoxID, false, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := r.Marshal()
	disk := NewID()
	next, err := r.Reidentify(disk)
	if err != nil {
		t.Fatal(err)
	}
	for n, channel := range r.Channels {
		if next.Channels[n].Name != channel.Name || next.Channels[n].ID != ImportedID(disk, channel.ID) {
			t.Fatal("channel identity was not independently remapped")
		}
		for _, id := range channel.Members {
			if !slices.Contains(next.Channels[n].Members, ImportedID(disk, id)) || slices.Contains(next.Channels[n].Members, id) {
				t.Fatal("ACL retained old authority or lost relationship")
			}
		}
	}
	after, _ := r.Marshal()
	if !bytes.Equal(before, after) {
		t.Fatal("archive remapping mutated source channel slices")
	}
	again, err := r.Reidentify(disk)
	if err != nil || !reflect.DeepEqual(again, next) {
		t.Fatal("interrupted import cannot reproduce its own channel identities", err)
	}
}
