package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// Dynamic completion uses one command-specific eligibility table: one
// registration pass wires flag completers and per-command positional
// completers. Cobra's hidden `__complete` command serves both shells (source
// `yoloctl completion bash|zsh|fish`) and the TUI palette (which shells out to
// `yoloctl __complete ...` — internal/tui/yoloctlcli), so the two surfaces
// never drift.
//
// Completion is context-aware: each command completes exactly the eligible set
// for THAT command, not just "any entity of this type". A `create` positional
// names a NEW entity, so it suggests nothing (existing names are the wrong
// answer); `space stop` suggests only running spaces; `space start` only
// not-running ones; and so on.
//
// Completion is local-first. The declaration-derived completers read only the
// on-disk registries (~/.config/yoloctl/...). Two runtime-aware sources back
// the run-state distinctions, each a single subprocess bounded by a short
// timeout and degrading to declaration-derived eligibility when the runtime is
// unavailable: `limactl list` for space run-state (`runningSpaceSet`), and the
// same `nerdctl ps` query `box ls` uses for box run-state
// (`runningBoxSet`, via container.ListQuiet — stderr discarded so a stopped
// space doesn't spew on every keystroke). So `space stop` / `box stop` offer
// only running entities, `space start` / `box start` only stopped ones, and so
// on.
//
// Host PATH values are completed Go-side as well (completion_path.go) instead
// of via shell directives: directives can't confine suggestions to a space's
// multi-root mount grants, and the palette consumes candidate lines only. The
// same context-awareness applies — --workdir means three different things (a
// NEW grant on `space create`; a grant-constrained host dir on the
// box-creating commands; a declared-workdir filter on the listings), so
// registerFlagCompleters layers per-command overrides over its uniform walk.

