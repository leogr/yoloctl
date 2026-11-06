package spacearchive

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
)

// Export transforms only archive metadata, never the source account. Import
// preserves clean outside absolute symlinks without following their targets.
type nativeExportPolicy struct {
	profiles map[string]*agentprofile.Profile
}

func (p *nativeExportPolicy) profile(agent string) *agentprofile.Profile {
	if profile, ok := p.profiles[agent]; ok {
		return profile
	}
	if p.profiles == nil {
		p.profiles = make(map[string]*agentprofile.Profile)
	}
	// A retired/missing custom profile must not prevent backing up ordinary
	// files. Without a valid profile its container-path links stay verbatim.
	profile, _ := agentprofile.Lookup(agent)
	p.profiles[agent] = profile
	return profile
}

func (p *nativeExportPolicy) skip(rel string, info fs.FileInfo) bool {
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) < 3 {
		return false // Account/agent roots must still be validated as directories.
	}
	// A socket is a process endpoint, not persistent account data. Do not try
	// to serialize it or connect to it, even if its name looks like a data file.
	if info.Mode()&fs.ModeSocket != 0 {
		return true
	}
	profile := p.profile(parts[1])
	return profile != nil && slices.Contains(profile.StateArchiveExcludes, parts[2])
}

func (p *nativeExportPolicy) relativeLink(dir, rel, target string) (string, error) {
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) < 3 {
		return "", errors.New("native account/agent roots must be directories")
	}
	// Cleaning a target with '..' through an intermediate symlink can change
	// its meaning. Only translate canonical absolute paths, without following
	// any part of the target in the controller's filesystem namespace.
	if target != path.Clean(target) || strings.ContainsAny(target, "\\\x00") {
		return "", errors.New("absolute native link target must be a clean path")
	}
	alias, err := layout.AccountStatePath(parts[0], parts[1])
	if err != nil {
		return "", err
	}
	roots := []string{filepath.Join(dir, parts[0], parts[1]), alias}
	if profile := p.profile(parts[1]); profile != nil && profile.StateContainerPath != "" {
		roots = append(roots, path.Clean(profile.StateContainerPath))
	}
	for _, root := range roots {
		var suffix string
		if target != root {
			var ok bool
			suffix, ok = strings.CutPrefix(target, strings.TrimSuffix(root, "/")+"/")
			if !ok {
				continue
			}
		}
		// Work relative to the leaf, not a host/VM/container absolute path. A
		// dangling in-leaf target is safe to preserve; never read its contents.
		if suffix == "" {
			suffix = "."
		}
		return filepath.Rel(path.Dir(parts[2]), suffix)
	}
	return target, nil
}
