// Package agentprofile holds the registered catalog of agent definitions — the
// "how to run claude / codex / future agents" tuples that
// `yoloctl box create --agent NAME`, `yoloctl agent update
// AGENT`, and the account-state bind mount all resolve against.
//
// # Storage model
//
// Built-in agents (agy, claude, codex, gemini, opencode, pi) are embedded read-only and are loaded
// directly from the running yoloctl binary. Custom agents live as YAML files
// under ~/.config/yoloctl/agents/. A legacy on-disk file whose name collides
// with a built-in is ignored; older yoloctl versions installed built-ins there,
// and allowing such a stale file to override the embedded definition would
// make upgrades keep retired images and schema.
//
// # Extension
//
// A user adds an agent with `yoloctl agent copy <source> <new-name>` and
// `yoloctl agent edit <new-name>`. The custom filename must equal its `name`.
// Built-in names are reserved, so customization always uses a distinct name.
// There is no inheritance/extends mechanism by design.
//
// This mirrors the feature and capability catalogs: embedded built-ins plus a
// flat, host-global custom registry. Instantiated boxes live under their
// space's bundle.
package agentprofile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/nativerelease"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

const (
	fileMode os.FileMode = 0o644
	dirMode  os.FileMode = 0o700
	ext                  = ".yaml"
)

// Profile describes one configured agent setup. Fields map 1:1 to
// the YAML schema users see on disk in
// ~/.config/yoloctl/agents/<name>.yaml.
type Profile struct {
	// Name is the profile identifier (claude, codex, my-claude, ...).
	// Must equal the filename without the .yaml extension; Load
	// rejects mismatch.
	Name string `yaml:"name"`
	// Image is the plain base image onto which the agent's required features
	// (including its CLI) are composed. Used as the default for box create.
	Image string `yaml:"image"`
	// Command is the binary name the agent invokes (claude, codex).
	// Used for managed agent sessions and as the pgrep target for
	// `yoloctl agent update`'s pre-flight check.
	Command string `yaml:"command"`
	// DefaultArgs are appended after Command for managed agent sessions.
	// Per-box AgentExtraArgs follow; see resolveSessionArgv in session_client.go.
	DefaultArgs []string `yaml:"default_args,omitempty"`
	// NpmPackage is what `yoloctl agent update PROFILE` installs
	// with `npm install -g <pkg>@latest`, and what `yoloctl agent versions`
	// queries on registry.npmjs.org. Empty means the agent isn't on
	// npm; NativeRelease may supply a managed native distribution instead.
	NpmPackage string `yaml:"npm_package,omitempty"`
	// NativeRelease selects a reviewed native distribution for image installation,
	// update and version checks. Mutually exclusive with NpmPackage.
	NativeRelease string `yaml:"native_release,omitempty"`
	// StateContainerPath is the in-agentbox absolute path where the
	// agent expects its state directory (e.g. /home/sandbox/.claude).
	// yoloctl bind-mounts the guest-native account/agent leaf here per the resolution
	// chain in cmd/yoloctl/agentstate.go. Empty means the profile
	// has no managed state (no bind mount added).
	StateContainerPath string `yaml:"state_container_path,omitempty"`
	// StateArchiveExcludes lists literal paths relative to the native agent
	// leaf that space export omits as disposable process state. Directories
	// exclude their subtree. No globs, parent traversal or leaf-root exclusion.
	// It never deletes source files and is unrelated to credential import.
	StateArchiveExcludes []string `yaml:"state_archive_excludes,omitempty"`
	// LaunchEnv is non-secret environment prepended to the agent command
	// as an `env K=V … <command>` prefix at every managed session launch
	// (create/start/recreate/attach). Its main purpose is to RELOCATE the agent's whole
	// related config footprint into StateContainerPath — the primary persisted
	// bind mount — so state the agent writes OUTSIDE that dir survives
	// `box recreate`. The motivating case: claude scatters its login
	// across ~/.claude/ (mounted) AND ~/.claude.json (a sibling, lost on
	// recreate); CLAUDE_CONFIG_DIR={{state_dir}} pulls the stray file into
	// the mount. It also carries non-secret in-box tuning, e.g. claude's
	// DISABLE_AUTOUPDATER=1 (self-update cannot work under the sandbox;
	// yoloctl owns updates — rationale in builtin/claude.yaml). Each value may use the {{state_dir}} token, expanded to
	// StateContainerPath. Prefix delivery (not `nerdctl -e`) is
	// deliberate: the openshell-sandbox SSH session strips -e env, but a
	// command-prefix `env` survives. NON-SECRET ONLY — the command line is
	// visible in /proc/*/cmdline. SecretInjection and native auth files
	// provide secret delivery; never put secrets here.
	LaunchEnv map[string]string `yaml:"launch_env,omitempty"`
	// Auth, when set, describes where this agent's subscription / OAuth login
	// lives in an ACCOUNT's native state. It drives the account-scoped auth
	// commands (cmd/yoloctl/account.go): `account auth import ACCOUNT AGENT`
	// copies an existing host login (ImportFiles / CredentialFile) once into
	// the account's state dir, and `status` reports whether one is present. The
	// account-state dir is bind-mounted into the box, so the captured file is
	// delivered to the agent with NO create-time bridge; interactive first-run
	// login happens inside the box (the agent's own flow). Import is a one-time
	// copy; yoloctl never synchronizes the resulting native state. Nil means
	// the agent has no managed native auth file.
	Auth *AuthSpec `yaml:"auth,omitempty"`
	// SecretInjection declares ACCOUNT secrets this agent reads from its
	// ENVIRONMENT (e.g. claude → ANTHROPIC_API_KEY). Each rule maps an account
	// secret to an env var (Env defaults to the secret name); at box create the
	// resolved value is delivered to the agent through the openshell-sandbox
	// supervisor's OPENSHELL_USER_ENVIRONMENT channel — injected into the
	// agent's ENVIRONMENT, never onto its command line (so it never lands in
	// /proc/<agent>/cmdline). Agents that require native credentials instead use
	// their own login flow or a one-time `account auth import`.
	SecretInjection []SecretInjection `yaml:"secret_injection,omitempty"`
	// RequiresFeatures lists the features an agentbox hosting this agent needs,
	// including the feature that installs the agent's own CLI (e.g. claude →
	// [claude, github]). This is the single boundary in force:
	//
	//   agent (profile) → REQUIRES FEATURES → features → REQUIRE CAPABILITIES → policy
	//
	// A profile has NO agent_feature field and NO agent-level required
	// capabilities. Capabilities reach the policy ONLY through features: the
	// agent's egress is granted because RequiresFeatures pulls in its CLI
	// feature (e.g. claude), which in turn requires the network capability (e.g.
	// network.anthropic). RequiresFeatures are unioned into the box's features
	// at create time, and the composer derives capabilities from that effective
	// feature set. Declared here so profiles are self-describing.
	RequiresFeatures []string `yaml:"requires_features,omitempty"`
	// Session, when set, lets yoloctl own the agent's conversation identity:
	// a new conversation starts with an ID yoloctl chose (AssignArgs) and a
	// later start resumes the recorded one (ResumeArgv), as the box's
	// agent_session.resume policy decides. Nil means unsupported: launches
	// stay command + default_args + agent_extra_args and nothing is recorded.
	Session *SessionSpec `yaml:"session,omitempty"`
}

