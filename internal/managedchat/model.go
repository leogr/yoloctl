// Package managedchat owns the native desired state of a space's managed room.
// It does not start workloads, expose ports, or imply that desired membership
// has reached a running AIRC server. The controller holds its native space lock
// across inventory, mutations and publication; readers see whole generations.
package managedchat

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/leogr/yoloctl/internal/layout"
)

const MaxDocument = 1 << 20
const MaxMembers = 1024
const DefaultBox = "chat"
const DefaultChannel = "#space"

type Box struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type Member struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BoxID           string `json:"box_id,omitempty"` // empty only for the human
	Revoked         bool   `json:"revoked,omitempty"`
	Nickname        string `json:"nickname,omitempty"`
	ChannelOperator bool   `json:"channel_operator,omitempty"` // controller-derived runtime grant
}

// Room contains no tokens. IDs and generations alone never grant runtime
// access. Prepared describes private storage, not a live server or enrollment.
type Room struct {
	Schema   int       `json:"schema"`
	ID       string    `json:"id"`
	DiskID   string    `json:"disk_id"`
	Server   Box       `json:"server"`
	Channel  string    `json:"channel"`
	Channels []Channel `json:"channels,omitempty"`
	// DeletedChannels holds only the bounded identity of explicitly deleted
	// channels: no content and no access. Readers tell a deletion from a
	// corrupt catalog by it, and a reused name stays a distinct channel.
	DeletedChannels []DeletedChannel `json:"deleted_channels,omitempty"`
	Generation      uint64           `json:"generation"`
	Prepared        bool             `json:"prepared"`
	// RuntimeEnabled opts into controller-derived binds. It is desired state,
	// never proof that a workload or ACL generation is applied.
	RuntimeEnabled bool     `json:"runtime_enabled,omitempty"`
	Human          Member   `json:"human"`
	Members        []Member `json:"members,omitempty"`
}

func NewID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func (b Box) Validate() error {
	if err := layout.ValidateName("agentbox", b.Name); err != nil {
		return err
	}
	return layout.ValidateIdentity(b.ID)
}

func (r *Room) Validate() error {
	if (r.Schema != 1 && r.Schema != 2) || r.Channel != DefaultChannel || r.Generation == 0 || r.Generation == ^uint64(0) || len(r.Members) > MaxMembers {
		return errors.New("invalid managed chat schema, channel, generation or member count")
	}
	for _, id := range []string{r.ID, r.DiskID, r.Human.ID} {
		if err := layout.ValidateIdentity(id); err != nil {
			return err
		}
	}
	if err := r.Server.Validate(); err != nil {
		return err
	}
	if r.Human.BoxID != "" || r.Human.Revoked || r.Human.ChannelOperator {
		return errors.New("invalid managed chat human identity")
	}
	if r.Human.Nickname != "" {
		if err := ValidateNickname(r.Human.Nickname); err != nil {
			return err
		}
	}
	if r.Human.Name == r.Server.Name {
		return errors.New("human and server names must be distinct")
	}
	if err := layout.ValidateName("chat human", r.Human.Name); err != nil {
		return err
	}
	ids := map[string]bool{r.Human.ID: true}
	active := map[string]bool{r.Human.Name: true, r.Server.Name: true}
	for _, m := range r.Members {
		if m.Nickname != "" || m.Revoked && m.ChannelOperator {
			return errors.New("invalid managed chat member nickname or revoked operator grant")
		}
		if err := (Box{Name: m.Name, ID: m.BoxID}).Validate(); err != nil {
			return err
		}
		if err := layout.ValidateIdentity(m.ID); err != nil {
			return err
		}
		if ids[m.ID] || !m.Revoked && active[m.Name] {
			return errors.New("duplicate managed chat identity or active member name")
		}
		ids[m.ID] = true
		if !m.Revoked {
			active[m.Name] = true
		}
	}
	return r.validateChannels()
}

func Parse(raw []byte) (*Room, error) {
	if len(raw) > MaxDocument {
		return nil, errors.New("managed chat declaration exceeds size limit")
	}
	var r Room
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return nil, errors.New("invalid managed chat declaration")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing managed chat declaration data")
	}
	// Even a null/empty new field is not a schema-1 declaration. Keep legacy
	// reads and ordinary saves compatible with the prior strict decoder.
	if r.Schema == 1 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, errors.New("invalid managed chat declaration")
		}
		for _, field := range []string{"channels", "deleted_channels"} {
			if _, present := fields[field]; present {
				return nil, errors.New("managed chat schema 1 cannot contain a channel catalog")
			}
		}
	}
	return &r, r.Validate()
}

func (r *Room) Marshal() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if len(raw) > MaxDocument {
		return nil, errors.New("managed chat declaration exceeds size limit")
	}
	return raw, err
}

// ImportedID deterministically binds an old identity to a fresh disk identity.
// These are public identifiers, not authentication keys. Determinism allows
// an interrupted restore to inspect/reuse exactly its own pending state.
func ImportedID(disk, old string) string {
	sum := sha256.Sum256([]byte("yoloctl-chat-import-v1\x00" + disk + "\x00" + old))
	return hex.EncodeToString(sum[:16])
}

func (r *Room) Reidentify(disk string) (*Room, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if err := layout.ValidateIdentity(disk); err != nil {
		return nil, err
	}
	if disk == r.DiskID {
		return nil, errors.New("managed chat restore requires a new disk identity")
	}
	next := *r
	next.ID, next.DiskID = ImportedID(disk, r.ID), disk
	next.Server.ID = ImportedID(disk, r.Server.ID)
	next.Human.ID = ImportedID(disk, r.Human.ID)
	next.Generation, next.Prepared = 1, false
	next.Members = append([]Member(nil), r.Members...)
	for i := range next.Members {
		next.Members[i].ID = ImportedID(disk, r.Members[i].ID)
		next.Members[i].BoxID = ImportedID(disk, r.Members[i].BoxID)
		// Runtime authority never transfers to an independent restored space.
		next.Members[i].ChannelOperator = false
	}
	next.reidentifyChannels(disk)
	return &next, next.Validate()
}
