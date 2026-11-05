package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// agentStateMount is the primary selected account/agent leaf view and any
// additional views of subdirectories in that same leaf. Sources are derived,
// never supplied by the box manifest. Agentless/account-none boxes get no views.
type agentStateMount struct {
	Additional []agentStateMount
	Relative   string
	// Source is the VM-native leaf on the space's persistent account disk.
	// Only this account/agent leaf is bound into the box.
	Source string
	// Destination is the in-agentbox absolute path. Comes verbatim from
	// agentprofile.Profile.StateContainerPath.
	Destination string
	// Enabled is true when a bind mount should be added to the agentbox's
	// Mounts. False for a generic-shell box (account none) or a profile
	// without StateContainerPath.
	Enabled bool
}

// resolveAgentStateMount picks the agent-state bind-mount source for the
// supplied agentbox from its ACCOUNT. The leaf is derived from the account
// name + the box's agent — it is never stored on the box and never
// user-overridable.
//
//   - No real agent / account none / a profile with no StateContainerPath →
//     disabled (no mount).
//   - Otherwise: Source = account.StatePath(cfg.Account, cfg.Agent),
//     Destination = the profile's StateContainerPath.
//
// Pure path derivation. Runtime preflight verifies the disk and creates the
// leaf through the privileged VM helper; no host filesystem stores this state.
func resolveAgentStateMount(profile *agentprofile.Profile, cfg *agentbox.Config) (agentStateMount, error) {
	if !cfg.HasRealAccount() || profile.StateContainerPath == "" {
		// Generic-shell box (account none) or an agent profile without
		// persistent state. Nothing to mount.
		return agentStateMount{}, nil
	}
	src, err := account.StatePath(cfg.Account, cfg.Agent)
	if err != nil {
		return agentStateMount{}, err
	}
	state := agentStateMount{Source: src, Destination: profile.StateContainerPath, Enabled: true}
	for _, extra := range profile.StateMounts {
		state.Additional = append(state.Additional, agentStateMount{Source: filepath.Join(src, extra.Source), Destination: extra.Destination, Relative: extra.Source, Enabled: true})
	}
	return state, nil
}

// writeAgentStateBlock renders the dedicated "Account state:" section of
// `box show`. Two shapes:
//   - bind enabled  → Source / Mounted at lines (the per-agent leaf)
//   - disabled      → suppressed entirely (the box hosts no agent / its account
//     is none / the profile declares no state path)
func writeAgentStateBlock(w interface {
	Write([]byte) (int, error)
}, state agentStateMount) {
	if !state.Enabled {
		return
	}
	_, _ = fmt.Fprintln(w, "Account state:")
	_, _ = fmt.Fprintf(w, "  Source:  %s\n", state.Source)
	_, _ = fmt.Fprintf(w, "  Mounted: %s\n", state.Destination)
	for _, extra := range state.Additional {
		_, _ = fmt.Fprintf(w, "  Source:  %s\n  Mounted: %s\n", extra.Source, extra.Destination)
	}
}

// resolveAgentStateForShow is the best-effort wrapper used by `box show`
// to surface "where does this box's account state land?". Returns a zero-value
// mount (Enabled=false) when anything in the chain fails (YAML missing, profile
// unknown, account invalid). The show writer degrades to nothing rather
// than erroring.
func resolveAgentStateForShow(spaceName, name string) agentStateMount {
	cfg, err := agentbox.Load(spaceName, name)
	if err != nil || !cfg.HasAgent() {
		return agentStateMount{}
	}
	profile, err := agentprofile.Lookup(cfg.Agent)
	if err != nil {
		return agentStateMount{}
	}
	mount, err := resolveAgentStateMount(profile, cfg)
	if err != nil {
		return agentStateMount{}
	}
	return mount
}