// SessionSpec is the optional `session:` block on a Profile. Its templates
// accept only {{session_id}} and {{command}}.
type SessionSpec struct {
	// IDFormat validates every conversation ID yoloctl assigns, records or
	// passes on argv. Only "uuid" (canonical lowercase) is supported.
	IDFormat string `yaml:"id_format"`
	// AssignArgs follow DefaultArgs to start a NEW conversation under a fresh
	// yoloctl-chosen ID. Without them yoloctl never assigns an ID.
	AssignArgs []string `yaml:"assign_args,omitempty"`
	// ResumeArgv replaces Command to resume a recorded conversation;
	// DefaultArgs and the box's AgentExtraArgs follow. Without it the block
	// is observe-only (see Resumes).
	ResumeArgv []string `yaml:"resume_argv,omitempty"`
	// ResumeFile is an optional native-leaf presence preflight before a new
	// resume allocation: fixed directories, one single-directory wildcard and
	// a filename containing {{session_id}} followed by a literal suffix.
	ResumeFile string `yaml:"resume_file,omitempty"`
	// ReservedArgs are agent arguments that take over conversation identity
	// (resume, continue, fork, a chosen ID). A box whose agent_extra_args
	// carry one is manual: yoloctl then assigns and resumes nothing.
	ReservedArgs []string `yaml:"reserved_args,omitempty"`
	// Observe declares how this agent reports its runtime values.
	Observe *ObserveSpec `yaml:"observe,omitempty"`
	// ResumeCarry declares values a resume carries over from the last
	// observation of the same conversation (opt-in; see ResumeCarry).
	ResumeCarry *ResumeCarry `yaml:"resume_carry,omitempty"`
	// Daemons are the agent's own background servers that outlive its
	// exit, each an argv prefix ("codex app-server") whose first word is
	// the executable's base name. A managed launch is their lifetime: the
	// session holder stops every same-user process matching one before the
	// agent starts and after it exits (see session.ReapScript).
	Daemons []string `yaml:"daemons,omitempty"`
	// LegacyObserveFeature is the removed observe_feature key, decoded only
	// so a profile still carrying it fails with the rename instead of an
	// unknown-field error.
	LegacyObserveFeature string `yaml:"observe_feature,omitempty"`
}

