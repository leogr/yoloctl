package managedchat

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/pelletier/go-toml/v2"
)

const ServerConfigFile = "server.toml"
const ClientConfigFile = "client.toml"

// AuthAccount fits AIRC's 24-byte, case-folded nick/account namespace. The
// retained ledger is checked for encoding collisions before any publication.
// It carries 115 bits from the immutable ID; a collision is an explicit error,
// never a decision to share credentials or reuse another member's authority.
func AuthAccount(id string) (string, error) {
	if err := layout.ValidateIdentity(id); err != nil {
		return "", err
	}
	raw, _ := hex.DecodeString(id)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return "m" + strings.ToLower(encoded[:23]), nil
}

func MemberNick(m Member) string {
	if m.Nickname != "" {
		return m.Nickname
	}
	if ValidateNickname(m.Name) == nil {
		return m.Name
	}
	account, _ := AuthAccount(m.ID)
	return account
}

// RoomNicknames allocates display nicknames in durable enrollment order, human
// first. Reserve every immutable account (including revoked ones) before using
// display names, so a colliding name always has a unique immutable fallback.
// A later enrollment cannot steal an existing nickname or poison rendering.
func RoomNicknames(r *Room) (map[string]string, error) {
	all := append([]Member{r.Human}, r.Members...)
	accounts := map[string]string{}
	for _, m := range all {
		a, err := AuthAccount(m.ID)
		if err != nil {
			return nil, err
		}
		if _, exists := accounts[a]; exists {
			return nil, errors.New("managed chat authentication ID encoding collision")
		}
		accounts[a] = m.ID
	}
	nicks, used := map[string]string{}, map[string]bool{}
	for _, m := range all {
		if m.Revoked {
			continue
		}
		nick := MemberNick(m)
		folded := strings.ToLower(nick)
		reserved := accounts[folded]
		if used[folded] || reserved != "" && reserved != m.ID {
			nick, _ = AuthAccount(m.ID)
		}
		nicks[m.ID], used[strings.ToLower(nick)] = nick, true
	}
	return nicks, nil
}

type serverDocument struct {
	ServerName      string `toml:"server_name"`
	ListenUnix      string `toml:"listen_unix"`
	ControlUnix     string `toml:"control_unix"`
	Generation      uint64 `toml:"membership_generation"`
	Database        string `toml:"database"`
	PrivateMessages bool   `toml:"allow_private_messages"`
	// RemovedChannels is AIRC's explicit deletion intent (channel-delete-reload).
	RemovedChannels []string        `toml:"removed_channels,omitempty"`
	Accounts        []serverAccount `toml:"accounts"`
	Channels        []serverChannel `toml:"channels"`
	Artifacts       serverArtifacts `toml:"artifacts"`
}
type serverAccount struct {
	Name        string `toml:"name"`
	TokenSHA256 string `toml:"token_sha256"`
}
type serverChannel struct {
	Name      string   `toml:"name"`
	Members   []string `toml:"members"`
	Operators []string `toml:"operators"`
}
type serverArtifacts struct {
	ListenUnix string `toml:"listen_unix"`
	PublicURL  string `toml:"public_url"`
}
type clientDocument struct {
	Unix         string   `toml:"unix"`
	ArtifactUnix string   `toml:"artifact_unix"`
	Account      string   `toml:"account"`
	Nick         string   `toml:"nick"`
	TokenFile    string   `toml:"token_file"`
	Channels     []string `toml:"channels"`
}

// Rendered contains only non-secret proof metadata. Generated server hashes are
// private as defense in depth; neither raw tokens nor config bytes are returned
// through controller status or operation receipts.
type Rendered struct {
	Generation   uint64
	ConfigSHA256 string
	Accounts     []string
}