// applyAgentStateMount replaces all profile-owned mount destinations with
// sources derived from the current account, removing stale account views.
// Unrelated user mounts remain intact. Native preparation happens later under
// the operation lock; this normalization has no filesystem side effects.
func applyAgentStateMount(cfg *agentbox.Config) (agentStateMount, bool, error) {
	if !cfg.HasAgent() {
		return agentStateMount{}, false, nil
	}
	profile, err := lookupAgentProfile(cfg.Agent)
	if err != nil {
		return agentStateMount{}, false, err
	}
	mount, err := resolveAgentStateMount(profile, cfg)
	if err != nil {
		return agentStateMount{}, false, err
	}
	before := append([]agentbox.Mount(nil), cfg.Mounts...)
	// Replace every managed destination, including stale views from an old account.
	destinations := profileStateDestinations(profile)
	if profile.StateContainerPath != "" {
		kept := make([]agentbox.Mount, 0, len(cfg.Mounts))
		for _, m := range cfg.Mounts {
			if slices.Contains(destinations, m.Target) {
				continue
			}
			kept = append(kept, m)
		}
		cfg.Mounts = kept
	}
	if mount.Enabled {
		for _, state := range append([]agentStateMount{mount}, mount.Additional...) {
			cfg.Mounts = append(cfg.Mounts, agentbox.Mount{Source: state.Source, Target: state.Destination})
		}
	}
	return mount, !slices.Equal(before, cfg.Mounts), nil
}

// canonicalizeUserMountSources resolves every ordinary host bind source to
// the same absolute, symlink-resolved identity used for workdirs. The managed
// account-state slot is excluded: its source is derived internally by
// applyAgentStateMount, never accepted from the user.
//
// Keeping the canonical path in cfg is security-significant. Grant checks and
// the account-state-root boundary must authorize the object the bind will
// actually expose, not a harmless-looking symlink name supplied in --mount or
// a hand-edited manifest. Persisting the resolved path also prevents a later
// symlink retarget from changing what an already-declared box mounts.
func canonicalizeUserMountSources(cfg *agentbox.Config) (bool, error) {
	changed := false
	for i := range cfg.Mounts {
		m := &cfg.Mounts[i]
		if isManagedStateDest(cfg, m.Target) {
			continue
		}
		canonical, _, err := agentbox.CanonicalizeWorkdir(m.Source)
		if err != nil {
			return false, fmt.Errorf("canonicalize mount source %s: %w", m.Source, err)
		}
		if canonical != m.Source {
			m.Source = canonical
			changed = true
		}
	}
	return changed, nil
}

// hostMount is a host path an agentbox needs Lima to share into its space VM,
// tagged with whether yoloctl requires it to be host-WRITABLE. The workdir and
// the account-state dir always need write (the agent persists into them); a
// read-only extra_mount only needs to be readable on the host. Lima itself
// always shares each path writable — it can't scope a share per-container, so
// the per-mount read-only flag is enforced at the container bind (nerdctl `-v
// SRC:DST:ro` in internal/agentbox/live.go), not at the Lima share. So Writable
// governs ONLY the preflight liveness probe (test -w vs test -r), not how Lima
// mounts the path; a read-only extra_mount whose host source isn't writable is
// still a valid live mount.
type hostMount struct {
	Path     string
	Writable bool
}

// managedStateDestinations identifies every profile-owned slot independently of
// the selected account, so account-none also removes stale views. Unknown
// profiles have no exemptions; ordinary native-root validation still applies.
func managedStateDestinations(cfg *agentbox.Config) []string {
	if !cfg.HasAgent() {
		return nil
	}
	profile, err := agentprofile.Lookup(cfg.Agent)
	if err != nil || profile.StateContainerPath == "" {
		return nil
	}
	return profileStateDestinations(profile)
}

func profileStateDestinations(profile *agentprofile.Profile) []string {
	if profile.StateContainerPath == "" {
		return nil
	}
	out := []string{profile.StateContainerPath}
	for _, mount := range profile.StateMounts {
		out = append(out, mount.Destination)
	}
	return out
}

func isManagedStateDest(cfg *agentbox.Config, target string) bool {
	return slices.Contains(managedStateDestinations(cfg), target)
}

// agentboxHostMounts returns only host workdir and ordinary extra-mount sources.
// All native state views are excluded: they are prepared on the independent
// account disk and never become host shares. Results are deduplicated/sorted.
func agentboxHostMounts(cfg *agentbox.Config) []hostMount {
	idx := map[string]int{}
	var out []hostMount
	add := func(path string, writable bool) {
		if path == "" {
			return
		}
		if i, ok := idx[path]; ok {
			if writable {
				out[i].Writable = true // a writable requirement subsumes a read-only one
			}
			return
		}
		idx[path] = len(out)
		out = append(out, hostMount{Path: path, Writable: writable})
	}
	if cfg.Workdir.IsHost() {
		add(cfg.Workdir.MountSource(), true)
	}
	for _, m := range cfg.Mounts {
		if isManagedStateDest(cfg, m.Target) {
			continue // managed account-state slot — resolved fresh above
		}
		add(m.Source, !m.ReadOnly)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