// ResumeCarry is the optional `session.resume_carry:` block. On a resume,
// each declared value last observed for the same conversation is appended
// after agent_extra_args, so it wins over the box's own flag: Effort (only a
// value listed in its Values) and Model (only a plain model id). Nothing is
// carried to a new conversation.
type ResumeCarry struct {
	Effort *CarryArgs `yaml:"effort,omitempty"`
	Model  *CarryArgs `yaml:"model,omitempty"`
}

// CarryArgs are the arguments that carry one observed value: Args with
// {{effort}} or {{model}} replaced. Effort requires Values (the only values
// carried); Model takes none.
type CarryArgs struct {
	Args   []string `yaml:"args"`
	Values []string `yaml:"values,omitempty"`
}

// ObserveSpec is the optional `session.observe:` block.
type ObserveSpec struct {
	// Push names the feature that installs the agent's record-only reporter
	// (its status line and hooks). It must also be one of the profile's
	// RequiresFeatures, so every box of the agent gets it; an existing box
	// gets it with its next recreate.
	Push string `yaml:"push,omitempty"`
	// Selection declares whether every in-app conversation switch emits a
	// start report: reported only with evidence that it does, otherwise
	// unknown (the default). Views word their limits from it.
	Selection string `yaml:"selection,omitempty"`
	// AccountStatusLine declares a reporter that runs as the status line of
	// the account's own settings, for an agent with no system settings
	// layer; `yoloctl account observe` sets and restores it there.
	AccountStatusLine *AccountStatusLine `yaml:"account_status_line,omitempty"`
}

// AccountStatusLine is a status line reporter set in the account's native
// state: the JSON settings file whose statusLine names it, relative to
// StateContainerPath, and the in-box command it sets.
type AccountStatusLine struct {
	Settings string `yaml:"settings"`
	Command  string `yaml:"command"`
}

// Selection values of ObserveSpec.
const (
	SelectionReported = "reported"
	SelectionUnknown  = "unknown"
)

// Resumes reports whether the agent can resume a recorded conversation. An
// observe-only block (an Observe reporter, no resume_argv or assign_args)
// tracks each launch only to read its reports: every start is a new
// conversation.
func (s *SessionSpec) Resumes() bool { return s != nil && len(s.ResumeArgv) > 0 }

// SelectionReport is the declared selection evidence, unknown by default.
func (s *SessionSpec) SelectionReport() string {
	if s == nil || s.Observe == nil || s.Observe.Selection == "" {
		return SelectionUnknown
	}
	return s.Observe.Selection
}

// PushFeature is the profile's declared push reporter feature, or "".
func (p *Profile) PushFeature() string {
	if p == nil || p.Session == nil || p.Session.Observe == nil {
		return ""
	}
	return p.Session.Observe.Push
}

// SessionIDFormatUUID is the only supported SessionSpec.IDFormat.
const SessionIDFormatUUID = "uuid"

// Auth formats select credential schemas, independently of profile/agent names.
const AuthFormatClaude = "claude"
const AuthFormatCodex = "codex"

