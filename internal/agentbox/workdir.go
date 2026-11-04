package agentbox

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/sandbox"
)

// Workdir modes. A "host" workdir binds a host directory into
// /workdir (Lima shares it into the VM at the same canonical path);
// an "ephemeral" workdir uses VM-local storage and adds no host mount.
const (
	WorkdirModeHost      = "host"
	WorkdirModeEphemeral = "ephemeral"
)

// Workdir is an agentbox's working-directory grant. Source is the
// user-supplied (host mode) or VM-local (ephemeral mode) path; Canonical is
// the absolute, symlink-resolved Source (host mode), the authoritative
// workdir identity used for grouping, the Lima mount, the in-container path
// (ContainerPath), and the space-default-workdir match.
type Workdir struct {
	Mode      string `yaml:"mode"`
	Source    string `yaml:"source,omitempty"`
	Canonical string `yaml:"canonical,omitempty"`
}

// MountSource returns the VM-side path bound into the container. For host
// workdirs this is the symlink-resolved canonical path (which matches
// the Lima share location); for ephemeral it is the VM-local source dir.
// This is the bind SOURCE; ContainerPath is the destination.
func (w Workdir) MountSource() string {
	if w.Mode == WorkdirModeEphemeral {
		return w.Source
	}
	if w.Canonical != "" {
		return w.Canonical
	}
	return w.Source
}

// ContainerPath returns the in-container destination where the workdir is
// bound — and the agent's working directory. A host workdir mirrors its
// canonical host path UNDER /workdir (e.g. /home/me/src/repo →
// /workdir/home/me/src/repo, dropping only the leading slash), so an
// agent that keys native repository-trust state by cwd sees a distinct path per
// host repo instead of the same bare /workdir. An ephemeral workdir has no
// host identity and binds at bare /workdir.
func (w Workdir) ContainerPath() string {
	if w.Mode == WorkdirModeEphemeral {
		return sandbox.WorkdirDir
	}
	src := w.MountSource()
	if src == "" {
		return sandbox.WorkdirDir
	}
	return filepath.Join(sandbox.WorkdirDir, strings.TrimPrefix(src, "/"))
}

// IsHost reports whether this is a host-shared workdir.
func (w Workdir) IsHost() bool { return w.Mode == WorkdirModeHost }

// CanonicalizeWorkdir turns a path into an absolute, symlink-resolved
// canonical form and reports whether it currently exists on disk.
//
// When the path exists, EvalSymlinks resolves it fully. When it does not
// yet exist, the deepest existing ancestor is resolved and the missing
// suffix re-appended, so a not-yet-created workdir still gets a stable
// identity that won't shift once the directory (or a symlink above it)
// is materialized. Tilde expansion is the CLI layer's job; the input
// should already be tilde-expanded.
func CanonicalizeWorkdir(path string) (canonical string, exists bool, err error) {
	if strings.TrimSpace(path) == "" {
		return "", false, errors.New("workdir path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	var missing []string
	cur := abs
	for {
		parent := filepath.Dir(cur)
		missing = append(missing, filepath.Base(cur))
		if parent == cur {
			// Reached the filesystem root without an existing ancestor.
			return abs, false, nil
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			full := resolved
			for i := len(missing) - 1; i >= 0; i-- {
				full = filepath.Join(full, missing[i])
			}
			return full, false, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
		cur = parent
	}
}

// NewHostWorkdir canonicalizes path into a host-mode workdir grant,
// returning whether the path currently exists so the CLI can create it /
// warn. path should be absolute / tilde-expanded already.
func NewHostWorkdir(path string) (wd Workdir, exists bool, err error) {
	canonical, exists, err := CanonicalizeWorkdir(path)
	if err != nil {
		return Workdir{}, false, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Workdir{}, false, err
	}
	return Workdir{
		Mode:      WorkdirModeHost,
		Source:    filepath.Clean(abs),
		Canonical: canonical,
	}, exists, nil
}

// NewEphemeralWorkdir returns a VM-local ephemeral workdir (no host
// mount) for the named agentbox.
func NewEphemeralWorkdir(name string) Workdir {
	return Workdir{Mode: WorkdirModeEphemeral, Source: sandbox.AgentboxDirHost(name)}
}

// validateWorkdir checks the workdir block's internal consistency.
// An unset mode ("") is allowed at this layer: the CLI fills the block in at
// create time and LiveApplier.Create is the hard enforcement that a runnable
// agentbox has a workdir. Validate only rejects a malformed block.
func (c *Config) validateWorkdir() error {
	w := c.Workdir
	switch w.Mode {
	case "":
		return nil
	case WorkdirModeHost, WorkdirModeEphemeral:
		if w.Source == "" {
			return fmt.Errorf("workdir.source is required for %s mode", w.Mode)
		}
		if !filepath.IsAbs(w.Source) {
			return fmt.Errorf("workdir.source %q must be an absolute path", w.Source)
		}
		if w.Canonical != "" && !filepath.IsAbs(w.Canonical) {
			return fmt.Errorf("workdir.canonical %q must be an absolute path", w.Canonical)
		}
	default:
		return fmt.Errorf("unknown workdir.mode %q; valid: %s|%s", w.Mode, WorkdirModeHost, WorkdirModeEphemeral)
	}
	return nil
}
