// Package account stores space-owned identity manifests and provider secrets.
// Native per-agent state lives on the space's persistent VM disk. A plain
// account name is resolved only within its owning space. Mutations hold the
// stable sibling lock under spaces/<space>/accounts/.
package account

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/manifestio"
	"github.com/leogr/yoloctl/internal/strictyaml"
	"gopkg.in/yaml.v3"
)

// SchemaVersion is the account manifest schema version.
const SchemaVersion = 1

// retiredDevelopmentSchemaVersion identifies the short-lived pre-release
// manifest shape that originally carried the unused revision counter. It is
// normalized immediately to the one canonical schema version above.
const retiredDevelopmentSchemaVersion = SchemaVersion + 1

// None is the box account selection meaning "no persisted account identity".
// It pairs with agent: none (the generic shell runtime) and is the ONLY
// special selection — it is reserved and can never be an account name.
const None = "none"

// Default is an ordinary conventional name, retained for callers that choose
// it explicitly. Each space declares its own default_account; none auto-create.
const Default = "default"

// ValidateName checks an account name: standard name validation plus the
// reserved "none" sentinel (which is a selection, not an account).
func ValidateName(name string) error {
	if name == None {
		return fmt.Errorf("account name %q is reserved (it is the no-account sentinel, not a real account)", None)
	}
	return layout.ValidateName("account", name)
}

// Config is an account manifest — the identity keychain's declaration.
type Config struct {
	Space string `yaml:"-"`
	path  string // backing file path (set by Load/Save)

	SchemaVersion int               `yaml:"schema_version"`
	Name          string            `yaml:"name"`
	Secrets       map[string]string `yaml:"secrets,omitempty"`
}

// accountManifest is the strict on-disk decode shape. Revision is a
// decode-only compatibility slot for pre-release manifests written before
// the unused field was removed. It is deliberately absent from Config, so
// every subsequent Save writes the canonical current shape without
// resurrecting it.
//
// Keep this wire type narrow: strictyaml still rejects every genuinely
// unknown field, especially misspellings of the security-sensitive secrets
// map.
type accountManifest struct {
	SchemaVersion int               `yaml:"schema_version"`
	Name          string            `yaml:"name"`
	Revision      *int              `yaml:"revision,omitempty"`
	Secrets       map[string]string `yaml:"secrets,omitempty"`
}

// New returns an in-memory account with no secrets.
func New(space, name string) *Config {
	return &Config{
		SchemaVersion: SchemaVersion,
		Name:          name,
		Space:         space,
	}
}

// Path returns the backing file path (empty if never saved/loaded).
func (c *Config) Path() string { return c.path }

// SecretNames returns the secret keys, sorted. Values are never exposed here.
func (c *Config) SecretNames() []string {
	out := make([]string, 0, len(c.Secrets))
	for k := range c.Secrets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetSecret sets a secret value.
func (c *Config) SetSecret(key, value string) {
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
	c.Secrets[key] = value
}

// UnsetSecret removes a secret and reports whether it existed.
func (c *Config) UnsetSecret(key string) bool {
	if _, ok := c.Secrets[key]; !ok {
		return false
	}
	delete(c.Secrets, key)
	return true
}

// Validate checks the manifest's internal invariants.
func (c *Config) Validate() error {
	if err := ValidateName(c.Name); err != nil {
		return err
	}
	for k := range c.Secrets {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("account %q: empty secret key", c.Name)
		}
	}
	return nil
}

