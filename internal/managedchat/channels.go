package managedchat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
)

const MaxChannels = 16

// MaxDeletedChannels bounds the deletion ledger. A full ledger refuses another
// deletion before any effect; records are never dropped to make room.
const MaxDeletedChannels = 256

// DeletedChannel is the identity of a channel removed by an explicit delete:
// Generation is the room generation that deleted it. It stores no content,
// grants nothing and never reserves the name.
type DeletedChannel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Generation uint64 `json:"generation"`
}

// Channel is a durable permission set, independent of connection JOIN state or
// a client's follow preference. Member IDs refer to this room's active ledger;
// display names, shared account names and nicknames never grant channel access.
type Channel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

func ValidateChannelName(name string) error {
	if len(name) < 2 || len(name) > 48 || name[0] != '#' {
		return errors.New("managed channel must be # followed by 1–47 lowercase ASCII letters, digits, - or _")
	}
	for _, c := range []byte(name[1:]) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return errors.New("managed channel must be canonical lowercase ASCII without lists or whitespace")
		}
	}
	return nil
}

// NormalizeChannelName is for the CLI only. Storage and RPC validation require
// the canonical form instead of silently interpreting an ambiguous selector.
func NormalizeChannelName(name string) (string, error) {
	for _, c := range []byte(name) {
		if c >= 128 {
			return "", errors.New("managed channel names must be ASCII")
		}
	}
	if !strings.HasPrefix(name, "#") {
		name = "#" + name
	}
	name = strings.ToLower(name)
	return name, ValidateChannelName(name)
}

// LegacyChannelID supplies stable public identity without a read-side migration.
// A schema-2 upgrade preserves it; independent import remaps it like other IDs.
func LegacyChannelID(roomID string) string {
	sum := sha256.Sum256([]byte("yoloctl-chat-channel-v1\x00" + roomID + "\x00" + DefaultChannel))
	return hex.EncodeToString(sum[:16])
}

func cloneChannels(channels []Channel) []Channel {
	next := slices.Clone(channels)
	for i := range next {
		next[i].Members = slices.Clone(channels[i].Members)
	}
	return next
}

// ChannelCatalog returns an independent desired snapshot of a validated room.
// It never writes or changes schema, and says nothing about applied server ACLs.
func (r *Room) ChannelCatalog() []Channel {
	if r.Schema == 2 {
		return cloneChannels(r.Channels)
	}
	members := []string{r.Human.ID}
	for _, member := range r.Members {
		if !member.Revoked {
			members = append(members, member.ID)
		}
	}
	slices.Sort(members)
	return []Channel{{ID: LegacyChannelID(r.ID), Name: DefaultChannel, Members: members}}
}