// Render publishes client leaves first and the server snapshot last. Any error
// preserves the previous live server; no signal/reload/cleanup is implicit. The
// caller holds the native space lock and explicitly applies/observes the result.
type renderFile struct {
	Path string
	Data []byte
}
type renderPlan struct {
	Proof   Rendered
	Server  []byte
	Clients []renderFile
}

type BootstrapIdentity struct {
	ID      string `json:"id"`
	Account string `json:"account"`
	Name    string `json:"name"`
	Nick    string `json:"nick,omitempty"`
	BoxID   string `json:"box_id,omitempty"`
	Role    string `json:"role"`
	Revoked bool   `json:"revoked,omitempty"`
}
type Bootstrap struct {
	Schema         int                 `json:"schema"`
	RoomID         string              `json:"room_id"`
	DiskID         string              `json:"disk_id"`
	Generation     uint64              `json:"desired_generation"`
	HumanAccount   string              `json:"human_account"`
	SelfAccount    string              `json:"self_account"`
	Identities     []BootstrapIdentity `json:"identities"`
	DefaultChannel string              `json:"default_channel,omitempty"`
	Channels       []Channel           `json:"channels,omitempty"`
	// DeletedChannels reaches only the human stream header and catalog reads,
	// never a participant's bootstrap file.
	DeletedChannels []DeletedChannel `json:"deleted_channels,omitempty"`
}

