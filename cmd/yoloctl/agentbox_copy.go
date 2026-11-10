package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// newAgentboxCopyCmd is `box copy SOURCE NEWNAME`: create a new box from an
// existing box's manifest, with every `box create` flag accepted as an
// override. The manifest is the template — nothing else: workdir contents,
// account state, and runtime state are never copied. The merge itself is
// copyOptionsFromSource; everything after it is the ordinary create pipeline,
// so copy and create can never diverge on validation or side effects.
func newAgentboxCopyCmd() *cobra.Command {
	var (
		opts      agentboxCreateOptions
		specs     agentboxCreateSpecs
		fromSpace string
	)
	cmd := &cobra.Command{
		Use:   "copy SOURCE NEWNAME",
		Short: "Create a new box from an existing box's manifest (settings only)",
		Long: `Creates NEWNAME using SOURCE's manifest as a template, then runs the
normal create pipeline: the same validation, preflight, reconcile, and
autoupdate as ` + "`box create`" + `.

Settings only: this never copies workdir contents, account state, or
runtime state. By default the copy selects the source's account NAME in
the destination space. Within one space, secrets and the agent's native
state are shared; across spaces, the destination's independent account
must already exist. A host workdir is shared with the source (both
boxes see the same host directory). --account, --agent (a different
agent uses its own per-agent state leaf), and --workdir change that; an
ephemeral workdir is always re-derived for NEWNAME and starts empty.

Every ` + "`box create`" + ` flag is accepted: a flag you pass explicitly
overrides the source's setting, anything else keeps the source's value.
Collection flags (--features, --capability, --mount, --env, --port,
--agent-extra-args) replace the source's list wholesale; pass an empty
value (e.g. --port "") to clear one. Overriding --agent re-derives
--image and --features — from the new agent's profile, or the create
defaults for --agent none — unless you pass those flags too, and
re-defaults the account when the source's selection no longer fits.
Ports are copied verbatim from the source manifest (the workdir's
devcontainer.json is NOT re-read); the source and the copy cannot both
hold the same published host port at the same time.

--space picks the DESTINATION space (flag > YOLOCTL_SPACE > global context > default);
--from-space picks the space holding SOURCE (defaults to the
destination). A cross-space copy needs the destination space to grant
the host workdir and any extra mounts (yoloctl space mount add);
--manifest-only defers that check to reconcile, but still requires the
destination controller. Cross-space copying requires both spaces running.

  yoloctl box copy coder coder-2
  yoloctl box copy coder reviewer --read-only
  yoloctl box copy coder codex-coder --agent codex
  yoloctl box copy coder staged --from-space default --space staging --manifest-only`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(cmd, args); err != nil {
				return err
			}
			// Validate before controller routing can resolve either space.
			if err := validateAgentboxName(args[1]); err != nil {
				return err
			}
			return validateAgentboxName(args[0])
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			source, newName := args[0], args[1]
			if err := validateAgentboxName(newName); err != nil {
				return err
			}
			if err := validateAgentboxName(source); err != nil {
				return err
			}
			if err := resolveSpaceFlag(cmd, &opts.Space); err != nil {
				return err
			}
			srcSpace := opts.Space
			if flagChanged(cmd, "from-space") {
				if err := validateSpaceName(fromSpace); err != nil {
					return err
				}
				srcSpace = fromSpace
			}
			if srcSpace == opts.Space && source == newName {
				return asUserError(fmt.Errorf(
					"cannot copy box %q in space %q onto itself — pick a different NEWNAME or destination --space",
					source, opts.Space))
			}
			// Parse the explicit override specs exactly as create does, after
			// the copy-only empty-filter implementing "empty value clears".
			ports, err := parsePortFlags(dropEmptySpecs(specs.Ports))
			if err != nil {
				return err
			}
			opts.Ports = ports
			mounts, err := parseMountFlags(dropEmptySpecs(specs.Mounts))
			if err != nil {
				return err
			}
			opts.Mounts = mounts
			envMap, err := parseEnvFlags(dropEmptySpecs(specs.Envs))
			if err != nil {
				return err
			}
			opts.Env = envMap
			src, err := loadCopySourceForOperation(cmd, srcSpace, source)
			if err != nil {
				return err
			}
			changed := func(name string) bool { return flagChanged(cmd, name) }
			merged, notices, err := copyOptionsFromSource(changed, opts, src, opts.Space, newName)
			if err != nil {
				return err
			}
			opts = merged
			for _, n := range notices {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "notice: %s\n", n)
			}
			// A real --agent override re-derives --image / --features from the
			// NEW agent's profile (the merge already handled --agent none).
			// Deliberately gated on the flag: applyAgentProfileDefaults
			// overwrites any not-Changed image/features, which on an
			// un-overridden agent would clobber the source's settings with
			// profile defaults.
			if changed("agent") && hasRealAgent(opts.Agent) {
				profile, err := lookupAgentProfile(opts.Agent)
				if err != nil {
					return err
				}
				applyAgentProfileDefaults(cmd, &opts, profile)
			}
			wd, err := resolveEffectiveWorkdir(opts.Space, newName, opts.Workdir, opts.WorkdirMode, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			opts.workdir = wd
			if !opts.ManifestOnly {
				if err := requireAccountForCreate(opts.Space, opts.Account); err != nil {
					return err
				}
				if err := ensureSpaceRunning(cmd.Context(), opts.Space); err != nil {
					return err
				}
				if err := prepareAutoAttach(cmd, opts.Space, newName, opts.NoAttach, true); err != nil {
					return err
				}
			}
			var agentUpdated bool
			if err := createAgentbox(cmd.Context(), opts, cmd.ErrOrStderr(), &agentUpdated); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "copied box %q (space %s) to %q (space %s)\n",
				source, srcSpace, newName, opts.Space)
			if opts.ManifestOnly {
				return nil
			}
			return maybeAutoAttach(cmd, opts.Space, newName, opts.NoAttach, opts.NoAutoupdate, agentUpdated)
		},
	}
	registerAgentboxCreateFlags(cmd, &opts, &specs)
	cmd.Flags().StringVar(&fromSpace, "from-space", "", "Space holding SOURCE (defaults to the destination space resolved from --space / the pinned context)")
	return cmd
}