func (r *Room) validateChannels() error {
	if r.Schema == 1 {
		if r.Channels != nil || r.DeletedChannels != nil {
			return errors.New("managed chat schema 1 cannot contain a channel catalog")
		}
		return nil
	}
	if len(r.Channels) == 0 || len(r.Channels) > MaxChannels || r.Channels[0].Name != DefaultChannel {
		return errors.New("managed chat needs a bounded channel catalog with #space first")
	}
	active := map[string]bool{r.Human.ID: true}
	for _, m := range r.Members {
		if !m.Revoked {
			active[m.ID] = true
		}
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for i, channel := range r.Channels {
		if err := layout.ValidateIdentity(channel.ID); err != nil {
			return err
		}
		if err := ValidateChannelName(channel.Name); err != nil {
			return err
		}
		if ids[channel.ID] || names[channel.Name] || i > 1 && r.Channels[i-1].Name >= channel.Name {
			return errors.New("managed channel IDs/names must be unique and catalog ordered")
		}
		ids[channel.ID], names[channel.Name] = true, true
		if len(channel.Members) == 0 || len(channel.Members) > MaxMembers+1 || !slices.Contains(channel.Members, r.Human.ID) {
			return errors.New("managed channel must contain the human and a bounded active-member ACL")
		}
		for j, id := range channel.Members {
			if !active[id] || j > 0 && channel.Members[j-1] >= id {
				return errors.New("managed channel ACL must contain sorted unique active member IDs")
			}
		}
	}
	return ValidateDeletedChannels(r.DeletedChannels, ids, r.Generation)
}

// ValidateDeletedChannels checks a deletion ledger against the active channel
// IDs and the room generation: bounded, never the default channel, IDs unique
// and disjoint from active ones, generations positive, ordered and not newer
// than the room. Names may recur, in active entries or later records.
func ValidateDeletedChannels(deleted []DeletedChannel, activeIDs map[string]bool, generation uint64) error {
	if len(deleted) > MaxDeletedChannels {
		return errors.New("managed channel deletion ledger is full")
	}
	seen := map[string]bool{}
	for i, channel := range deleted {
		if err := layout.ValidateIdentity(channel.ID); err != nil {
			return err
		}
		if err := ValidateChannelName(channel.Name); err != nil {
			return err
		}
		if channel.Name == DefaultChannel || activeIDs[channel.ID] || seen[channel.ID] {
			return errors.New("deleted managed channel must be a unique non-default identity outside the active catalog")
		}
		seen[channel.ID] = true
		if channel.Generation == 0 || channel.Generation > generation || i > 0 && deleted[i-1].Generation > channel.Generation {
			return errors.New("deleted managed channel generations must be positive, ordered and not newer than the room")
		}
	}
	return nil
}

// RemovedChannelNames is the server's explicit deletion intent: deleted names
// absent from the current active catalog, sorted and unique. A current active
// name is never listed, even if an older identity with that name was deleted.
func (r *Room) RemovedChannelNames() []string {
	active := map[string]bool{}
	for _, channel := range r.ChannelCatalog() {
		active[channel.Name] = true
	}
	var names []string
	for _, channel := range r.DeletedChannels {
		if !active[channel.Name] && !slices.Contains(names, channel.Name) {
			names = append(names, channel.Name)
		}
	}
	slices.Sort(names)
	return names
}

func (r *Room) reidentifyChannels(disk string) {
	r.Channels = cloneChannels(r.Channels)
	for i := range r.Channels {
		r.Channels[i].ID = ImportedID(disk, r.Channels[i].ID)
		for j, id := range r.Channels[i].Members {
			r.Channels[i].Members[j] = ImportedID(disk, id)
		}
		slices.Sort(r.Channels[i].Members)
	}
	// Deletion identities transfer with fresh IDs. The restored room starts a
	// new generation sequence, so every record belongs to its first one.
	r.DeletedChannels = slices.Clone(r.DeletedChannels)
	for i := range r.DeletedChannels {
		r.DeletedChannels[i].ID = ImportedID(disk, r.DeletedChannels[i].ID)
		r.DeletedChannels[i].Generation = r.Generation
	}
}

func (r *Room) enrollDefaultChannel(memberID string) {
	if r.Schema == 2 {
		r.Channels[0].Members = append(r.Channels[0].Members, memberID)
		slices.Sort(r.Channels[0].Members)
	}
}

func (r *Room) revokeChannels(memberID string) {
	for i := range r.Channels {
		r.Channels[i].Members = slices.DeleteFunc(r.Channels[i].Members, func(id string) bool { return id == memberID })
	}
}

func (s Store) channelMutation(roomID string, generation uint64, inventory Inventory) (*Room, error) {
	if err := layout.ValidateIdentity(roomID); err != nil {
		return nil, err
	}
	r, err := s.mutation(generation, inventory)
	if err != nil {
		return nil, err
	}
	if r.ID != roomID {
		return nil, errors.New("managed channel belongs to a different room")
	}
	id, declared := inventory.Declarations[r.Server.Name]
	if declared && id != r.Server.ID || !declared && inventory.Runtime[r.Server.Name] {
		return nil, errors.New("managed server incarnation conflicts with its room reservation")
	}
	return r, nil
}

// CreateChannel records desired permission for current active room members.
// The caller owns runtime preflight/application, native locking and receipts.
// A repeated name is a no-op only after all identity/CAS/ownership checks.
func (s Store) CreateChannel(roomID, name string, generation uint64, inventory Inventory) (*Room, error) {
	if err := ValidateChannelName(name); err != nil {
		return nil, err
	}
	r, err := s.channelMutation(roomID, generation, inventory)
	if err != nil {
		return nil, err
	}
	catalog := r.ChannelCatalog()
	for _, channel := range catalog {
		if channel.Name == name {
			return r, nil
		}
	}
	if len(catalog) >= MaxChannels {
		return nil, errors.New("managed channel catalog is full")
	}
	members := []string{r.Human.ID}
	for _, member := range r.Members {
		if !member.Revoked {
			members = append(members, member.ID)
		}
	}
	slices.Sort(members)
	catalog = append(catalog, Channel{ID: NewID(), Name: name, Members: members})
	slices.SortFunc(catalog[1:], func(a, b Channel) int { return strings.Compare(a.Name, b.Name) })
	r.Schema, r.Channels = 2, catalog
	r.Generation++
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteChannel removes one non-default channel from the desired catalog and
// records only its identity under the new generation. The caller owns the
// runtime capability/proof preflight, publication, the one reload and receipts.
func (s Store) DeleteChannel(roomID, channelID string, generation uint64, inventory Inventory) (*Room, error) {
	if err := layout.ValidateIdentity(channelID); err != nil {
		return nil, err
	}
	r, err := s.channelMutation(roomID, generation, inventory)
	if err != nil {
		return nil, err
	}
	catalog := r.ChannelCatalog()
	index := slices.IndexFunc(catalog, func(c Channel) bool { return c.ID == channelID })
	if index < 0 {
		return nil, errors.New("managed channel identity is not in this room")
	}
	if index == 0 || catalog[index].Name == DefaultChannel {
		return nil, errors.New("the default channel #space cannot be deleted")
	}
	if len(r.DeletedChannels) >= MaxDeletedChannels {
		return nil, errors.New("managed channel deletion ledger is full")
	}
	r.Generation++
	r.DeletedChannels = append(slices.Clone(r.DeletedChannels), DeletedChannel{ID: channelID, Name: catalog[index].Name, Generation: r.Generation})
	r.Schema, r.Channels = 2, slices.Delete(catalog, index, index+1)
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}

// SetChannelMember changes one desired ACL, never the room account or token.
// Every selector is immutable and scoped to the verified room/disk. Box names
// are resolved from this room's ledger. Grants require matching declarations;
// removal may reduce access after that declaration disappears or is replaced.
func (s Store) SetChannelMember(roomID, channelID, memberID, boxID string, allowed bool, generation uint64, inventory Inventory) (*Room, error) {
	for _, id := range []string{channelID, memberID, boxID} {
		if err := layout.ValidateIdentity(id); err != nil {
			return nil, err
		}
	}
	r, err := s.channelMutation(roomID, generation, inventory)
	if err != nil {
		return nil, err
	}
	current := false
	for _, member := range r.Members {
		if member.ID == memberID && !member.Revoked && member.BoxID == boxID && (!allowed || inventory.Declarations[member.Name] == boxID) {
			current = true
			break
		}
	}
	if !current {
		return nil, errors.New("channel ACL change requires the current enrolled member and box identity")
	}
	catalog := r.ChannelCatalog()
	index := slices.IndexFunc(catalog, func(c Channel) bool { return c.ID == channelID })
	if index < 0 {
		return nil, errors.New("managed channel identity is not in this room")
	}
	if slices.Contains(catalog[index].Members, memberID) == allowed {
		return r, nil
	}
	if allowed {
		catalog[index].Members = append(catalog[index].Members, memberID)
		slices.Sort(catalog[index].Members)
	} else {
		catalog[index].Members = slices.DeleteFunc(catalog[index].Members, func(id string) bool { return id == memberID })
	}
	r.Schema, r.Channels = 2, catalog
	r.Generation++
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}
