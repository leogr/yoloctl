// Package layout is the single owner of yoloctl's host-side filesystem
// contract. Native spaceenv paths take precedence; an absolute, clean
// YOLOCTL_HOME selects config and its cache child for either build. Otherwise
// XDG/OS roots use yoloctl for every build; a separate development home is
// opt-in through YOLOCTL_HOME. All host
// path construction belongs here.
//
// # Scope-first layout
//
// Global, reusable definitions stay flat at the config root; everything
// that belongs to ONE space lives under that space's bundle:
//
//	~/.config/yoloctl/
//	  context.yaml                       # ContextPath
//	  tui.yaml                           # TUIPath
//	  agents/<name>.yaml                 # AgentPath  (conceptual; flat)
//	  features/<name>.yaml               # FeaturesDir       (conceptual; flat)
//	  capabilities/<name>.yaml           # CapabilityPath    (conceptual; flat)
//	  spaces/
//	    <space>/                         # SpaceBundleDir
//	      space.yaml                     # SpaceConfigPath
//	      accounts/<name>.yaml           # AccountPath (0600)
//	      accounts/<name>.lock           # AccountLockPath
//	      boxes/
//	        <name>.yaml                  # BoxPath
//	        <name>.lock                  # BoxLockPath
//	  locks/spaces/<space>.lock          # SpaceLockPath (outlives the bundle)
//	  lima/                              # LimaHome (VMs and separate account disks)
//
// Native state lives inside the owning VM at /var/lib/yoloctl/account-state,
// backed by its ext4 data disk, never by a shared host directory.
//
// # Name validation at the boundary
//
// ValidateName lives here so layout stays a leaf package with no yoloctl
// imports. Every helper that interpolates a
// caller-supplied space, box, account, or catalog name into a
// path validates it first, so a name can never escape its directory via ".."
// or smuggle a shell metacharacter into a VM path.
package layout

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

const ext = ".yaml"