// registerCompletions wires flag completers and per-command positional
// completers onto the command tree. Aliases share their canonical command's
// completer automatically (they are the same *cobra.Command).
func registerCompletions(root *cobra.Command) {
	registerFlagCompleters(root)

	// Per-command positional completers. A command absent from this table
	// gets no positional completion (a deliberate, safe default — better than
	// a wrong guess). Each entry's first element selects the command by path;
	// aliases resolve to the same command, so `space`/`sp` are both covered
	// by the canonical-name path.
	table := []struct {
		path []string
		fn   cobra.CompletionFunc
	}{
		// space — runtime-aware where the operation needs a running VM.
		{[]string{"space", "create"}, completeNoArgs},                       // a NEW name
		{[]string{"space", "start"}, firstArgOnly(completeStartableSpaces)}, // not running
		{[]string{"space", "stop"}, firstArgOnly(completeRunningSpaces)},    // running
		{[]string{"space", "ssh"}, firstArgOnly(completeRunningSpaces)},     // running (shell alias shares the cmd)
		{[]string{"space", "recreate"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "destroy"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "show"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "set"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "edit"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "view"}, firstArgOnly(completeRunningSpaces)},    // host-only live view of a running space
		{[]string{"space", "storage"}, firstArgOnly(completeRunningSpaces)}, // host-only read of a running space
		{[]string{"space", "use"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "logs"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "mount", "list"}, firstArgOnly(completeSpaces)},
		{[]string{"space", "mount", "add"}, completeSpaceMountAddArgs},
		{[]string{"space", "mount", "remove"}, completeSpaceMountRemoveArgs},
		// Backups: export reads an existing SPACE into a new archive path;
		// import restores an existing archive into a NEW space name.
		{[]string{"space", "export"}, argSequence(completeSpaces, completeHostFiles)},
		{[]string{"space", "import"}, argSequence(completeNoArgs, completeHostFiles)},
		{[]string{"space", "operation"}, firstArgOnly(completeSpaces)},

		// box — run-state-aware via the same nerdctl ps query `box ls` uses.
		// create / run name a NEW box (run auto-derives or takes --name; its
		// positional is the agent's `-- COMMAND`, not a box name); stop/exec/
		// restart need a live box; start materializes a not-running one; attach
		// auto-revives any declared box; the config / lifecycle verbs act on any
		// declared box. Root attach has its own command instance and entry below.
		{[]string{"box", "create"}, completeNoArgs},                      // a NEW name
		{[]string{"box", "copy"}, copyArgs(completeCopySourceBoxes)},     // SOURCE = declared; NEWNAME = a NEW name
		{[]string{"box", "stop"}, firstArgOnly(completeRunningBoxes)},    // running
		{[]string{"box", "restart"}, firstArgOnly(completeRunningBoxes)}, // running (stop+start)
		{[]string{"box", "exec"}, firstArgOnly(completeRunningBoxes)},    // running (no auto-start)
		{[]string{"box", "start"}, firstArgOnly(completeStartableBoxes)}, // not running
		{[]string{"box", "attach"}, firstArgOnly(completeAgentboxes)},    // ensure-semantics revive any
		{[]string{"box", "logs"}, firstArgOnly(completeAgentboxes)},
		{[]string{"box", "show"}, firstArgOnly(completeKnownBoxes)},
		{[]string{"box", "inspect"}, firstArgOnly(completeKnownBoxes)},
		{[]string{"box", "destroy"}, firstArgOnly(completeKnownBoxes)},
		{[]string{"box", "recreate"}, firstArgOnly(completeAgentboxes)},
		{[]string{"box", "reconcile"}, firstArgOnly(completeReconcileBoxes)},
		{[]string{"box", "diff"}, firstArgOnly(completeAgentboxes)},
		{[]string{"box", "edit"}, firstArgOnly(completeAgentboxes)},
		{[]string{"box", "set"}, firstArgOnly(completeAgentboxes)},
		{[]string{"box", "session"}, firstArgOnly(completeAgentboxes)}, // a declared box's conversation record

		// Root attach mirrors box attach completion.
		{[]string{"attach"}, firstArgOnly(completeAgentboxes)},

		// agent
		{[]string{"agent", "update"}, firstArgOnly(completeAgentProfiles)},
		// agent catalog: show = any (built-in+custom); copy SRC
		// = any, NEWNAME = nothing (a new name); edit/destroy = custom only.
		{[]string{"agent", "show"}, firstArgOnly(completeAgentProfiles)},
		{[]string{"agent", "copy"}, copyArgs(completeAgentProfiles)},
		{[]string{"agent", "edit"}, firstArgOnly(completeCustomAgents)},
		{[]string{"agent", "destroy"}, firstArgOnly(completeCustomAgents)},

		// feature catalog
		{[]string{"feature", "show"}, firstArgOnly(completeFeatures)},
		{[]string{"feature", "copy"}, copyArgs(completeFeatures)},
		{[]string{"feature", "edit"}, firstArgOnly(completeCustomFeatures)},
		{[]string{"feature", "destroy"}, firstArgOnly(completeCustomFeatures)},

		// capability catalog
		{[]string{"capability", "show"}, firstArgOnly(completeCapabilities)},
		{[]string{"capability", "copy"}, copyArgs(completeCapabilities)},
		{[]string{"capability", "edit"}, firstArgOnly(completeCustomCapabilities)},
		{[]string{"capability", "destroy"}, firstArgOnly(completeCustomCapabilities)},

		// image — PROFILE or catalog REF for refresh; catalog REF for pull.
		{[]string{"image", "pull"}, firstArgOnly(completeCatalogRefs)},

		// port — AGENTBOX is the first positional (declared boxes).
		{[]string{"port", "forward"}, firstArgOnly(completeAgentboxes)},
		{[]string{"port", "unforward"}, firstArgOnly(completeAgentboxes)},
		{[]string{"port", "list"}, firstArgOnly(completeAgentboxes)},

		// Chat enrollment names a declared box; participation names a current
		// enrolled declaration, while revocation names an immutable member ID.
		{[]string{"chat"}, completeNoArgs},
		{[]string{"chat", "ensure"}, completeNoArgs},
		{[]string{"chat", "status"}, completeNoArgs},
		{[]string{"chat", "channel"}, completeNoArgs},
		{[]string{"chat", "channel", "list"}, completeNoArgs},
		{[]string{"chat", "enroll"}, firstArgOnly(completeChatEnrollBoxes)},
		{[]string{"chat", "connect"}, firstArgOnly(completeChatParticipantBoxes)},
		{[]string{"chat", "disconnect"}, firstArgOnly(completeChatParticipantBoxes)},
		{[]string{"chat", "revoke"}, firstArgOnly(completeChatRevokeTargets)},
		{[]string{"chat", "nickname"}, completeNoArgs},          // a NEW display nickname
		{[]string{"chat", "channel", "create"}, completeNoArgs}, // a NEW channel

		// account — NAME is a plain account name. create names a NEW account
		// (suggest nothing); the management commands act on an existing one,
		// completing from accounts/ WITHOUT the "none" sentinel (none is a box
		// selection, not a manageable account — --account keeps it via
		// completeAccounts).
		{[]string{"account", "create"}, completeNoArgs}, // a NEW name
		{[]string{"account", "show"}, firstArgOnly(completeAccountNames)},
		{[]string{"account", "edit"}, firstArgOnly(completeAccountNames)},
		{[]string{"account", "destroy"}, firstArgOnly(completeAccountNames)},
		{[]string{"account", "secret", "set"}, firstArgOnly(completeAccountNames)},
		{[]string{"account", "secret", "unset"}, firstArgOnly(completeAccountNames)},
		{[]string{"account", "secret", "list"}, firstArgOnly(completeAccountNames)},
		// account auth — NAME AGENT: an existing account name, then an agent
		// from the catalog (whose declared auth: block applies).
		{[]string{"account", "auth", "import"}, argSequence(completeAccountNames, completeAgentProfiles)},
		{[]string{"account", "auth", "status"}, argSequence(completeAccountNames, completeAgentProfiles)},
		{[]string{"account", "observe"}, argSequence(completeAccountNames, completeAgentProfiles)},
	}
	for _, e := range table {
		if cmd := findByPath(root, e.path...); cmd != nil && cmd.ValidArgsFunction == nil {
			cmd.ValidArgsFunction = controllerCompletion("", e.fn)
		}
	}
}