// AuthSpec is the optional `auth:` block on a Profile. It makes the
// account-scoped one-time import and local credential-status flow profile-driven rather
// than hardcoded per agent, so a user who drops a profile YAML with an auth
// block gets `yoloctl account auth …` for free.
//
// CredentialFile is resolved relative to the profile's
// StateContainerPath, so an auth block only makes sense when
// StateContainerPath is set (Validate enforces this).
type AuthSpec struct {
	// Format selects bounded native material recognition; empty retains file-presence status.
	Format string `yaml:"format,omitempty"`
	// ProfilesDir is the Claude Anthropic profile tree inside the same native leaf.
	ProfilesDir string `yaml:"profiles_dir,omitempty"`

	// CredentialFile is the agent's on-disk auth file, RELATIVE to
	// StateContainerPath (e.g. "auth.json" for codex at
	// /home/sandbox/.codex/auth.json, ".credentials.json" for claude). It is
	// the target `account auth import` writes into the account-state dir, and
	// the file `status` checks there.
	CredentialFile string `yaml:"credential_file"`
	// ImportHomeDir overrides the conventional host state directory when
	// importing without --file. It is relative to the host user's home, e.g.
	// .local/share/opencode. ImportFiles and CredentialFile resolve inside it.
	// It does not affect the native destination. Custom XDG roots use --file.
	ImportHomeDir string `yaml:"import_home_dir,omitempty"`
	// ImportFiles are the PREFERRED host login filenames (relative to the
	// host state dir, ~/ImportHomeDir or ~/<base of StateContainerPath>/) that
	// `yoloctl account auth import` tries when --file is omitted — in listed
	// order, BEFORE CredentialFile, which is the last-resort fallback
	// (deduped). First existing wins. Lets one agent expose its login under
	// more than one conventional file — e.g. claude keeps it under config.json
	// on some installs and the documented .credentials.json on others, so
	// `import_files: [config.json]` tries config.json first, then
	// CredentialFile (.credentials.json). Optional; same relative/no-escape
	// rules as CredentialFile.
	ImportFiles []string `yaml:"import_files,omitempty"`
	// ImportSecret declares an API-key fallback for `account auth import` when no
	// native credential_file login exists: read a host config file, pull one JSON
	// field's value, and store it as an ACCOUNT SECRET (which the agent reads via
	// env injection). Used for agents whose host config keeps the key in a mixed
	// JSON file — e.g. claude's ~/.claude.json `primaryApiKey` — that newer in-box
	// agents ignore directly, so the key is moved to the env path. Nil means no
	// extraction fallback (import only copies the credential_file).
	ImportSecret *ImportSecret `yaml:"import_secret,omitempty"`
}

// ImportSecret is the agent-declared rule `account auth import` uses to extract
// an API key from a host config file into an account secret. The extracted
// VALUE is owned by the account; this declares only WHERE to read it.
type ImportSecret struct {
	// File is the host config file RELATIVE to the user's home dir
	// (e.g. ".claude.json" → ~/.claude.json). Must not escape home via "..".
	File string `yaml:"file"`
	// Field is the top-level JSON string field holding the key value
	// (e.g. "primaryApiKey"). Version-coupled to the agent's config format.
	Field string `yaml:"field"`
	// Secret is the account secret key to set from Field's value
	// (e.g. "ANTHROPIC_API_KEY").
	Secret string `yaml:"secret"`
}

// SecretInjection is one agent-declared rule mapping an ACCOUNT secret to an
// environment variable injected into the agent process (delivered via the
// openshell-sandbox OPENSHELL_USER_ENVIRONMENT channel, never the command line).
// The secret VALUE is owned by the box's account; this only declares the
// secret→env-var mapping.
type SecretInjection struct {
	// Secret is the account secret KEY to read (e.g. "ANTHROPIC_API_KEY").
	Secret string `yaml:"secret"`
	// Env is the environment variable to set in the agent. Optional; defaults to
	// Secret when empty (the common case where the names match).
	Env string `yaml:"env,omitempty"`
}

// EnvName returns the environment variable the secret is injected as: Env when
// set, otherwise the secret key itself.
func (s SecretInjection) EnvName() string {
	if s.Env != "" {
		return s.Env
	}
	return s.Secret
}

// Lookup returns the profile with the given name. Built-ins resolve directly
// from the embedded catalog; custom agents resolve from the on-disk registry.
// The built-in check deliberately comes first so legacy built-in files written
// by older yoloctl versions cannot shadow the current shipped definition.
//
// Returns a wrapped fs.ErrNotExist when the file doesn't exist —
// callers can errors.Is(err, fs.ErrNotExist) to surface a friendly
// "unknown agent" error.
func Lookup(name string) (*Profile, error) {
	if err := layout.ValidateName("agent-profile", name); err != nil {
		return nil, err
	}
	if IsBuiltin(name) {
		raw, err := BuiltinContent(name)
		if err != nil {
			return nil, err
		}
		return parseProfile(raw, "embedded agent "+name, name)
	}
	path, err := layout.AgentPath(name)
	if err != nil {
		return nil, err
	}
	return openFile(path, name)
}

