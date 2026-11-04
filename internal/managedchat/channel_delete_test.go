package managedchat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/pelletier/go-toml/v2"
)

func serverConfig(t *testing.T, s Store, r *Room) serverDocument {
	t.Helper()
	base, _ := layout.ChatRoomRelative(r.ID)
	raw, err := os.ReadFile(filepath.Join(s.Disk, base, "server/config", ServerConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	var doc serverDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestChannelDeleteRecordsOnlyIdentityAndRendersExplicitIntent(t *testing.T) {
	s, inventory, r, _ := renderedFixture(t)
	unchanged := serverConfig(t, s, r)
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	build := r.Channels[1]
	// The default channel, unknown or foreign identities and a stale generation
	// are refused before anything is saved.
	for _, test := range []struct {
		name             string
		room, channel    string
		generation       uint64
		inventoryChanged bool
	}{
		{"default", r.ID, r.Channels[0].ID, r.Generation, false},
		{"unknown", r.ID, NewID(), r.Generation, false},
		{"foreign room", NewID(), build.ID, r.Generation, false},
		{"stale generation", r.ID, build.ID, r.Generation - 1, false},
		{"invalid", r.ID, "../build", r.Generation, false},
	} {
		if _, err := s.DeleteChannel(test.room, test.channel, test.generation, inventory); err == nil {
			t.Fatal("deletion accepted", test.name)
		}
	}
	if kept, _ := s.Load(); kept.Generation != r.Generation || len(kept.Channels) != 2 {
		t.Fatal("a refused deletion changed desired state")
	}
	deleted, err := s.DeleteChannel(r.ID, build.ID, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	want := []DeletedChannel{{ID: build.ID, Name: "#build", Generation: r.Generation + 1}}
	if deleted.Generation != r.Generation+1 || len(deleted.Channels) != 1 || !reflect.DeepEqual(deleted.DeletedChannels, want) {
		t.Fatal("deletion did not remove the channel and record only its identity", deleted.Channels, deleted.DeletedChannels)
	}
	if _, err := s.Render(deleted); err != nil {
		t.Fatal(err)
	}
	doc := serverConfig(t, s, deleted)
	if len(doc.Channels) != 1 || !reflect.DeepEqual(doc.RemovedChannels, []string{"#build"}) {
		t.Fatal("server configuration lacks explicit deletion intent", doc.Channels, doc.RemovedChannels)
	}
	if unchanged.RemovedChannels != nil {
		t.Fatal("a room without deletions renders deletion intent")
	}
	// Participant bootstrap metadata never carries deletion records.
	leaf, _ := layout.ChatMemberRelative(deleted.ID, deleted.Members[0].ID)
	identity, _ := os.ReadFile(filepath.Join(s.Disk, leaf, "identity.json"))
	if bytes.Contains(identity, []byte("deleted")) {
		t.Fatal("participant bootstrap carries deletion records")
	}
	// Recreating the name gets a fresh ID; the active name leaves the intent
	// list while its old identity stays recorded.
	again, err := s.CreateChannel(deleted.ID, "#build", deleted.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if again.Channels[1].ID == build.ID || !reflect.DeepEqual(again.DeletedChannels, want) || again.RemovedChannelNames() != nil {
		t.Fatal("same-name channel reused the deleted identity or stayed listed for purge")
	}
	if _, err := s.Render(again); err != nil {
		t.Fatal(err)
	}
	if doc := serverConfig(t, s, again); doc.RemovedChannels != nil || len(doc.Channels) != 2 {
		t.Fatal("recreated name rendered as a deletion target", doc.RemovedChannels)
	}
	// Deleting it again records a second identity for the same name.
	twice, err := s.DeleteChannel(again.ID, again.Channels[1].ID, again.Generation, inventory)
	if err != nil || len(twice.DeletedChannels) != 2 || !reflect.DeepEqual(twice.RemovedChannelNames(), []string{"#build"}) {
		t.Fatal("repeated deletion of a reused name", twice.DeletedChannels, err)
	}
}

func TestChannelDeletionLedgerIsStrictAndBounded(t *testing.T) {
	s, inventory := fixture(t)
	r := ensure(t, s, inventory)
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.DeleteChannel(r.ID, r.Channels[1].ID, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := r.Marshal()
	for _, test := range []struct {
		name   string
		mutate func(*Room)
	}{
		{"default name", func(r *Room) { r.DeletedChannels[0].Name = DefaultChannel }},
		{"active id", func(r *Room) { r.DeletedChannels[0].ID = r.Channels[0].ID }},
		{"invalid id", func(r *Room) { r.DeletedChannels[0].ID = "../x" }},
		{"invalid name", func(r *Room) { r.DeletedChannels[0].Name = "#Build" }},
		{"zero generation", func(r *Room) { r.DeletedChannels[0].Generation = 0 }},
		{"future generation", func(r *Room) { r.DeletedChannels[0].Generation = r.Generation + 1 }},
		{"duplicate id", func(r *Room) { r.DeletedChannels = append(r.DeletedChannels, r.DeletedChannels[0]) }},
		{"unordered", func(r *Room) {
			r.DeletedChannels = append(r.DeletedChannels, DeletedChannel{ID: NewID(), Name: "#old", Generation: r.DeletedChannels[0].Generation - 1})
		}},
		{"too many", func(r *Room) {
			for n := len(r.DeletedChannels); n <= MaxDeletedChannels; n++ {
				r.DeletedChannels = append(r.DeletedChannels, DeletedChannel{ID: NewID(), Name: "#old", Generation: r.Generation})
			}
		}},
		{"schema 1", func(r *Room) { r.Schema, r.Channels = 1, nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(next)
			bad, _ := json.Marshal(next)
			if _, err := Parse(bad); err == nil {
				t.Fatal("invalid deletion ledger accepted")
			}
		})
	}
	// A full ledger refuses another deletion before any write; records are
	// never dropped to make room.
	full, err := s.CreateChannel(r.ID, "#ops", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for n := len(full.DeletedChannels); n < MaxDeletedChannels; n++ {
		full.DeletedChannels = append(full.DeletedChannels, DeletedChannel{ID: NewID(), Name: fmt.Sprintf("#old%d", n%3), Generation: full.Generation})
	}
	if err := s.save(full); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteChannel(full.ID, full.Channels[1].ID, full.Generation, inventory); err == nil {
		t.Fatal("deletion past a full ledger accepted")
	}
	if kept, _ := s.Load(); kept.Generation != full.Generation || len(kept.DeletedChannels) != MaxDeletedChannels || !slices.ContainsFunc(kept.Channels, func(c Channel) bool { return c.Name == "#ops" }) {
		t.Fatal("refused deletion changed the ledger or catalog")
	}
}

func TestChannelDeletionIdentitiesSurviveIndependentImport(t *testing.T) {
	s, inventory, r, _ := renderedFixture(t)
	r, err := s.CreateChannel(r.ID, "#build", r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.DeleteChannel(r.ID, r.Channels[1].ID, r.Generation, inventory)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := r.Marshal()
	disk := NewID()
	next, err := r.Reidentify(disk)
	if err != nil {
		t.Fatal(err)
	}
	old := r.DeletedChannels[0]
	if len(next.DeletedChannels) != 1 || next.DeletedChannels[0] != (DeletedChannel{ID: ImportedID(disk, old.ID), Name: old.Name, Generation: next.Generation}) {
		t.Fatal("import lost or failed to remap the deletion identity", next.DeletedChannels)
	}
	if after, _ := r.Marshal(); !bytes.Equal(before, after) {
		t.Fatal("import mutated the source deletion ledger")
	}
	if !reflect.DeepEqual(next.RemovedChannelNames(), []string{"#build"}) {
		t.Fatal("restored room lost its explicit deletion intent")
	}
}