// ParseManifest strictly decodes one account manifest while accepting the
// retired pre-release revision field. That development shape is normalized
// to the single canonical schema version; the field never enters Config and
// is therefore discarded on the next explicit account write. All other
// unknown fields and trailing YAML documents remain hard errors.
func ParseManifest(raw []byte) (*Config, error) {
	var manifest accountManifest
	if err := strictyaml.Decode(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion == retiredDevelopmentSchemaVersion {
		manifest.SchemaVersion = SchemaVersion
	}
	return &Config{
		SchemaVersion: manifest.SchemaVersion,
		Name:          manifest.Name,
		Secrets:       manifest.Secrets,
	}, nil
}

// StatePath returns the account's native-state leaf for one agent
// (account-state/<name>/<agent>; derived, never stored).
func StatePath(name, agent string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return layout.AccountStatePath(name, agent)
}

// StateHome returns the account's portable identity home
// (account-state/<name>), the parent of its per-agent leaves.
func StateHome(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return layout.AccountStateHome(name)
}

// Load reads the account manifest for a name. Returns a wrapped
// fs.ErrNotExist when the account does not exist so callers can detect
// "missing account" and emit an actionable hint.
//
// The decode is strict: an unknown field — say a misspelled
// `secrets:` — is an error, never silently dropped into an empty keychain,
// and a trailing YAML document is rejected.
func Load(space, name string) (*Config, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	path, err := layout.AccountPath(space, name)
	if err != nil {
		return nil, err
	}
	raw, err := manifestio.Read(path)
	if err != nil {
		return nil, err // includes fs.ErrNotExist
	}
	c, err := ParseManifest(raw)
	if err != nil {
		return nil, fmt.Errorf("parse account %s (%s): %w", name, path, err)
	}
	if c.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("account %s (%s): unsupported schema_version %d (want %d)", name, path, c.SchemaVersion, SchemaVersion)
	}
	// The path is authoritative for identity; the manifest name must agree.
	if c.Name != name {
		return nil, fmt.Errorf("account %s (%s): manifest name %q does not match its location", name, path, c.Name)
	}
	c.Space = space
	c.path = path
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("account %s (%s): %w", name, path, err)
	}
	return c, nil
}

// Exists reports whether an account manifest exists for a name.
func Exists(space, name string) (bool, error) {
	if err := ValidateName(name); err != nil {
		return false, err
	}
	path, err := layout.AccountPath(space, name)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Save writes the manifest atomically (tmp+rename) at mode 0600 (it holds
// secrets), creating parent dirs.
func (c *Config) Save() error {
	return c.SaveRecorded(nil)
}

// SaveRecorded is Save with a callback that receives the exact bytes about to
// be published, before the write. Its error aborts the write.
func (c *Config) SaveRecorded(record func([]byte) error) error {
	if err := layout.ValidateExistingSpaceName(c.Space); err != nil {
		return err
	}
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := layout.AccountPath(c.Space, c.Name)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if record != nil {
		if err := record(data); err != nil {
			return err
		}
	}
	if err := manifestio.Write(path, data, 0o600); err != nil {
		return err
	}
	c.path = path
	return nil
}

// Delete removes the account manifest. Native state is removed separately by
// the caller BEFORE the manifest (account destroy is the identity-bundle
// exception to preserve-by-default; state-first ordering keeps a partial
// destroy retryable).
//
// The lock sibling (accounts/<name>.lock) is deliberately NEVER unlinked, not
// even on destroy. Lock opens the lock by path, so unlinking it would hand
// the next locker a fresh inode while an in-flight holder still holds the old
// one — two "exclusive" holders at once, and the destroy/recreate races the
// lock exists to serialize would go unserialized. A leftover .lock is an
// empty file; keeping it pins one stable inode per account name for the whole
// lifetime of the name, across any number of destroys and recreates.
func (c *Config) Delete() error {
	path, err := layout.AccountPath(c.Space, c.Name)
	if err != nil {
		return err
	}
	if err := manifestio.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every account name, sorted. Missing registry dir → empty.
func List(space string) ([]string, error) {
	dir, err := layout.AccountsDir(space)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		if ValidateName(name) != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Lock acquires the per-account exclusive flock (accounts/<name>.lock) so
// concurrent yoloctl invocations don't race on the same account. Every
// account mutation — create/get-or-create, secret set/unset, edit, destroy,
// and box-create's existence-check-then-save — runs under it. Returns a
// release function the caller must defer.
//
// flock(2) on a sibling .lock file, mirroring agentbox.LockConfig: the lock
// does NOT live at the YAML path itself (that path is rewritten atomically via
// tmp+rename, which would lose the lock). Process-scoped, which is the right
// granularity for separate yoloctl invocations; the kernel drops advisory
// locks on process exit.
//
// The lock path's INODE is stable for the lifetime of the name: nothing —
// including Delete/destroy — ever unlinks it, so every locker for a name
// contends on the same inode and exclusion can never split across a
// deleted-and-recreated lock file.
func Lock(space, name string) (release func(), err error) {
	lockPath, err := layout.AccountLockPath(space, name) // validates name
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir account lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open account lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire lock on %s: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