// registerFlagCompleters attaches value completers to the recurring entity
// flags wherever they appear in the tree. Most flags mean the same thing on
// every command (`--space` always picks a configured space, etc.), so a
// generic walk covers them; the flags whose meaning IS command-specific
// (--workdir, --mount, --file) get per-command overrides first, and the walk
// skips any flag an override already claimed (RegisterFlagCompletionFunc
// refuses duplicates rather than overwriting).
func registerFlagCompleters(root *cobra.Command) {
	// Per-command overrides. --workdir on the box-creating commands is a
	// host dir constrained to the space's mount grants (the only values
	// create-time validation accepts); on the listings it's a filter whose
	// eligible set is the workdirs declared boxes actually bind; on `space
	// create` it names a NEW grant (the walk's unconstrained default below).
	// Each command with a specialized flag completer has its own row.
	overrides := []struct {
		path []string
		flag string
		fn   cobra.CompletionFunc
	}{
		{[]string{"space", "create"}, "account", completeNoArgs},
		{[]string{"space", "create"}, "memory", completeVMMemory},
		{[]string{"space", "create"}, "cpus", completeNewVMCPUs},
		{[]string{"space", "set"}, "memory", completeVMMemory},
		{[]string{"space", "set"}, "cpus", completeVMCPUs},
		{[]string{"space", "set"}, "image-layer-compression", cobra.FixedCompletions([]string{"none", spaceenv.ImageLayerGzip}, cobra.ShellCompDirectiveNoFileComp)},
		{[]string{"box", "create"}, "memory", completeBoxMemory},
		{[]string{"box", "copy"}, "memory", completeBoxMemory},
		{[]string{"box", "set"}, "memory", completeBoxMemory},
		{[]string{"box", "create"}, "workdir", completeGrantedDirs},
		{[]string{"box", "copy"}, "workdir", completeGrantedDirs},
		{[]string{"box", "create"}, "mount", completeGrantedMountSources},
		{[]string{"box", "copy"}, "mount", completeGrantedMountSources},
		{[]string{"box", "list"}, "workdir", completeDeclaredWorkdirs},
		{[]string{"account", "auth", "import"}, "file", completeHostFiles},
		{[]string{"audit", "logs"}, "box", completeKnownBoxes},
		{[]string{"audit", "export"}, "box", completeKnownBoxes},
		// Chat's --box names the server box a first ensure creates (never an
		// existing unrelated box); --human and --client are free-form names.
		{[]string{"chat"}, "box", completeNoArgs},
		{[]string{"chat", "ensure"}, "box", completeNoArgs},
		{[]string{"chat"}, "human", completeNoArgs},
		{[]string{"chat", "ensure"}, "human", completeNoArgs},
		{[]string{"chat", "enroll"}, "client", completeNoArgs},
		{[]string{"chat", "connect"}, "client", completeNoArgs},
		{[]string{"chat", "disconnect"}, "client", completeNoArgs},
	}
	for _, o := range overrides {
		if cmd := findByPath(root, o.path...); cmd != nil {
			_ = cmd.RegisterFlagCompletionFunc(o.flag, controllerCompletion(o.flag, o.fn))
		}
	}

	flagFns := map[string]cobra.CompletionFunc{
		"backend":        completeVMBackends,
		"vm-image":       completeHostDirs,
		"space":          completeSpaces, // any configured space (the command ensures it runs if needed)
		"from-space":     completeSpaces, // `box copy` source space
		"agent":          completeAgentProfiles,
		"account":        completeAccounts,
		"agentbox":       completeAgentboxes,
		"features":       completeFeatures,
		"feature":        completeFeatures,
		"feature-option": completeFeatureOptions,
		"capability":     completeCapabilities,
		// --drop-capability removes a capability; complete from the known
		// capability names (a superset of what's droppable — good enough).
		"drop-capability": completeCapabilities,
		"workdir":         completeHostDirs,
		"workdir-mode":    completeWorkdirModes,
		// --port / --env take free-form specs; offer nothing (and suppress
		// file completion) rather than a wrong guess. --mount is free-form
		// too, except where a grant-constrained override above claims it.
		"port":  completeNoArgs,
		"mount": completeNoArgs,
		"env":   completeNoArgs,
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for name, fn := range flagFns {
			// A persistent flag (chat/account --space) lives only in its
			// declaring command's PersistentFlags until Cobra merges it at
			// execution; registering there covers every subcommand.
			if c.Flags().Lookup(name) == nil && c.PersistentFlags().Lookup(name) == nil {
				continue
			}
			if _, ok := c.GetFlagCompletionFunc(name); ok {
				continue // per-command override wins
			}
			_ = c.RegisterFlagCompletionFunc(name, controllerCompletion(name, fn))
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// findByPath resolves a command by its full path from root (matching command
// names and aliases). Returns nil if the path doesn't resolve to a command
// exactly (no leftover args).
func findByPath(root *cobra.Command, path ...string) *cobra.Command {
	cmd, rest, err := root.Find(path)
	if err != nil || len(rest) != 0 {
		return nil
	}
	return cmd
}

// firstArgOnly limits a completer to the first positional.
func firstArgOnly(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fn(cmd, args, toComplete)
	}
}

// argSequence completes positional N with fns[N] (nothing past the last),
// for commands whose positionals are distinct entities — e.g. `account auth
// login NAME AGENT`.
func argSequence(fns ...cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) >= len(fns) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return fns[len(args)](cmd, args, toComplete)
	}
}

