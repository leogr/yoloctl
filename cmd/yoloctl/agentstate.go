package main

import (
	"fmt"
	"slices"
	"sort"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// agentStateMount is the resolved source/destination for an agent-state bind
// mount, plus a flag for the "no mount at all" case (a generic-shell box, or an
// agent profile without StateContainerPath).
//
// The state dir is OWNED by the box's account and lives at a DERIVED path —
// the account's per-agent leaf, account-state/<account>/<box-agent>/
// (account.StatePath) — it is never a box field and never user-overridable.
// The account belongs to the box's space; the box grafts in exactly its own
// agent's leaf.
type agentStateMount struct {
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
	return agentStateMount{
		Source:      src,
		Destination: profile.StateContainerPath,
		Enabled:     true,
	}, nil
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

// applyAgentStateMount normalizes cfg.Mounts so it carries EXACTLY the
// managed account-state mount currently resolved from cfg.Account (or none —
// for account none, or an agent with no managed state). It removes any existing
// mount at the profile's StateContainerPath (the managed slot) and appends the
// freshly-resolved one when enabled, leaving unrelated user mounts
// untouched.
//
// Pure normalization: runtime preflight prepares the native directory while
// holding the space operation lock. Manifest-only creation never starts a VM.
//
// This keeps the container binding the path the box's account points at — never
// a stale one left by an account hand-edit. Idempotent: a second call with the
// same account is a no-op. Returns the resolved mount (for notices / auth
// bridging) and whether Mounts changed (so callers can decide to persist).
// No-op (zero mount, changed=false) when the agentbox hosts no agent.
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
	// Drop the managed slot (any mount at StateContainerPath); keep the rest.
	if sc := profile.StateContainerPath; sc != "" {
		kept := make([]agentbox.Mount, 0, len(cfg.Mounts))
		for _, m := range cfg.Mounts {
			if m.Target == sc {
				continue
			}
			kept = append(kept, m)
		}
		cfg.Mounts = kept
	}
	if mount.Enabled {
		cfg.Mounts = append(cfg.Mounts, agentbox.Mount{
			Source: mount.Source,
			Target: mount.Destination,
		})
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
	managedDest := managedStateDest(cfg)
	changed := false
	for i := range cfg.Mounts {
		m := &cfg.Mounts[i]
		if managedDest != "" && m.Target == managedDest {
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

// managedStateDest returns the in-container destination of cfg's managed
// account-state bind mount (the agent profile's StateContainerPath), or "" when
// the agentbox hosts no agent / the profile declares no managed state. This is
// the "managed slot" in cfg.Mounts that applyAgentStateMount owns. Mount
// derivation skips any Mounts entry at this target and instead takes
// the source freshly resolved from the CURRENT account, so a stale slot — left
// by an account hand-edit not yet reconciled — never leaks its old state dir
// alongside the new one. Independent of the account (uses StateContainerPath,
// not the resolved mount's destination) so it also catches a stale slot when
// the account is now none. Best-effort: any lookup failure yields "" (no slot
// skipped).
func managedStateDest(cfg *agentbox.Config) string {
	if !cfg.HasAgent() {
		return ""
	}
	profile, err := agentprofile.Lookup(cfg.Agent)
	if err != nil {
		return ""
	}
	return profile.StateContainerPath
}

// agentboxHostMounts returns the host paths that must be visible as live Lima
// mounts for cfg, each tagged with the writability yoloctl requires: the host
// workdir and the resolved account-state leaf (writable), plus every non-managed
// extra_mount source (writable unless the mount is read_only). Ephemeral
// workdirs are VM-local and excluded. Deduped (writable wins on a path
// collision) + sorted by path.
//
// The managed account-state mount is taken from the source resolved off the
// CURRENT account (managedStateMount), NOT from cfg.Mounts: any stale
// managed slot still sitting in Mounts (see managedStateDest) is skipped,
// so a since-changed account can't mount both the old and new state dir.
//
// Single source of truth for the Lima writable set (spaceWritableMounts shares
// every path writable) and the preflight mount-live checks
// (preflightAgentboxRuntime probes each per Writable).
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
	managedDest := managedStateDest(cfg)
	for _, m := range cfg.Mounts {
		if managedDest != "" && m.Target == managedDest {
			continue // managed account-state slot — resolved fresh above
		}
		add(m.Source, !m.ReadOnly)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