// Names returns every known agent name (embedded built-ins plus custom
// definitions), sorted. Legacy on-disk copies of built-ins are ignored.
func Names() ([]string, error) {
	builtins, err := BuiltinNames()
	if err != nil {
		return nil, err
	}
	dir, err := layout.AgentsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return builtins, nil
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(builtins)+len(entries))
	names := make([]string, 0, len(builtins)+len(entries))
	for _, name := range builtins {
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ext) {
			continue
		}
		n = strings.TrimSuffix(n, ext)
		if layout.ValidateName("agent-profile", n) != nil {
			continue
		}
		if _, exists := seen[n]; exists {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// All loads every installed profile keyed by name. Used by
// `yoloctl agent versions` (iterate every profile with an
// NpmPackage). The map is built fresh per call; callers wanting
// repeated lookups should use Lookup.
func All() (map[string]Profile, error) {
	names, err := Names()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Profile, len(names))
	for _, n := range names {
		p, err := Lookup(n)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", n, err)
		}
		out[n] = *p
	}
	return out, nil
}

// Save writes a CUSTOM agent profile YAML to the agents dir (atomic
// tmp+rename), overwriting any existing file at the profile's canonical path.
// It validates the profile first and refuses a built-in name (built-ins are
// yoloctl-owned and resolve directly from the embedded catalog). Used by
// `agent copy`.
func (p *Profile) Save() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if IsBuiltin(p.Name) {
		return fmt.Errorf("%q is a built-in agent; choose a different name for the custom copy", p.Name)
	}
	raw, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	path, err := layout.AgentPath(p.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}
	return atomicfile.Write(path, raw, fileMode)
}

// Delete removes a CUSTOM agent profile YAML from the agents dir. Refuses a
// built-in name because built-ins resolve directly from the embedded catalog.
// Returns fs.ErrNotExist when no file exists for name. Used by `agent destroy`.
func Delete(name string) error {
	if err := layout.ValidateName("agent-profile", name); err != nil {
		return err
	}
	if IsBuiltin(name) {
		return fmt.Errorf("%q is a built-in agent and cannot be deleted", name)
	}
	path, err := layout.AgentPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Validate checks the profile's invariants. Returns nil if valid.
//
// Rules:
//   - Name matches the standard name pattern (alnum + ._-, 63 max,
//     start alnum)
//   - Image is non-empty
//   - Command is non-empty
//   - StateContainerPath, when set, is an absolute path
//   - launch_env keys are valid env names; values' only template token
//     is {{state_dir}}, which requires state_container_path to be set
func (p *Profile) Validate() error {
	if err := layout.ValidateName("agent-profile", p.Name); err != nil {
		return err
	}
	if p.Image == "" {
		return fmt.Errorf("agent-profile %q: image is required", p.Name)
	}
	if p.Command == "" {
		return fmt.Errorf("agent-profile %q: command is required", p.Name)
	}
	if p.StateContainerPath != "" && !filepath.IsAbs(p.StateContainerPath) {
		return fmt.Errorf("agent-profile %q: state_container_path %q must be absolute", p.Name, p.StateContainerPath)
	}
	if len(p.StateArchiveExcludes) > 0 && p.StateContainerPath == "" {
		return fmt.Errorf("agent-profile %q: state_archive_excludes requires state_container_path", p.Name)
	}
	for _, name := range p.StateArchiveExcludes {
		if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00*?[]") {
			return fmt.Errorf("agent-profile %q: state_archive_excludes entry %q must be a literal relative path below the state directory", p.Name, name)
		}
	}
	if p.NativeRelease != "" {
		if p.NpmPackage != "" {
			return fmt.Errorf("agent-profile %q: npm_package and native_release are mutually exclusive", p.Name)
		}
		if _, err := nativerelease.Lookup(p.NativeRelease); err != nil {
			return err
		}
	}
	if err := p.validateLaunchEnv(); err != nil {
		return err
	}
	if err := p.validateAuth(); err != nil {
		return err
	}
	if err := validateSecretInjection(p.Name, p.SecretInjection); err != nil {
		return err
	}
	return p.validateSession()
}

