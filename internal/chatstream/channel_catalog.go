package chatstream

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func headerHuman(h Header) (managedchat.BootstrapIdentity, error) {
	var human managedchat.BootstrapIdentity
	for _, identity := range h.Identities {
		if identity.Role == "human" && !identity.Revoked {
			if human.ID != "" || identity.Account != h.HumanAccount {
				return human, errors.New("ambiguous managed human identity")
			}
			human = identity
		} else if identity.Role != "member" {
			return human, errors.New("invalid managed identity role")
		}
	}
	if human.ID == "" {
		return human, errors.New("missing managed human identity")
	}
	return human, nil
}

func validateHeaderChannels(h Header) error {
	human, err := headerHuman(h)
	if err != nil {
		return err
	}
	if !validReference(h.Nick, 24) {
		return errors.New("invalid managed human nickname")
	}
	if h.Schema == 1 {
		if h.Channels != nil || h.DefaultChannel != "" || h.DeletedChannels != nil {
			return errors.New("legacy managed header cannot contain a new catalog")
		}
		return nil
	}
	if h.DefaultChannel != h.Channel || len(h.Channels) == 0 || len(h.Channels) > managedchat.MaxChannels || h.Channels[0].Name != h.Channel {
		return errors.New("invalid managed channel catalog header")
	}
	active := make(map[string]bool, len(h.Identities))
	for _, identity := range h.Identities {
		active[identity.ID] = !identity.Revoked
	}
	seenIDs, seenNames := map[string]bool{}, map[string]bool{}
	for i, channel := range h.Channels {
		if layout.ValidateIdentity(channel.ID) != nil || managedchat.ValidateChannelName(channel.Name) != nil || seenIDs[channel.ID] || seenNames[channel.Name] || i > 1 && h.Channels[i-1].Name >= channel.Name {
			return errors.New("invalid managed channel identity or order")
		}
		seenIDs[channel.ID], seenNames[channel.Name] = true, true
		if len(channel.Members) == 0 || len(channel.Members) > managedchat.MaxMembers+1 || !slices.Contains(channel.Members, human.ID) {
			return errors.New("managed channel lacks bounded human access")
		}
		for n, id := range channel.Members {
			if !active[id] || n > 0 && channel.Members[n-1] >= id {
				return errors.New("invalid managed channel member references or order")
			}
		}
	}
	return managedchat.ValidateDeletedChannels(h.DeletedChannels, seenIDs, h.Generation)
}

// normalizedHeader projects legacy metadata without writing it, so equal native
// generations can be compared consistently to a read-only catalog refresh.
func normalizedHeader(h Header) Header {
	h.Identities = slices.Clone(h.Identities)
	if h.Schema == 1 {
		members := []string{}
		for _, identity := range h.Identities {
			if !identity.Revoked {
				members = append(members, identity.ID)
			}
		}
		slices.Sort(members)
		h.Schema, h.DefaultChannel = 2, h.Channel
		h.Channels = []managedchat.Channel{{ID: managedchat.LegacyChannelID(h.RoomID), Name: h.Channel, Members: members}}
	} else {
		h.Channels = slices.Clone(h.Channels)
		for i := range h.Channels {
			h.Channels[i].Members = slices.Clone(h.Channels[i].Members)
		}
	}
	h.DeletedChannels = slices.Clone(h.DeletedChannels)
	return h
}

// receiveCatalog preserves local read identity on both accepted and refused
// asynchronous snapshots. Validation remains in refreshCatalog.
func (d *channelDriver) receiveCatalog(c Catalog) {
	if err := d.refreshCatalog(c); err != nil {
		d.emit(Event{Kind: "catalog", State: "refused", Err: err, CatalogRead: c.Read})
	}
}

func (d *channelDriver) refreshCatalog(c Catalog) error {
	if c.RuntimeUnobserved {
		c.RuntimeApplied, c.Pending = false, nil
	}
	if err := validateHeader(c.Header); err != nil {
		return err
	}
	if raw, err := json.Marshal(c); err != nil || len(raw) > ResponseLimit {
		return errors.New("managed catalog exceeds its response bound")
	}
	next := normalizedHeader(c.Header)
	previous := normalizedHeader(d.catalog)
	// Attachments is probed once on the confined client connection, not part
	// of the native room generation. Catalog reads omit it; they must neither
	// conflict with that probe nor change the capability on a live stream.
	next.Attachments = d.wire.header.Attachments
	if next.RoomID != previous.RoomID || next.DiskID != previous.DiskID || next.SelfAccount != previous.SelfAccount || next.HumanAccount != previous.HumanAccount {
		return errors.New("catalog identity changed; reconnect explicitly")
	}
	if next.Generation < previous.Generation || next.Generation == previous.Generation && !reflect.DeepEqual(next, previous) {
		return errors.New("catalog generation is stale or has conflicting content")
	}
	// A channel may leave the catalog only with its own deletion record, and
	// that ledger only grows. A same-name channel with a new ID never revives
	// the deleted one on this connection.
	for _, old := range previous.Channels {
		if !slices.ContainsFunc(next.Channels, func(channel managedchat.Channel) bool { return channel.ID == old.ID && channel.Name == old.Name }) &&
			!slices.ContainsFunc(next.DeletedChannels, func(deleted managedchat.DeletedChannel) bool { return deleted.ID == old.ID && deleted.Name == old.Name }) {
			return errors.New("catalog replaced or removed an existing channel identity")
		}
	}
	for _, old := range previous.DeletedChannels {
		if !slices.Contains(next.DeletedChannels, old) {
			return errors.New("catalog dropped a channel deletion record")
		}
	}
	// A saved nickname is a next-connection preference. Keep the current
	// protocol recipient/echo identity until reconnect, while comparing future
	// generations against the complete desired snapshot (including its nick).
	d.catalog = normalizedHeader(next)
	liveNick := d.wire.header.Nick
	d.wire.header = normalizedHeader(next)
	d.wire.header.Nick = liveNick
	for _, channel := range next.Channels {
		if d.channels[channel.Name] == nil {
			d.addChannel(channel.Name, "left")
		}
	}
	c.Header, c.Pending = normalizedHeader(next), slices.Clone(c.Pending)
	d.emit(Event{Kind: "catalog", Catalog: &c})
	d.markDeleted(next.DeletedChannels)
	return nil
}

// errChannelDeleted refuses, before Write, any request for a name deleted
// after this connection opened, even if a new channel now has that name.
var errChannelDeleted = errors.New("channel was deleted/recreated; open a new chat connection to use it")

// markDeleted makes each name deleted after this connection's original
// generation unusable here. Its pending send or control keeps its genuine
// server outcome; queued setup and history for it are dropped; other
// channels and the connection continue.
func (d *channelDriver) markDeleted(deleted []managedchat.DeletedChannel) {
	for _, record := range deleted {
		channel := d.channels[record.Name]
		if record.Generation <= d.origin || channel == nil || channel.deleted {
			continue
		}
		channel.deleted = true
		channel.setup, channel.whoPending, channel.until = false, false, time.Time{}
		channel.adapter.roster, channel.adapter.names = map[string]Member{}, nil
		d.cancelChannelHistory(record.Name)
		d.emit(Event{Kind: "membership", Channel: record.Name, State: "deleted", Err: errChannelDeleted})
	}
}
