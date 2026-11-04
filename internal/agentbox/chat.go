package agentbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func refreshChatProjection(c *Config) error {
	c.chat = nil
	native := spaceenv.Current()
	if native == nil {
		return nil
	}
	if err := spaceenv.RequireSpace(c.Space); err != nil {
		return err
	}
	s := managedchat.Store{Disk: native.DiskMount, DiskID: native.DiskID, UID: native.UID, GID: native.GID}
	p, err := s.Projection(managedchat.Box{Name: c.Name, ID: c.ID})
	if err != nil {
		return fmt.Errorf("managed chat projection: %w", err)
	}
	if p != nil && p.Server {
		if err := ValidateChatServer(c, managedchat.Box{Name: c.Name, ID: p.BoxID}); err != nil {
			return err
		}
	}
	c.chat = p
	return nil
}

// SameDeclaration reports whether two configs declare the same box. The managed
// chat projection is runtime state that validation derives from the native
// room (VerifyChatRuntime refreshes it), so it is not compared here; callers
// verify bindings and identities against the runtime separately.
func SameDeclaration(a, b *Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.chat, y.chat = nil, nil
	return reflect.DeepEqual(x, y)
}

// ValidateChatDeclaration runs before a native declaration save or lifecycle
// teardown, even when explicit recreation is authorized. It does no I/O on a
// host-only ordinary Save and never mints IDs or enables a dormant room.
func ValidateChatDeclaration(c *Config) error { return refreshChatProjection(c) }

func ChatServerCommand() []string {
	return []string{"/bin/sh", "-c", "umask 077; exec /usr/local/bin/aircd serve --config " + managedchat.ServerConfigMount + "/server.toml"}
}

// ValidateChatServer also guards ordinary box create/recreate paths. The
// reserved declaration ID alone must never project human credentials/control
// into a declaration edited into an agent, operator or different workload.
func ValidateChatServer(c *Config, reserved managedchat.Box) error {
	if c == nil || c.Name != reserved.Name || c.ID != reserved.ID || c.Agent != "none" || c.Account != "none" || normalizeImage(c.Image) != normalizeImage(agentboximage.RefAgentbox) || c.Workdir != NewEphemeralWorkdir(reserved.Name) || c.UnconfinedNetwork || c.Privileged || len(c.Ports) != 0 || len(c.Mounts) != 0 || len(c.Env) != 0 || len(c.Capabilities) != 0 || len(c.AgentExtraArgs) != 0 || c.AgentSession != nil || len(c.FeatureOptions) != 0 || len(c.FeatureLocks) != 0 || !slices.Equal(c.Features, []string{"yolo.chat.skill"}) || !slices.Equal(c.SessionCommand, ChatServerCommand()) {
		return errors.New("managed chat server declaration no longer matches its confined reserved workload; inspect it before recovery")
	}
	return c.Validate()
}

// PrepareChatRuntime is shared by container create and existing-container start.
// It only prepares stable directory binds; it never removes socket inodes.
func PrepareChatRuntime(c *Config) error {
	if err := VerifyChatServerPublication(c); err != nil {
		return err
	}
	if c.chat == nil {
		return nil
	}
	native := spaceenv.Current()
	return managedchat.PrepareEndpoints(spaceenv.RunRoot, c.chat, native.UID, native.GID)
}

// VerifyChatServerPublication validates the current native reservation and
// published configuration before a fresh server process can start. Container
// preparation and holder-only revival share this check: a running container
// does not prove that its next server process would load current authority.
// This never prepares endpoints, renders files or retries an uncertain reload.
func VerifyChatServerPublication(c *Config) error {
	if err := refreshChatProjection(c); err != nil {
		return err
	}
	if c.chat == nil || !c.chat.Server {
		return nil
	}
	native := spaceenv.Current()
	store := managedchat.Store{Disk: native.DiskMount, DiskID: native.DiskID, UID: native.UID, GID: native.GID}
	r, err := store.Load()
	if err != nil {
		return err
	}
	return store.VerifyPublished(r)
}

func chatMounts(p *managedchat.Projection) []Mount {
	if p == nil {
		return nil
	}
	result := make([]Mount, 0, len(p.Binds))
	for _, b := range p.Binds {
		result = append(result, Mount{Source: b.Source, Target: b.Target, ReadOnly: b.ReadOnly})
	}
	return result
}

func chatRole(p *managedchat.Projection) string {
	if p.Server {
		return "server"
	}
	return "member"
}