// copyArgs completes a catalog `copy SRC NEWNAME`: SRC (first positional) with
// fn (any existing item, built-in or custom); NEWNAME (second positional) with
// nothing, since it names a NEW custom item.
func copyArgs(fn cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return fn(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeCustomAgents / completeCustomFeatures / completeCustomCapabilities
// list only the user-defined (custom) catalog items — the eligible set for
// `edit` / `destroy`, which refuse built-ins.
func completeCustomAgents(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete(customAgentNames())
}

func completeCustomFeatures(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete(customFeatureNames())
}

func completeCustomCapabilities(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete(customCapabilityNames())
}

// completeNoArgs offers nothing and suppresses file completion for positionals
// that name a NEW entity (`space create`, `box create`), where existing host
// names/files are exactly the wrong suggestions.
func completeNoArgs(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// spaceScope resolves the space a completion should list within, mirroring the
// exact resolution the commands themselves use (config.ResolveSpace): the
// explicit --space flag, else the context pin, else "default". It MUST key off
// the flag's Changed bit, not emptiness — the --space flag's own default value
// is "default", so its value is never empty; an emptiness check would pin every
// completion to "default" and ignore `space use` entirely.
func spaceScope(cmd *cobra.Command) string {
	flagValue, flagChanged := "", false
	if f := cmd.Flags().Lookup("space"); f != nil {
		flagValue, flagChanged = f.Value.String(), f.Changed
	}
	space, _, err := config.ResolveSpace(flagValue, flagChanged)
	if err != nil {
		return defaultSpaceName
	}
	return space
}

func complete(names []string, err error) ([]cobra.Completion, cobra.ShellCompDirective) {
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	sort.Strings(names)
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeAgentProfiles(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	names, err := agentprofile.Names()
	return complete(names, err)
}

// completeAccounts suggests account selections for --account: every account
// name in the selected space plus the "none" sentinel for
// generic-shell boxes. Best-effort: a missing registry contributes nothing
// rather than erroring on a keystroke.
func completeAccounts(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	out := []string{account.None}
	if names, err := account.List(completionAccountSpace(cmd)); err == nil {
		out = append(out, names...)
	}
	return complete(out, nil)
}

// completeAccountNames suggests MANAGEABLE account names (the `account`
// subcommand positional): every account name, WITHOUT the "none" sentinel —
// none is a box selection, not an account that can be shown/edited/destroyed.
func completeAccountNames(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete(account.List(completionAccountSpace(cmd)))
}

func completeFeatures(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	names := append(features.Names(), ocifeature.CommonReferences...)
	names = append(names, ocifeature.CachedReferences()...)
	return complete(names, nil)
}

// completeCapabilities lists the known capability names (built-in registry +
// user-defined under ~/.config/yoloctl/capabilities/) for --capability.
func completeCapabilities(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete(capabilities.Names(), nil)
}

// completeHostDirs / completeHostFiles complete unconstrained host paths
// Go-side (completion_path.go) — the palette gets candidates too, which a
// shell directive can't deliver.
func completeHostDirs(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return listPathCandidates(toComplete, true, nil)
}

func completeHostFiles(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return listPathCandidates(toComplete, false, nil)
}

func completeVMBackends(_ *cobra.Command, _ []string, prefix string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var names []string
	for _, name := range vmhost.BackendNames() {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeVMMemory(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete([]string{"auto", "4GiB", "8GiB", "12GiB", "16GiB"}, nil)
}

func completeVMCPUs(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete([]string{"1", "2", "4", "8", "16", "32"}, nil)
}

// completeNewVMCPUs adds creation-only automatic selection.
func completeNewVMCPUs(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete([]string{"auto", "1", "2", "4", "8", "16", "32"}, nil)
}

func completeBoxMemory(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete([]string{"shared", "512m", "1g", "2g", "4g", "8g"}, nil)
}

// loadSpaceGrantRoots returns the scoped space config and its completion roots.
// writableOnly selects the roots that can actually back a host workdir under
// runtime preflight. A nil config means it could not be read, so callers may
// degrade to unconstrained host completion; an empty roots slice with a
// non-nil config is authoritative (the space has no eligible roots).
func loadSpaceGrantRoots(cmd *cobra.Command, writableOnly bool) (*spaceconfig.Config, []rootCandidate) {
	var scfg *spaceconfig.Config
	var err error
	if cmd.Root().Annotations["controller-client"] == "true" {
		var boot *spaceconfig.Bootstrap
		boot, err = spaceconfig.LoadBootstrap(spaceScope(cmd))
		if err == nil {
			scfg = &spaceconfig.Config{Mounts: boot.Mounts}
		}
	} else {
		scfg, err = spaceconfig.Load(spaceScope(cmd))
	}
	if err != nil {
		return nil, nil
	}
	seen := make(map[string]bool, len(scfg.Mounts)+1)
	roots := make([]rootCandidate, 0, len(scfg.Mounts)+1)
	add := func(path, desc string) {
		path = filepath.Clean(path)
		if path == "." || seen[path] {
			return
		}
		seen[path] = true
		roots = append(roots, rootCandidate{path: path, desc: desc})
	}
	defaultGrant, defaultGranted := scfg.GrantFor(scfg.DefaultWorkdir)
	if !writableOnly || defaultGranted && defaultGrant.Writable {
		add(scfg.DefaultWorkdir, "default workdir")
	}
	for _, m := range scfg.Mounts {
		if writableOnly && !m.Writable {
			continue
		}
		desc := "mount grant (ro)"
		if m.Writable {
			desc = "mount grant (rw)"
		}
		add(m.Source, desc)
	}
	return scfg, roots
}

// completeGrantedDirs suggests a box workdir: create-time validation accepts
// only host dirs under the scoped space's mount grants, so only those (and
// the path toward them) are offered, seeded with the grant roots themselves.
// With --workdir-mode ephemeral already on the line the flag combination is
// invalid — offer nothing rather than a value that will be rejected.
func completeGrantedDirs(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if f := cmd.Flags().Lookup("workdir-mode"); f != nil && f.Changed && f.Value.String() == agentbox.WorkdirModeEphemeral {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	scfg, roots := loadSpaceGrantRoots(cmd, true)
	if scfg == nil {
		return completeHostDirs(cmd, nil, toComplete)
	}
	return listPathCandidatesMatching(toComplete, true, roots, func(path string, isDir bool) bool {
		if grant, ok := scfg.GrantFor(path); ok && grant.Writable {
			return true
		}
		// A read-only or ungranted directory remains a useful navigation step
		// only when it is an ancestor of a deeper writable grant.
		if isDir {
			for _, root := range roots {
				if pathWithinRoot(root.path, path) {
					return true
				}
			}
		}
		return false
	})
}

// completeGrantedMountSources completes the SOURCE half of a --mount
// source:target[:ro] spec — the half that must lie under a space grant.
// Files complete too: parseMountSpec accepts host paths to either kind of
// object, and binding a single config file is a standard pattern. Past the first
// colon the value is an in-box target: a new path with nothing to list, so
// completion stops there.
func completeGrantedMountSources(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if strings.Contains(toComplete, ":") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	scfg, roots := loadSpaceGrantRoots(cmd, false)
	if scfg == nil {
		return completeHostFiles(cmd, nil, toComplete)
	}
	return listPathCandidates(toComplete, false, roots)
}

// completeDeclaredWorkdirs suggests values for the `box ls --workdir`
// FILTER: the distinct host workdirs declared boxes actually bind (arbitrary
// filesystem dirs are the wrong set for a filter). Scope mirrors the
// command: --all-spaces walks every space, else the usual --space scope.
// Ephemeral boxes have no host workdir and contribute nothing.
func completeDeclaredWorkdirs(cmd *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	refs, err := declaredBoxRefs(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	seen := make(map[string]bool, len(refs))
	workdirs := make([]string, 0, len(refs))
	for _, ref := range refs {
		cfg, err := agentbox.Load(ref.Space, ref.Name)
		if err != nil || !cfg.Workdir.IsHost() {
			continue
		}
		src := cfg.Workdir.MountSource()
		if src == "" || seen[src] {
			continue
		}
		seen[src] = true
		workdirs = append(workdirs, src)
	}
	return projectLiteralPaths(workdirs, toComplete), cobra.ShellCompDirectiveNoFileComp
}

// declaredBoxRefs lists the declared boxes the workdir filter draws from:
// every space when --all-spaces is set, else the scoped space.
func declaredBoxRefs(cmd *cobra.Command) ([]agentbox.AgentboxRef, error) {
	if f := cmd.Flags().Lookup("all-spaces"); f != nil && f.Changed && f.Value.String() == "true" {
		return agentbox.ListAllAgentboxes()
	}
	space := spaceScope(cmd)
	names, err := agentbox.ListNames(space)
	if err != nil {
		return nil, err
	}
	refs := make([]agentbox.AgentboxRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, agentbox.AgentboxRef{Space: space, Name: name})
	}
	return refs, nil
}

// completeWorkdirModes suggests the two valid --workdir-mode values.
func completeWorkdirModes(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete([]string{agentbox.WorkdirModeHost, agentbox.WorkdirModeEphemeral}, nil)
}

// completeCatalogRefs lists the buildable agentbox image refs (`image pull`).
func completeCatalogRefs(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return complete([]string{agentboximage.RefAgentbox}, nil)
}

func completeAgentboxes(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	names, err := agentbox.ListNames(spaceScope(cmd))
	return complete(names, err)
}

// completeCopySourceBoxes completes `box copy`'s SOURCE positional: the
// declared boxes of the space the source will be loaded from — --from-space
// when passed, else the usual --space scope.
func completeCopySourceBoxes(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	space := spaceScope(cmd)
	if f := cmd.Flags().Lookup("from-space"); f != nil && f.Changed {
		space = f.Value.String()
	}
	names, err := agentbox.ListNames(space)
	return complete(names, err)
}

func completeKnownBoxes(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	boxes, err := resolveBoxesForCompletion(cmd.Context(), spaceScope(cmd), boxstate.RuntimeSummary)
	if err != nil {
		return completeAgentboxes(cmd, nil, "")
	}
	names := make([]string, 0, len(boxes))
	for _, b := range boxes {
		if b.Declaration != boxstate.DeclarationMissing || b.Runtime == boxstate.RuntimeOrphaned {
			names = append(names, b.Name)
		}
	}
	return complete(names, nil)
}

func completeReconcileBoxes(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	boxes, err := resolveBoxesForCompletion(cmd.Context(), spaceScope(cmd), boxstate.FullDiff)
	if err != nil {
		return completeAgentboxes(cmd, nil, "")
	}
	names := make([]string, 0, len(boxes))
	for _, b := range boxes {
		switch {
		case b.Declaration == boxstate.DeclarationInvalid:
			names = append(names, b.Name)
		case b.Declaration == boxstate.DeclarationDeclared && b.Drift != boxstate.DriftInSync:
			names = append(names, b.Name)
		}
	}
	return complete(names, nil)
}

func resolveBoxesForCompletion(ctx context.Context, space string, depth boxstate.Depth) ([]boxstate.Box, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return newBoxStateResolver().ResolveSpace(ctx, space, depth)
}

// completeRunningBoxes lists the scoped space's agentboxes whose container is
// running — the eligible set for `box stop` / `box exec` (which need a live
// box). Falls back to all declared boxes when the space VM can't be queried
// (stopped / unreachable).
func completeRunningBoxes(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	space := spaceScope(cmd)
	declared, err := agentbox.ListNames(space)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return complete(filterByRunState(declared, runningBoxSet(cmd.Context(), space), true), nil)
}

// completeStartableBoxes lists the scoped space's agentboxes that are NOT
// running — the eligible set for `box start` (start materializes/boots a
// not-running box). Same runtime fallback.
func completeStartableBoxes(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	space := spaceScope(cmd)
	declared, err := agentbox.ListNames(space)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return complete(filterByRunState(declared, runningBoxSet(cmd.Context(), space), false), nil)
}

// runningBoxSet returns the set of agentboxes whose container is running in the
// given space, via the same `nerdctl ps` query `box ls` uses (stderr discarded
// for the completion probe). A box is running iff its status starts with "Up"
// (the convention used throughout the cmd layer). Returns a nil map (distinct
// from empty) when the space VM can't be queried, so callers fall back to
// declaration-derived eligibility; an empty non-nil map means "queried, none
// running".
func runningBoxSet(ctx context.Context, space string) map[string]bool {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	containers, err := container.New(space).ListQuiet(ctx, agentbox.LabelAgentbox)
	if err != nil {
		return nil
	}
	running := make(map[string]bool, len(containers))
	for _, ctr := range containers {
		if strings.HasPrefix(ctr.Status, "Up") {
			running[ctr.Name] = true
		}
	}
	return running
}

// completeSpaces lists the configured spaces — every bundle that holds a
// spaces/<name>/space.yaml — read straight from the on-disk registry
// (spaceconfig.List), the same authoritative source `space create` writes to.
// This is the declaration-level set used wherever any configured space is a
// valid target (the --space flag, `space destroy/show/use/logs`, mount
// management). Run-state-specific commands use the running/startable variants.
func completeSpaces(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	names, err := spaceconfig.List()
	return complete(names, err)
}

// completeRunningSpaces lists configured spaces whose VM is currently
// Running — the eligible set for `space stop` / `space ssh` (which need a live
// VM). Falls back to all configured spaces if the Lima runtime can't be
// queried.
func completeRunningSpaces(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	declared, err := spaceconfig.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return complete(filterByRunState(declared, runningSpaceSet(cmd.Context()), true), nil)
}

// completeStartableSpaces lists configured spaces whose VM is NOT currently
// Running — the eligible set for `space start`. Same runtime fallback.
func completeStartableSpaces(cmd *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
	declared, err := spaceconfig.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return complete(filterByRunState(declared, runningSpaceSet(cmd.Context()), false), nil)
}

// filterByRunState keeps the declared entities (spaces or boxes) whose running
// state matches wantRunning. A nil `running` map means the runtime was
// unavailable, so it falls back to the full declared set (declaration-derived
// eligibility).
func filterByRunState(declared []string, running map[string]bool, wantRunning bool) []string {
	if running == nil {
		return declared
	}
	out := make([]string, 0, len(declared))
	for _, s := range declared {
		if running[s] == wantRunning {
			out = append(out, s)
		}
	}
	return out
}

// runningSpaceSet returns the set of spaces whose Lima VM is Running, via a
// single short-bounded `limactl list`. Returns a nil map (distinct from an
// empty one) when the runtime can't be queried, so callers fall back to
// declaration-derived eligibility; an empty non-nil map means "queried, none
// running".
func runningSpaceSet(ctx context.Context) map[string]bool {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	instances, err := vmhost.New().List(ctx)
	if err != nil {
		return nil
	}
	running := make(map[string]bool, len(instances))
	for _, inst := range instances {
		if inst.Status == "Running" {
			running[inst.Name] = true
		}
	}
	return running
}

// completeSpaceMountAddArgs completes `space mount add SPACE PATH`: the SPACE
// name first, then a host PATH (a new grant — any directory).
func completeSpaceMountAddArgs(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeSpaces(cmd, args, toComplete)
	case 1:
		return completeHostDirs(cmd, args, toComplete)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeSpaceMountRemoveArgs completes `space mount remove SPACE PATH`: the
// SPACE name first, then only that space's currently granted mount sources
// (the only paths a remove can act on).
func completeSpaceMountRemoveArgs(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return completeSpaces(cmd, args, toComplete)
	case 1:
		scfg, err := spaceconfig.Load(args[0])
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		sources := make([]string, 0, len(scfg.Mounts))
		for _, m := range scfg.Mounts {
			sources = append(sources, m.Source)
		}
		return complete(sources, nil)
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completionAccountSpace(cmd *cobra.Command) string {
	space, err := selectedSpace(cmd)
	if err != nil {
		return ""
	}
	return space
}