// validateSession checks the optional session block: a known ID format, a
// resume argv naming {{session_id}} unless the block is observe-only, assign
// args that do, no other template token, and non-empty reserved arguments.
func (p *Profile) validateSession() error {
	s := p.Session
	if s == nil {
		return nil
	}
	if s.IDFormat != SessionIDFormatUUID {
		return fmt.Errorf("agent-profile %q: session.id_format %q is not supported (only %q)", p.Name, s.IDFormat, SessionIDFormatUUID)
	}
	if len(s.ResumeArgv) == 0 && (s.Observe == nil || len(s.AssignArgs) > 0) {
		return fmt.Errorf("agent-profile %q: session.resume_argv is required unless the block is observe-only (session.observe without assign_args)", p.Name)
	}
	check := func(field string, args []string, command bool) error {
		uses := false
		for _, a := range args {
			if a == "" {
				return fmt.Errorf("agent-profile %q: session.%s has an empty argument", p.Name, field)
			}
			uses = uses || strings.Contains(a, "{{session_id}}")
			stripped := strings.ReplaceAll(a, "{{session_id}}", "")
			if command {
				stripped = strings.ReplaceAll(stripped, "{{command}}", "")
			}
			if strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
				return fmt.Errorf("agent-profile %q: session.%s argument %q has an unknown template token", p.Name, field, a)
			}
		}
		if len(args) > 0 && !uses {
			return fmt.Errorf("agent-profile %q: session.%s must contain {{session_id}}", p.Name, field)
		}
		return nil
	}
	if err := check("resume_argv", s.ResumeArgv, true); err != nil {
		return err
	}
	if err := check("assign_args", s.AssignArgs, false); err != nil {
		return err
	}
	if s.ResumeFile != "" {
		if p.StateContainerPath == "" || len(s.AssignArgs) == 0 || !s.Resumes() {
			return fmt.Errorf("agent-profile %q: session.resume_file requires native state, assign_args and resume_argv", p.Name)
		}
		if err := validateResumeFile(s.ResumeFile); err != nil {
			return fmt.Errorf("agent-profile %q: session.resume_file: %w", p.Name, err)
		}
	}
	for _, a := range s.ReservedArgs {
		if strings.TrimSpace(a) == "" || strings.Contains(a, "{{") {
			return fmt.Errorf("agent-profile %q: session.reserved_args entry %q must be a literal argument", p.Name, a)
		}
	}
	for _, d := range s.Daemons {
		if _, err := DaemonArgv(d); err != nil {
			return fmt.Errorf("agent-profile %q: session.daemons: %w", p.Name, err)
		}
	}
	if c := s.ResumeCarry; c != nil {
		if c.Effort == nil && c.Model == nil {
			return fmt.Errorf("agent-profile %q: session.resume_carry declares nothing to carry", p.Name)
		}
		for _, carry := range []struct {
			name, placeholder string
			args              *CarryArgs
		}{{"effort", "{{effort}}", c.Effort}, {"model", "{{model}}", c.Model}} {
			e := carry.args
			if e == nil {
				continue
			}
			uses := false
			for _, a := range e.Args {
				if strings.TrimSpace(a) == "" || strings.Contains(strings.ReplaceAll(a, carry.placeholder, ""), "{{") {
					return fmt.Errorf("agent-profile %q: session.resume_carry.%s.args entry %q may only use %s", p.Name, carry.name, a, carry.placeholder)
				}
				uses = uses || strings.Contains(a, carry.placeholder)
			}
			if !uses {
				return fmt.Errorf("agent-profile %q: session.resume_carry.%s needs args with %s", p.Name, carry.name, carry.placeholder)
			}
			if carry.name == "effort" && len(e.Values) == 0 {
				return fmt.Errorf("agent-profile %q: session.resume_carry.effort needs args with {{effort}} and its values", p.Name)
			}
			if carry.name == "model" && len(e.Values) > 0 {
				return fmt.Errorf("agent-profile %q: session.resume_carry.model takes no values", p.Name)
			}
			for _, v := range e.Values {
				if v == "" || strings.Trim(v, "abcdefghijklmnopqrstuvwxyz") != "" {
					return fmt.Errorf("agent-profile %q: session.resume_carry.effort value %q must be a lowercase word", p.Name, v)
				}
			}
		}
	}
	if s.LegacyObserveFeature != "" {
		return fmt.Errorf("agent-profile %q: session.observe_feature was renamed: declare session.observe.push: %s and list %s in requires_features", p.Name, s.LegacyObserveFeature, s.LegacyObserveFeature)
	}
	if o := s.Observe; o != nil {
		if o.Push == "" || strings.TrimSpace(o.Push) != o.Push || strings.ContainsAny(o.Push, " ,/:@") {
			return fmt.Errorf("agent-profile %q: session.observe.push %q must be a feature name", p.Name, o.Push)
		}
		if !slices.Contains(p.RequiresFeatures, o.Push) {
			return fmt.Errorf("agent-profile %q: session.observe.push %s must also be listed in requires_features", p.Name, o.Push)
		}
		switch o.Selection {
		case "", SelectionReported, SelectionUnknown:
		default:
			return fmt.Errorf("agent-profile %q: session.observe.selection %q must be %s or %s", p.Name, o.Selection, SelectionReported, SelectionUnknown)
		}
		if a := o.AccountStatusLine; a != nil {
			if err := validateRelativeAuthFile(p.Name, "session.observe.account_status_line.settings", a.Settings); err != nil {
				return err
			}
			if path.Clean(a.Settings) != a.Settings {
				return fmt.Errorf("agent-profile %q: session.observe.account_status_line.settings %q must be a clean path", p.Name, a.Settings)
			}
			if p.StateContainerPath == "" {
				return fmt.Errorf("agent-profile %q: session.observe.account_status_line requires state_container_path", p.Name)
			}
			if !path.IsAbs(a.Command) || path.Clean(a.Command) != a.Command || strings.ContainsAny(a.Command, " \t\n'\"\\$`;&|<>") {
				return fmt.Errorf("agent-profile %q: session.observe.account_status_line.command %q must be a plain absolute path", p.Name, a.Command)
			}
		}
	}
	return nil
}