func addChatRunOptions(c *Config, opts *container.RunOptions) error {
	if err := PrepareChatRuntime(c); err != nil {
		return err
	}
	p := c.chat
	if p == nil {
		return nil
	}
	for _, m := range opts.Mounts {
		for _, b := range p.Binds {
			if chatTargetsOverlap(m.Destination, b.Target) {
				return errors.New("ordinary mounts cannot overlap managed chat infrastructure")
			}
		}
	}
	for _, b := range p.Binds {
		opts.Mounts = append(opts.Mounts, container.Mount{Source: b.Source, Destination: b.Target, ReadOnly: b.ReadOnly})
	}
	opts.Labels[managedchat.RoomLabel] = p.RoomID
	opts.Labels[managedchat.BoxLabel] = p.BoxID
	opts.Labels[managedchat.MemberLabel] = p.MemberID
	opts.Labels[managedchat.RoleLabel] = chatRole(p)
	opts.Labels[managedchat.IncarnationLabel] = NewIdentity()
	return addChatEnvironment(c, opts, p)
}

// chatServerEnvironment points the managed server's temporary files at /tmp,
// which its sandbox may write. Unset, SQLite falls back to /var/tmp: its
// mode bits pass SQLite's access() probe, but the sandbox denies creating
// files there, so a publication that needs a temporary file fails to store.
// SQLITE_TMPDIR is set too because SQLite prefers it to TMPDIR.
var chatServerEnvironment = map[string]string{"TMPDIR": "/tmp", "SQLITE_TMPDIR": "/tmp"}

// Confined SSH children use env_clear. Supply non-secret discovery values,
// and the server's temporary directory, through the supervisor's existing
// child-environment channel, preserving the account injection map without
// exposing any value in diagnostics. The channel is read when the container
// is created, so only a recreation applies a change.
func addChatEnvironment(c *Config, opts *container.RunOptions, p *managedchat.Projection) error {
	identityPath := managedchat.IdentityMount
	if p.Server {
		identityPath = managedchat.HumanMount
	}
	account, _ := managedchat.AuthAccount(p.MemberID)
	env := map[string]string{}
	index := -1
	for i, raw := range opts.Env {
		if value, ok := strings.CutPrefix(raw, openshellUserEnvironment+"="); ok {
			if index >= 0 || json.Unmarshal([]byte(value), &env) != nil || env == nil {
				return errors.New("invalid or duplicate sandbox child environment")
			}
			index = i
		}
	}
	for key, value := range map[string]string{"YOLOCTL_CHAT_CONFIG": identityPath + "/client.toml", "YOLOCTL_CHAT_ROOM": p.RoomID, "YOLOCTL_CHAT_ACCOUNT": account} {
		if _, exists := c.Env[key]; exists {
			return fmt.Errorf("%s is reserved for managed chat enrollment", key)
		}
		if _, exists := env[key]; exists {
			return errors.New("account injection overlaps managed chat discovery")
		}
		env[key] = value
	}
	if p.Server {
		for key, value := range chatServerEnvironment {
			if _, exists := c.Env[key]; exists {
				return fmt.Errorf("%s is reserved for the managed chat server", key)
			}
			if _, exists := env[key]; exists {
				return errors.New("sandbox child environment overlaps the managed chat server's temporary directory")
			}
			env[key] = value
		}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	value := openshellUserEnvironment + "=" + string(raw)
	if index < 0 {
		opts.Env = append(opts.Env, value)
	} else {
		opts.Env[index] = value
	}
	return nil
}

func chatBindingReason(c *Config, actual ActualState) string {
	want := chatMounts(c.chat)
	if reason := extraMountsRecreateReason(want, actual.ChatMounts); reason != "" {
		return "managed chat bindings changed; explicit box recreate required"
	}
	p := c.chat
	if p == nil {
		for _, label := range []string{managedchat.RoomLabel, managedchat.BoxLabel, managedchat.MemberLabel, managedchat.RoleLabel, managedchat.IncarnationLabel} {
			if actual.Labels[label] != "" {
				return "managed chat identity was revoked or changed; explicit box recreate required"
			}
		}
		return ""
	}
	if actual.Labels[managedchat.RoomLabel] != p.RoomID || actual.Labels[managedchat.BoxLabel] != p.BoxID || actual.Labels[managedchat.MemberLabel] != p.MemberID || actual.Labels[managedchat.RoleLabel] != chatRole(p) || layout.ValidateIdentity(actual.Labels[managedchat.IncarnationLabel]) != nil {
		return "managed chat runtime identity differs; explicit box recreate required"
	}
	return ""
}

// chatServerRuntimeReason is the managed server's create-time deployment
// drift that Diff reports: its children's temporary directory, read from the
// inspected environment. It is no authority check, so runtime verification
// (and the connections it gates) keeps serving an older server until an
// explicit recreation. An uninspected environment is unverified, not healthy.
func chatServerRuntimeReason(c *Config, actual ActualState) string {
	if c.chat == nil || !c.chat.Server {
		return ""
	}
	return chatServerDeploymentReason(actual.Env)
}

// ChatServerDeploymentDrift is the same drift read from one inspection of
// the room's server container, for status: a reason to show, never an
// authority check. The caller has verified that details are the server's.
func ChatServerDeploymentDrift(details *container.Details) string {
	if details == nil {
		return chatServerDeploymentReason(nil)
	}
	return chatServerDeploymentReason(details.Env)
}

func chatServerDeploymentReason(env []string) string {
	if env == nil {
		return "managed chat server runtime environment was not inspected, so its temporary directory is unverified; inspect the server box again"
	}
	return chatServerEnvironmentReason(env)
}

// chatServerEnvironmentReason checks the server container's actual child
// environment: exactly one envelope, a JSON object of strings, with each
// chatServerEnvironment value. No value is ever part of the reason.
func chatServerEnvironmentReason(env []string) string {
	const recreate = "; explicit box recreate required"
	var raw []string
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, openshellUserEnvironment+"="); ok {
			raw = append(raw, value)
		}
	}
	switch len(raw) {
	case 0:
		return "managed chat server runtime lacks its sandbox child environment" + recreate
	case 1:
	default:
		return "managed chat server runtime has more than one sandbox child environment" + recreate
	}
	var values map[string]*string
	if json.Unmarshal([]byte(raw[0]), &values) != nil || values == nil || slices.Contains(slices.Collect(maps.Values(values)), nil) {
		return "managed chat server runtime has an invalid sandbox child environment" + recreate
	}
	for _, key := range slices.Sorted(maps.Keys(chatServerEnvironment)) {
		if v := values[key]; v == nil || *v != chatServerEnvironment[key] {
			return "managed chat server runtime lacks its temporary directory (" + key + ")" + recreate
		}
	}
	return ""
}