// copyOptionsFromSource merges an existing box manifest with the explicitly
// passed `box copy` flags into the agentboxCreateOptions the create pipeline
// consumes. Pure: changed reports whether a flag was explicitly passed
// (injected so tests drive it with a map), flags carries the flag values
// (with --port/--mount/--env already parsed), src is the loaded source
// manifest. Returns the merged options plus human notices for stderr.
//
// The rule is uniform — a Changed flag wins, anything else derives from the
// source — with these copy-specific derivations:
//
//   - collections replace wholesale (an empty Changed value clears);
//   - --read-only derives from the source's effective posture
//     (!WorkdirWritable) so the capability set round-trips exactly;
//   - the managed account-state mount is stripped from inherited mounts
//     (the pipeline re-derives it for the effective account/agent);
//   - an inherited host workdir is the source's CANONICAL path
//     (Workdir.MountSource) — the authoritative identity — so a later
//     symlink retarget can't point the copy somewhere else; an inherited
//     ephemeral workdir stays empty for re-derivation from NEWNAME;
//   - --agent overrides re-default the account / drop agent extra args when
//     the source's selection is structurally incompatible with the new agent.
func copyOptionsFromSource(changed func(string) bool, flags agentboxCreateOptions, src *agentbox.Config, destSpace, newName string) (agentboxCreateOptions, []string, error) {
	opts := agentboxCreateOptions{
		Space: destSpace,
		Name:  newName,
		// The source's Ports are the persisted devcontainer snapshot;
		// re-discovery would union today's file into a verbatim copy.
		skipDevcontainerPorts: true,
		ManifestOnly:          flags.ManifestOnly,
		NoAutoupdate:          flags.NoAutoupdate,
		NoAttach:              flags.NoAttach,
	}
	var notices []string

	// --agent / --account feed resolveAgentAndAccount with the same raw
	// shapes create uses. When --agent changes the box's agent-ness and
	// --account wasn't given, the source's account no longer fits
	// structurally (real agents require a real account, generic shells use
	// none): re-default rather than fail on a value the user never passed.
	agentFlag := src.Agent
	if changed("agent") {
		agentFlag = flags.Agent
	}
	accountFlag := src.Account
	if changed("account") {
		accountFlag = flags.Account
	}
	if changed("agent") && !changed("account") {
		switch {
		case hasRealAgent(agentFlag) && accountFlag == account.None:
			accountFlag = "" // resolves to the default account
		case !hasRealAgent(agentFlag) && accountFlag != "" && accountFlag != account.None:
			notices = append(notices, fmt.Sprintf(
				"dropping account %q: a generic-shell box (--agent none) uses account none", accountFlag))
			accountFlag = "" // resolves to none
		}
	}
	resolvedAgent, resolvedAccount, err := resolveAgentAndAccount(opts.Space, agentFlag, accountFlag)
	if err != nil {
		return agentboxCreateOptions{}, nil, err
	}
	opts.Agent, opts.Account = resolvedAgent, resolvedAccount

	opts.AgentExtraArgs = slices.Clone(src.AgentExtraArgs)
	if changed("agent-extra-args") {
		opts.AgentExtraArgs = dropEmptySpecs(flags.AgentExtraArgs)
	} else if !hasRealAgent(opts.Agent) && len(opts.AgentExtraArgs) > 0 {
		notices = append(notices, fmt.Sprintf(
			"dropping agent_extra_args %v: generic-shell boxes have no managed agent argv", opts.AgentExtraArgs))
		opts.AgentExtraArgs = nil
	}

	opts.AgentSessionResume = src.ResumePolicy()
	if changed("agent-session-resume") {
		opts.AgentSessionResume = flags.AgentSessionResume
	} else if !hasRealAgent(opts.Agent) && opts.AgentSessionResume != "" {
		notices = append(notices, fmt.Sprintf(
			"dropping agent_session.resume %s: generic-shell boxes have no agent conversation", opts.AgentSessionResume))
		opts.AgentSessionResume = ""
	}

	opts.SessionCommand = slices.Clone(src.SessionCommand)
	if changed("session-command") {
		opts.SessionCommand = slices.Clone(flags.SessionCommand)
		if slices.Equal(opts.SessionCommand, []string{""}) {
			opts.SessionCommand = nil
		}
	} else if hasRealAgent(opts.Agent) && len(opts.SessionCommand) > 0 {
		notices = append(notices, "dropping session_command: agent boxes use their agent profile")
		opts.SessionCommand = nil
	}

	opts.Image = src.Image
	if changed("image") {
		opts.Image = flags.Image
	}
	opts.FeatureOptions = src.FeatureOptions
	opts.FeatureLocks = src.FeatureLocks
	opts.FeatureOptionSpecs = flags.FeatureOptionSpecs
	opts.Features = slices.Clone(src.Features)
	if changed("features") || changed("feature") {
		opts.Features = dropEmptySpecs(flags.Features)
		opts.ExtraFeatures = slices.Clone(flags.ExtraFeatures)
	}
	if changed("agent") && !hasRealAgent(opts.Agent) {
		// --agent none re-derives like a fresh generic-shell create: default
		// base image, no features. (A real --agent's profile prefill needs a
		// catalog lookup and lives in RunE.) Keeping the old agent's values
		// would ship its CLI image/features on a box that no longer hosts it.
		if !changed("image") {
			opts.Image = defaultAgentboxImage
		}
		if !changed("features") && !changed("feature") {
			opts.Features = nil
		}
	}

	// The manifest's capability list already reflects every historic
	// --capability / --drop-capability / --read-only, so it feeds back
	// verbatim (the implicit workdir-write append is Contains-guarded) with
	// ReadOnly derived from the source's effective posture to re-drop
	// workdir-write when absent. Drops themselves are never persisted:
	// inherit none.
	opts.Capabilities = slices.Clone(src.Capabilities)
	if changed("capability") {
		opts.Capabilities = dropEmptySpecs(flags.Capabilities)
	}
	opts.DropCapabilities = dropEmptySpecs(flags.DropCapabilities)
	opts.ReadOnly = !src.WorkdirWritable()
	if changed("read-only") {
		opts.ReadOnly = flags.ReadOnly
	}

	opts.UnconfinedNetwork = src.UnconfinedNetwork
	if changed("unconfined-network") {
		opts.UnconfinedNetwork = flags.UnconfinedNetwork
	}
	opts.Privileged = src.Privileged
	if changed("privileged") {
		opts.Privileged = flags.Privileged
	}
	opts.CPUs = src.Resources.CPUs
	if changed("cpus") {
		opts.CPUs = flags.CPUs
	}
	opts.Memory = src.Resources.Memory
	if changed("memory") {
		opts.Memory = flags.Memory
	}

	// Clones throughout: the pipeline normalizes cfg.Mounts (and friends) in
	// place and must never write back into the loaded source config.
	opts.Env = maps.Clone(src.Env)
	if changed("env") {
		opts.Env = flags.Env
	}
	opts.Mounts = stripManagedStateMounts(src)
	if changed("mount") {
		opts.Mounts = flags.Mounts
	}
	opts.Ports = slices.Clone(src.Ports)
	if changed("port") {
		opts.Ports = flags.Ports
	} else if len(opts.Ports) > 0 {
		notices = append(notices, copiedPortsNotice(src.Name, opts.Ports))
	}

	// Workdir pairing: a bare --workdir implies host mode (an inherited
	// ephemeral mode would otherwise reject the user's path with an error
	// they never caused); an effective ephemeral mode keeps the value empty
	// so resolveEffectiveWorkdir re-derives the VM-local dir from NEWNAME —
	// copying the source's would share one wipe-on-destroy dir between the
	// two boxes.
	opts.WorkdirMode = src.Workdir.Mode
	if changed("workdir-mode") {
		opts.WorkdirMode = flags.WorkdirMode
	} else if changed("workdir") {
		opts.WorkdirMode = agentbox.WorkdirModeHost
	}
	if changed("workdir") {
		opts.Workdir = flags.Workdir
	} else if opts.WorkdirMode != agentbox.WorkdirModeEphemeral && src.Workdir.IsHost() {
		opts.Workdir = src.Workdir.MountSource()
	}
	// else: ephemeral (re-derived from NEWNAME), or an ephemeral source
	// switched to host mode with no --workdir — the empty value falls back
	// to the destination space's default workdir, as on create.

	return opts, notices, nil
}