func (s Store) Render(r *Room) (Rendered, error) {
	plan, err := s.buildRender(r)
	if err != nil {
		return Rendered{}, err
	}
	for _, f := range plan.Clients {
		if err := s.put(f.Path, f.Data, s.UID, s.GID); err != nil {
			return Rendered{}, err
		}
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	if err := s.put(filepath.Join(base, "server/config", ServerConfigFile), plan.Server, s.UID, s.GID); err != nil {
		return Rendered{}, err
	}
	return plan.Proof, nil
}

// Expected computes the desired exact-byte configuration proof without writing
// files or applying runtime changes. Status must never repair state as a read.
func (s Store) Expected(r *Room) (Rendered, error) {
	plan, err := s.buildRender(r)
	return plan.Proof, err
}

// VerifyPublished prevents an ordinary server start from loading an older
// grant snapshot after interrupted publication. Explicit chat ensure renders
// before starting/recovering the server; this check never writes or reloads.
func (s Store) VerifyPublished(r *Room) error {
	want, err := s.Expected(r)
	if err != nil {
		return err
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	raw, err := privateFile(s.Disk, filepath.Join(base, "server/config", ServerConfigFile), MaxDocument, s.UID, s.GID)
	if err != nil {
		return errors.New("managed chat server configuration is unavailable; run chat ensure before starting it")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != want.ConfigSHA256 {
		return errors.New("managed chat configuration publication is pending; run chat ensure before starting the server")
	}
	return nil
}

func (s Store) buildRender(r *Room) (renderPlan, error) {
	var plan renderPlan
	var result Rendered
	current, err := s.Load()
	if err != nil {
		return plan, err
	}
	want, err := r.Marshal()
	if err != nil {
		return plan, err
	}
	have, err := current.Marshal()
	if err != nil {
		return plan, err
	}
	if string(want) != string(have) {
		return plan, errors.New("managed chat desired snapshot changed before rendering")
	}

	if !r.RuntimeEnabled || !r.Prepared {
		return plan, errors.New("managed chat runtime is not enabled and prepared")
	}
	if err := s.Verify(r); err != nil {
		return plan, err
	}
	doc := serverDocument{
		ServerName: "yoloctl-" + r.ID[:12], ListenUnix: EndpointMount + "/irc.sock", ControlUnix: ControlMount + "/control.sock",
		Generation: r.Generation, Database: ServerDataMount + "/airc.sqlite3",
		Artifacts: serverArtifacts{ListenUnix: EndpointMount + "/artifacts.sock", PublicURL: "http://airc.local"},
	}
	all := append([]Member{r.Human}, r.Members...)
	human, _ := AuthAccount(r.Human.ID)
	bootstrap := Bootstrap{Schema: 1, RoomID: r.ID, DiskID: r.DiskID, Generation: r.Generation, HumanAccount: human}
	if r.Schema == 2 {
		bootstrap.Schema, bootstrap.DefaultChannel, bootstrap.Channels = 2, r.Channel, r.ChannelCatalog()
	}
	nicks, err := RoomNicknames(r)
	if err != nil {
		return plan, err
	}
	// Validate the entire ledger before writing any generated file. Include
	// revoked IDs so a later member cannot take over a historical account tag.
	for _, m := range all {
		account, err := AuthAccount(m.ID)
		if err != nil {
			return plan, err
		}
		role := "member"
		if m.ID == r.Human.ID {
			role = "human"
		}
		bootstrap.Identities = append(bootstrap.Identities, BootstrapIdentity{ID: m.ID, Account: account, Name: m.Name, Nick: nicks[m.ID], BoxID: m.BoxID, Role: role, Revoked: m.Revoked})
	}
	for _, m := range all {
		if m.Revoked {
			continue
		}
		account, _ := AuthAccount(m.ID)
		leaf, _ := layout.ChatMemberRelative(r.ID, m.ID)
		token, err := privateFile(s.Disk, filepath.Join(leaf, "token"), 128, s.UID, s.GID)
		if err != nil {
			return plan, err
		}
		hash := sha256.Sum256(token)
		doc.Accounts = append(doc.Accounts, serverAccount{Name: account, TokenSHA256: hex.EncodeToString(hash[:])})
		result.Accounts = append(result.Accounts, account)
		client := clientDocument{Unix: doc.ListenUnix, ArtifactUnix: doc.Artifacts.ListenUnix, Account: account, Nick: nicks[m.ID], TokenFile: "token", Channels: []string{r.Channel}}
		raw, err := toml.Marshal(client)
		if err != nil {
			return plan, err
		}
		plan.Clients = append(plan.Clients, renderFile{Path: filepath.Join(leaf, ClientConfigFile), Data: raw})
		bootstrap.SelfAccount = account
		identity, err := json.Marshal(bootstrap)
		if err != nil {
			return plan, err
		}
		plan.Clients = append(plan.Clients, renderFile{Path: filepath.Join(leaf, "identity.json"), Data: identity})
	}
	operators := []string{human}
	for _, m := range r.Members {
		if !m.Revoked && m.ChannelOperator {
			account, _ := AuthAccount(m.ID)
			operators = append(operators, account)
		}
	}
	doc.Channels = renderChannels(r, result.Accounts, operators)
	doc.RemovedChannels = r.RemovedChannelNames()
	raw, err := toml.Marshal(doc)
	if err != nil {
		return plan, err
	}
	digest := sha256.Sum256(raw)
	result.Generation, result.ConfigSHA256 = r.Generation, hex.EncodeToString(digest[:])
	plan.Proof, plan.Server = result, raw
	return plan, nil
}

// Configured operator accounts still need permission in the individual channel.
// The caller derives operator authority; this renderer only intersects ACLs.
// Preserve schema-1 order/bytes so ordinary legacy publication does not migrate.
func renderChannels(r *Room, accounts, operators []string) []serverChannel {
	if r.Schema == 1 {
		return []serverChannel{{Name: r.Channel, Members: accounts, Operators: operators}}
	}
	channels := make([]serverChannel, 0, len(r.Channels))
	for _, channel := range r.Channels {
		next := serverChannel{Name: channel.Name}
		allowed := make(map[string]bool, len(channel.Members))
		for _, id := range channel.Members {
			account, _ := AuthAccount(id) // the entire room has already validated
			next.Members = append(next.Members, account)
			allowed[account] = true
		}
		for _, account := range operators {
			if allowed[account] {
				next.Operators = append(next.Operators, account)
			}
		}
		channels = append(channels, next)
	}
	return channels
}