// daemonWord is one word of a declared daemon's argv prefix. Words are
// embedded in the holder's reaper script, so only plain names are allowed.
var daemonWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// DaemonArgv splits a session.daemons entry into its argv prefix: one to
// eight plain words separated by single spaces, the first an executable's
// base name.
func DaemonArgv(entry string) ([]string, error) {
	words := strings.Split(entry, " ")
	if len(words) > 8 {
		return nil, fmt.Errorf("daemon %q has more than 8 words", entry)
	}
	for _, w := range words {
		if !daemonWord.MatchString(w) {
			return nil, fmt.Errorf("daemon %q must be plain words separated by single spaces", entry)
		}
	}
	return words, nil
}

// SessionDaemons is the profile's declared daemons, or nil.
func (p *Profile) SessionDaemons() []string {
	if p == nil || p.Session == nil {
		return nil
	}
	return p.Session.Daemons
}

// Distribution identifies the version/update source, independently of profile names.
func (p *Profile) Distribution() string {
	if p.NativeRelease != "" {
		return "native:" + p.NativeRelease
	}
	return p.NpmPackage
}

// validateSecretInjection checks the optional secret_injection block: each rule
// needs a non-empty secret, and the env var it injects as (EnvName) must be a
// valid environment variable name. No-op when empty.
func validateSecretInjection(profileName string, rules []SecretInjection) error {
	for i, r := range rules {
		if strings.TrimSpace(r.Secret) == "" {
			return fmt.Errorf("agent-profile %q: secret_injection[%d] has an empty secret", profileName, i)
		}
		if !validEnvName(r.EnvName()) {
			return fmt.Errorf("agent-profile %q: secret_injection[%d] env %q is not a valid environment variable name", profileName, i, r.EnvName())
		}
	}
	return nil
}

// validateLaunchEnv checks the optional launch_env block. No-op
// when empty.
//
// Rules:
//   - each key is a valid POSIX-ish environment variable name
//     ([A-Za-z_][A-Za-z0-9_]*) — it's emitted bare on a command line.
//   - the only template token a value may contain is {{state_dir}};
//     any other {{…}} is a typo we reject rather than pass through
//     literally to the agent.
//   - if any value uses {{state_dir}}, state_container_path must be set
//     (it's what the token expands to — an empty expansion would yield a
//     meaningless `KEY=`).
func (p *Profile) validateLaunchEnv() error {
	usesStateDir := false
	for k, v := range p.LaunchEnv {
		if !validEnvName(k) {
			return fmt.Errorf("agent-profile %q: launch_env key %q is not a valid environment variable name", p.Name, k)
		}
		if strings.Contains(v, "{{state_dir}}") {
			usesStateDir = true
		}
		if stripped := strings.ReplaceAll(v, "{{state_dir}}", ""); strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
			return fmt.Errorf("agent-profile %q: launch_env[%s] value %q has an unknown template token (only {{state_dir}} is supported)", p.Name, k, v)
		}
	}
	if usesStateDir && p.StateContainerPath == "" {
		return fmt.Errorf("agent-profile %q: launch_env references {{state_dir}} but state_container_path is empty", p.Name)
	}
	return nil
}