// VerifyChatRuntime rejects name reuse, copied/stale labels and any missing,
// duplicate, writable or foreign bind. It does not prove server application.
func VerifyChatRuntime(c *Config, details *container.Details) error {
	if details == nil {
		return errors.New("managed chat requires authoritative container inspection")
	}
	if err := refreshChatProjection(c); err != nil {
		return err
	}
	return verifyChatRuntimeProjection(c, details)
}

func verifyChatRuntimeProjection(c *Config, details *container.Details) error {
	a := ActualState{Labels: details.Labels}
	seen := map[string]bool{}
	for _, m := range details.Mounts {
		if c.chat != nil {
			for _, b := range c.chat.Binds {
				if m.Destination != b.Target && chatTargetsOverlap(m.Destination, b.Target) {
					return errors.New("runtime mount overlaps managed chat authority")
				}
			}
		}
		if !managedchat.IsProjectionTarget(m.Destination) {
			continue
		}
		if seen[m.Destination] {
			return errors.New("duplicate managed chat bind")
		}
		seen[m.Destination] = true
		a.ChatMounts = append(a.ChatMounts, Mount{Source: m.Source, Target: m.Destination, ReadOnly: m.ReadOnly})
	}
	if reason := chatBindingReason(c, a); reason != "" {
		return errors.New(reason)
	}
	if c.chat != nil && (details.ID == "" || details.Name != c.Name || details.Labels[LabelSpace] != c.Space || details.Labels[LabelAgentbox] != c.Name) {
		return errors.New("managed chat container scope differs from its declaration")
	}
	return nil
}

func chatTargetsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/") || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/")
}

type chatRecreateKey struct{}

// WithChatRecreate is used only by an explicit recreation request, not start,
// attach, set or a policy reconcile. A changed native bind loses container-home
// data, so permission must be established before session teardown.
func WithChatRecreate(ctx context.Context) context.Context {
	return context.WithValue(ctx, chatRecreateKey{}, true)
}
func ChatRecreateAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(chatRecreateKey{}).(bool)
	return allowed
}

// ValidateChatMountTargets reserves the narrow infrastructure namespaces. Wider
// parent mounts are checked when a projection is actually being installed.
func ValidateChatMountTargets(c *Config) error {
	for _, m := range c.Mounts {
		if managedchat.IsProjectionTarget(m.Target) {
			return errors.New("mount overlaps managed chat infrastructure")
		}
	}
	return nil
}