// Names use 1-63 ASCII characters, alnum + ._-, starting with alnum.
// Excluding shell metacharacters and `/` keeps validated names safe in shell
// commands and VM paths. Boxes require at least two characters to match
// nerdctl's Docker-compatible name rule, retaining yoloctl's 63-character cap.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
var agentboxNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,62}$`)

// reservedAgentboxNames are agentbox names yoloctl claims for its own VM
// infrastructure. "sessions" would make a box's sandbox-socket dir
// /run/yoloctl/sessions — the session layer's attach-socket directory
// (internal/session.SocketDir), which must never be bind-mounted into a
// container.
var reservedAgentboxNames = map[string]bool{
	"sessions":   true,
	"controller": true,
	"operators":  true,
	"operator":   true,
}

// ValidateName returns nil if name is a valid identifier for the given
// kind (e.g. "agentbox", "space", "account", "agent-profile").
// Use this at every entry that interpolates a name into a VM path, a shell
// command, or a label. Boxes share the runtime's two-character minimum;
// other kinds retain their one-character minimum.
func ValidateName(kind, name string) error {
	pattern := namePattern
	if kind == "agentbox" {
		pattern = agentboxNamePattern
	}
	if !pattern.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must match %s", kind, name, pattern)
	}
	if kind == "agentbox" && reservedAgentboxNames[name] {
		return fmt.Errorf("agentbox name %q is reserved for yoloctl session infrastructure", name)
	}
	return nil
}

// ConfigRoot returns the selected host home, after native catalog precedence.
func ConfigRoot() (string, error) {
	if c := spaceenv.Current(); c != nil {
		return spaceenv.CatalogDirectory(), nil
	}
	if home, err := selectedHome(); home != "" || err != nil {
		return home, err
	}
	return xdgRoot("XDG_CONFIG_HOME", os.UserConfigDir)
}

// CacheRoot returns the selected cache, after native cache precedence.
func CacheRoot() (string, error) {
	if spaceenv.Native() {
		return "/var/cache/yoloctl", nil
	}
	if home, err := selectedHome(); home != "" || err != nil {
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "cache"), nil
	}
	return xdgRoot("XDG_CACHE_HOME", os.UserCacheDir)
}

func selectedHome() (string, error) {
	home := os.Getenv("YOLOCTL_HOME")
	if home != "" && (!filepath.IsAbs(home) || filepath.Clean(home) != home) {
		return "", fmt.Errorf("YOLOCTL_HOME must be an absolute, clean path")
	}
	return home, nil
}

// xdgRoot honors absolute XDG overrides on every OS. Relative XDG values
// fall back to the OS base, as required by the XDG specification.
func xdgRoot(envVar string, osDir func() (string, error)) (string, error) {
	if dir := os.Getenv(envVar); filepath.IsAbs(dir) {
		return filepath.Join(dir, buildinfo.HomeLeaf()), nil
	}
	dir, err := osDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, buildinfo.HomeLeaf()), nil
}

func underConfig(parts ...string) (string, error) {
	root, err := ConfigRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

func underCache(parts ...string) (string, error) {
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{root}, parts...)...), nil
}

// --- Single-document config files -----------------------------------------

// ContextPath returns the current-context pin file (context.yaml).
func ContextPath() (string, error) { return underConfig("context.yaml") }

// TUIPath returns the TUI config (tui.yaml).
func TUIPath() (string, error) { return underConfig("tui.yaml") }

// --- Conceptual (flat, not space-scoped) registries -----------------------

// AgentsDir returns the agents registry dir.
func AgentsDir() (string, error) { return underConfig("agents") }

// AgentPath returns the YAML path for a named agent profile.
func AgentPath(name string) (string, error) {
	if err := ValidateName("agent-profile", name); err != nil {
		return "", err
	}
	return underConfig("agents", name+ext)
}

// FeaturesDir returns the user-feature registry dir.
func FeaturesDir() (string, error) { return underConfig("features") }

// CapabilitiesDir returns the user-capability registry dir (positive-grant
// presets that compile to OpenShell policy). Sibling of the feature and
// agent-profile registries.
func CapabilitiesDir() (string, error) { return underConfig("capabilities") }

// --- Lima (external-tool storage; path unchanged) --------------------------

// LimaHome returns yoloctl's LIMA_HOME. The location is intentionally
// unchanged by the scope-first reorg — Lima runtime storage stays here.
func LimaHome() (string, error) { return underConfig("lima") }

// FirecrackerHome contains private per-VM runtime metadata, never declarations.
func FirecrackerHome() (string, error) { return underConfig("firecracker") }

// VMImagesDir contains immutable, in-use boot media, not disposable cache data.
func VMImagesDir(backend string) (string, error) {
	if err := ValidateName("VM backend", backend); err != nil {
		return "", err
	}
	return underConfig("vm-images", backend)
}

// VMImageBuildDir holds reusable pinned downloads, kernels and preparation logs.
// Boot media remains in VMImagesDir so deleting a build cache cannot break VMs.
func VMImageBuildDir(backend string) (string, error) {
	if err := ValidateName("VM backend", backend); err != nil {
		return "", err
	}
	root, err := CacheRoot()
	return filepath.Join(root, "vm-build", backend), err
}
func FirecrackerInstanceDir(space string) (string, error) {
	if err := ValidateName("space", space); err != nil {
		return "", err
	}
	return underConfig("firecracker", "instances", space)
}
func FirecrackerDiskDir(id string) (string, error) {
	if _, err := AccountDiskDir(id); err != nil {
		return "", err
	}
	return underConfig("firecracker", "disks", id)
}

// --- Space bundles (scope-first) ------------------------------------------

// SpacesDir returns the parent dir holding every space bundle.
func SpacesDir() (string, error) { return underConfig("spaces") }

// SpaceBundleDir returns a space's bundle dir (spaces/<space>), the root
// of everything scoped to that one space.
func SpaceBundleDir(space string) (string, error) {
	if err := ValidateName("space", space); err != nil {
		return "", err
	}
	if c := spaceenv.Current(); c != nil {
		if err := spaceenv.RequireSpace(space); err != nil {
			return "", err
		}
		return c.ConfigDir, nil
	}
	return underConfig("spaces", space)
}

// SpaceConfigPath returns a space's declaration (spaces/<space>/space.yaml).
func SpaceConfigPath(space string) (string, error) {
	dir, err := SpaceBundleDir(space)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "space"+ext), nil
}

// BoxesDir returns a space's agentbox registry dir
// (spaces/<space>/boxes).
func BoxesDir(space string) (string, error) {
	dir, err := SpaceBundleDir(space)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "boxes"), nil
}

// BoxPath returns the YAML path for a named agentbox in a space
// (spaces/<space>/boxes/<name>.yaml). Validates both names.
func BoxPath(space, name string) (string, error) {
	dir, err := BoxesDir(space)
	if err != nil {
		return "", err
	}
	if err := ValidateName("agentbox", name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+ext), nil
}

// BoxSessionPath returns the controller-owned conversation record of a box
// (spaces/<space>/state/boxes/<name>.session.yaml natively,
// <config>/state/boxes/...). It is outside boxes/ so declaration listings,
// export and import never read it as a box.
func BoxSessionPath(space, name string) (string, error) {
	dir, err := SpaceBundleDir(space)
	if err != nil {
		return "", err
	}
	if err := ValidateName("agentbox", name); err != nil {
		return "", err
	}
	return filepath.Join(dir, "state", "boxes", name+".session"+ext), nil
}

// BoxLockPath returns the flock sibling of an agentbox YAML
// (spaces/<space>/boxes/<name>.lock). The lock is a separate file so
// the YAML can be rewritten atomically (tmp+rename) without losing it.
func BoxLockPath(space, name string) (string, error) {
	dir, err := BoxesDir(space)
	if err != nil {
		return "", err
	}
	if err := ValidateName("agentbox", name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".lock"), nil
}

// --- Space-owned accounts -------------------------------------------------

func AccountsDir(space string) (string, error) {
	dir, err := SpaceBundleDir(space)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "accounts"), nil
}

func AccountPath(space, name string) (string, error) {
	dir, err := AccountsDir(space)
	if err != nil {
		return "", err
	}
	if err := ValidateName("account", name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+ext), nil
}

func AccountLockPath(space, name string) (string, error) {
	dir, err := AccountsDir(space)
	if err != nil {
		return "", err
	}
	if err := ValidateName("account", name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".lock"), nil
}

// Guest account paths are identical in different VMs; they are never host I/O paths.
func AccountStateRoot() (string, error) { return "/var/lib/yoloctl/account-state", nil }
func AccountStateHome(name string) (string, error) {
	if err := ValidateName("account", name); err != nil {
		return "", err
	}
	root, _ := AccountStateRoot()
	return filepath.Join(root, name), nil
}
func AccountStatePath(name, agent string) (string, error) {
	home, err := AccountStateHome(name)
	if err != nil {
		return "", err
	}
	if err := ValidateName("agent", agent); err != nil {
		return "", err
	}
	return filepath.Join(home, agent), nil
}

func SpaceLockPath(space string) (string, error) {
	if err := ValidateName("space", space); err != nil {
		return "", err
	}
	if spaceenv.Native() {
		if err := spaceenv.RequireSpace(space); err != nil {
			return "", err
		}
		return filepath.Join(spaceenv.RunRoot, "operation.lock"), nil
	}
	return underConfig("locks", "spaces", space+".lock")
}

// AccountDiskName binds a persistent disk to an unguessable declaration identity.
// Lima prefixes filesystem labels with "lima-". ext4 labels have 16 bytes,
// leaving 11 for the disk name. Allocation is exclusive and precedes saving a
// declaration; a name collision can never adopt another disk. The full random
// identity remains in space.yaml and the guest ownership marker.
func AccountDiskName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "y" + strings.ToLower(base32.StdEncoding.EncodeToString(sum[:])[:10])
}
func AccountDiskMount(id string) string { return "/mnt/lima-" + AccountDiskName(id) }

func AccountDiskDir(id string) (string, error) {
	if b, err := hex.DecodeString(id); err != nil || len(b) != 16 {
		return "", fmt.Errorf("invalid account disk identity")
	}
	return underConfig("lima", "_disks", AccountDiskName(id))
}

// --- Caches ($XDG_CACHE_HOME/yoloctl) -------------------------------------

// NpmLatestCachePath returns the registry-latest cache file for an npm
// package (npm-latest/<escaped>.json). pkg is URL-path-escaped so a scoped
// package name (@scope/pkg) stays a single path component.
func NpmLatestCachePath(pkg string) (string, error) {
	return underCache("npm-latest", url.PathEscape(pkg)+".json")
}

// OCIFeatureCacheDir holds advisory completion metadata, never declarations.
func OCIFeatureCacheDir() (string, error) { return underCache("oci-features") }

// InstallRoot is independent of the selected development/release state home.
// Old immutable bundles may be referenced by VMs in any selected home.
func InstallRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "yoloctl"), nil
}
func DefaultBinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}