// validEnvName reports whether s is a valid environment variable name:
// a non-empty string of [A-Za-z0-9_] not starting with a digit.
func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_',
			r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// validateAuth checks the optional auth block's invariants. No-op
// when Auth is nil.
//
// Rules (when Auth != nil):
//   - credential_file is required, must be relative (it joins onto
//     state_container_path), and must not escape via "..".
//   - state_container_path must be set, since credential_file is
//     resolved relative to it.
//
// validateRelativeAuthFile enforces that an auth file reference (joined
// onto host temp / account-state / home dirs) is relative and can't escape
// its base via a ".." component. label names the field for the error.
func validateRelativeAuthFile(profileName, label, file string) error {
	if file == "" {
		return fmt.Errorf("agent-profile %q: %s must not be empty", profileName, label)
	}
	if filepath.IsAbs(file) {
		return fmt.Errorf("agent-profile %q: %s %q must be relative to its base directory", profileName, label, file)
	}
	if cleaned := filepath.Clean(file); cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("agent-profile %q: %s %q must not escape its base directory via a parent (..) component", profileName, label, file)
	}
	return nil
}

func (p *Profile) validateAuth() error {
	if p.Auth == nil {
		return nil
	}
	a := p.Auth
	if a.Format != "" && a.Format != AuthFormatClaude && a.Format != AuthFormatCodex {
		return fmt.Errorf("agent-profile %q: unsupported auth.format", p.Name)
	}
	if a.ProfilesDir != "" {
		if a.Format != AuthFormatClaude {
			return fmt.Errorf("agent-profile %q: auth.profiles_dir requires Claude format", p.Name)
		}
		if err := validateRelativeAuthFile(p.Name, "auth.profiles_dir", a.ProfilesDir); err != nil {
			return err
		}
	}
	if a.ImportHomeDir != "" {
		if err := validateRelativeAuthFile(p.Name, "auth.import_home_dir", a.ImportHomeDir); err != nil {
			return err
		}
	}
	if a.CredentialFile == "" {
		return fmt.Errorf("agent-profile %q: auth.credential_file is required when auth is set", p.Name)
	}
	// credential_file and each import_files entry are joined onto the
	// account-state dir (status), or the user's home (account auth import), so
	// each must be relative and must not escape its base via a ".." component.
	if err := validateRelativeAuthFile(p.Name, "auth.credential_file", a.CredentialFile); err != nil {
		return err
	}
	for _, f := range a.ImportFiles {
		if err := validateRelativeAuthFile(p.Name, "auth.import_files entry", f); err != nil {
			return err
		}
	}
	if p.StateContainerPath == "" {
		return fmt.Errorf("agent-profile %q: auth requires state_container_path (credential_file is resolved relative to it)", p.Name)
	}
	if a.ImportSecret != nil {
		is := a.ImportSecret
		if err := validateRelativeAuthFile(p.Name, "auth.import_secret.file", is.File); err != nil {
			return err
		}
		if strings.TrimSpace(is.Field) == "" {
			return fmt.Errorf("agent-profile %q: auth.import_secret.field is required", p.Name)
		}
		if strings.TrimSpace(is.Secret) == "" {
			return fmt.Errorf("agent-profile %q: auth.import_secret.secret is required", p.Name)
		}
	}
	return nil
}

// openFile reads + parses + validates one profile YAML at the given
// path. Filename-vs-name check is enforced here so a mistakenly-
// renamed file surfaces a clear error rather than silently using a
// stale name.
func openFile(path, expectedName string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseProfile(raw, path, expectedName)
}

// parseProfile parses and validates one profile from either an embedded
// built-in or an on-disk custom file. source is included in diagnostics.
//
// The decode is strict: an agent definition routes credentials
// (secret_injection, auth, launch_env), so a misspelled field must fail
// closed rather than silently vanish and leave the agent running with
// different injection/auth behavior than the definition's author declared.
// Trailing YAML documents are rejected for the same reason.
func parseProfile(raw []byte, source, expectedName string) (*Profile, error) {
	var p Profile
	if err := strictyaml.Decode(raw, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("%s: `name` is required", source)
	}
	if p.Name != expectedName {
		return nil, fmt.Errorf("%s: profile name %q must equal filename %q (rename the file or update the YAML)", source, p.Name, expectedName)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &p, nil
}