// stripManagedStateMounts returns src's extra mounts without the managed
// account-state slot: primarily any entry at the source agent profile's
// StateContainerPath (the slot applyAgentStateMount owns), plus — as a
// fallback for an un-lookupable source profile — any entry whose source
// rides the account-state root (which only the managed slot may do in a
// yoloctl-validated manifest). The pipeline re-derives the slot for the
// copy's effective account/agent; an inherited stale slot would otherwise
// survive as an ordinary bind and be rejected by rejectAccountRootBinds.
func stripManagedStateMounts(src *agentbox.Config) []agentbox.Mount {
	managedDest := managedStateDest(src)
	root, err := layout.AccountStateRoot()
	if err != nil {
		root = ""
	}
	kept := make([]agentbox.Mount, 0, len(src.Mounts))
	for _, m := range src.Mounts {
		if managedDest != "" && m.Target == managedDest {
			continue
		}
		if root != "" && pathsIntersect(m.Source, root) {
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// loadCopySource loads `box copy`'s SOURCE manifest, mapping a missing
// declaration to a copy-specific user error (loadManagedAgentbox's hints
// suggest creating the box — wrong verbs for a source miss). No lock: Save
// is atomic tmp+rename, so a plain read never sees a partial manifest, and
// taking the source lock here would only add a lock-file side effect for a
// typo'd --from-space.
func loadCopySource(space, name string) (*agentbox.Config, error) {
	cfg, err := agentbox.Load(space, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, asUserError(fmt.Errorf(
				"source box %q not found in space %q — list boxes with: yoloctl box list --space %s (pick the source space with --from-space)",
				name, space, space))
		}
		return nil, fmt.Errorf("load source box manifest: %w", err)
	}
	return cfg, nil
}

func loadCopySourceForOperation(cmd *cobra.Command, space, name string) (*agentbox.Config, error) {
	if source, ok := cmd.Context().Value(copySourceKey{}).(*agentbox.Config); ok {
		if source.Space != space || source.Name != name {
			return nil, errors.New("cross-space copy snapshot identity mismatch")
		}
		return source, nil
	}
	return loadCopySource(space, name)
}

// copiedPortsNotice warns that inherited port forwards publish the source's
// fixed host ports, which are host-exclusive: the source and the copy cannot
// both hold them at the same time (the loser fails at reconcile).
func copiedPortsNotice(srcName string, ports []agentbox.Port) string {
	published := make([]string, 0, len(ports))
	for _, p := range ports {
		published = append(published, strconv.Itoa(p.Published))
	}
	return fmt.Sprintf(
		"copied port forward(s) from %q publishing host port(s) %s; two boxes cannot hold the same published port at the same time — override with --port (or clear with --port \"\")",
		srcName, strings.Join(published, ", "))
}

// dropEmptySpecs implements the `box copy` "empty value clears" convention
// for collection flags: empty (or whitespace-only) elements are dropped, and
// a list that was ONLY empties collapses to nil — an explicit empty
// replacement for the inherited collection. Create never calls this: its
// parsers keep rejecting empty specs.
func dropEmptySpecs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
