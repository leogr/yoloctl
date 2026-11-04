package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newAgentboxReconcileCmd wires `yoloctl box reconcile NAME`. It loads the box
// YAML (erroring if the box has no declaration), then runs Reconcile against the
// live VM state via LiveInspector + LiveApplier. Prints what changed.
//
// `--dry-run` runs Diff only, printing the strategy that would be
// taken and the port-forward delta.
func newAgentboxReconcileCmd() *cobra.Command {
	var (
		spaceName string
		dryRun    bool
		rebuild   bool
	)
	cmd := &cobra.Command{
		Use:   "reconcile BOX",
		Short: "Reconcile a box runtime with its manifest",
		Long: `Loads the box declaration through the running space controller,
diffs against the actual VM state, and applies the changes. A missing
declaration is an error; use box edit to change an existing declaration.

  - Create   — container doesn't exist (initial provisioning).
  - Recreate — an immutable field changed (image, resources, mounts).
  - Restart  — the computed policy changed; sandbox is stopped and
               restarted with the new policy.yaml (snapshot + rollback
               on failure).
  - Live     — only port forwards changed; no container disturbance.

  yoloctl box reconcile coder
  yoloctl box reconcile coder --space prod-untrusted
  yoloctl box reconcile coder --dry-run    # see the plan; do nothing
  yoloctl box reconcile coder --rebuild    # force a fresh composed-image build

Idempotent: re-reconciling against already-reconciled state is a no-op.

--rebuild forces a fresh build of the box's feature-composed runtime image
(cache-busted, so ` + "`latest`" + `-pinned features like go/agy/claude/codex/gemini/opencode/pi refetch) and
then recreates the container — even though the content-addressed image ref is
unchanged. Use it to pull a newer ` + "`latest`" + ` into an existing box; an ordinary
reconcile reuses the cached image.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			if dryRun && rebuild {
				return asUserError(errors.New("--dry-run and --rebuild are mutually exclusive"))
			}
			return runReconcile(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				spaceName, args[0], dryRun, rebuild)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the plan without applying it")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "Force a fresh build of the feature-composed image (cache-busted) and recreate the box")
	return cmd
}

// newAgentboxDiffCmd is `yoloctl box diff NAME` — shorthand for
// `box reconcile NAME --dry-run`, kept as a first-class subcommand
// because "diff" is what users with kubectl muscle memory will reach
// for.
func newAgentboxDiffCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "diff BOX",
		Short: "Show the reconcile plan without applying it (like `reconcile --dry-run`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			return runReconcile(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				spaceName, args[0], true, false)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}

// runReconcile is the shared reconcile/diff entry point. dryRun true:
// only compute and print the DiffPlan. dryRun false: also call
// Reconcile and print the ReconcileResult. rebuild true forces a fresh
// composed-image build + recreate (see runRebuild); it is rejected with
// dryRun by the caller.
func runReconcile(ctx context.Context, out, errw io.Writer, spaceName, name string, dryRun, rebuild bool) error {
	lock, err := lockSessionBoxFn(spaceName, name)
	if err != nil {
		return fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer lock.Release()

	client := container.New(spaceName)
	cfg, err := loadManagedAgentbox(spaceName, name)
	if err != nil {
		return err
	}

	if dryRun {
		// Compute the plan against actual state and print it; do
		// not call Reconcile. This is the diff path.
		inspector := agentbox.LiveInspector{Client: client}
		actual, err := inspector.Inspect(ctx, cfg)
		if err != nil {
			return fmt.Errorf("inspect actual state: %w", err)
		}
		return printDryRun(out, cfg, actual)
	}

	// apply may have to Create (container gone) or Recreate, so run the
	// same VM-side preflight as every other create/recreate path (strict
	// account existence included). The dry-run branch above returns before
	// here — diff is read-only.
	if !rebuild {
		result, err := reconcileManaged(ctx, lock, client, cfg, errw)
		if err != nil {
			return err
		}
		return printReconcileResult(out, name, result)
	}
	if err := preflightAgentboxRuntime(ctx, client, cfg, true); err != nil {
		return err
	}
	inspector := agentbox.LiveInspector{Client: client}
	applier := newManagedLiveApplier(client, errw)
	return runRebuild(ctx, lock, out, errw, client, cfg, name, inspector, applier)
}

// runRebuild forces a fresh build of the box's feature-composed runtime image
// and recreates the container. The composed ref is content-addressed by recipe,
// so a same-recipe `latest` refresh does NOT move the ref — an ordinary
// reconcile would see "in sync" and do nothing. So --rebuild carries its own
// explicit signal: EnsureComposedImage(Force) rebuilds the tag in place
// (cache-busted), then we drive a recreate directly rather than reading a Diff
// verdict. The subsequent Create's image-ensure finds the freshly-built tag
// present and reuses it.
func runRebuild(ctx context.Context, lock *agentsession.BoxLock, out, errw io.Writer, client *container.Client, cfg *agentbox.Config, name string, inspector agentbox.LiveInspector, applier agentbox.Applier) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	tracking, _, err := sessionTrackingFor(cfg)
	if err != nil {
		return err
	}
	if err := agentbox.ValidateChatDeclaration(cfg); err != nil {
		return err
	}
	policy, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		return err
	}
	candidates, err := agentbox.RuntimeFeatures(cfg)
	if err != nil {
		return err
	}
	plan, err := cfg.FeaturePlan()
	if err != nil {
		return err
	}
	if _, err := agentboximage.EnsureComposedImage(ctx, client, cfg.Image, candidates,
		agentboximage.Options{Force: true, Notice: errw, FeaturePlan: plan}); err != nil {
		return fmt.Errorf("rebuild composed image: %w", err)
	}
	actual, err := inspector.Inspect(ctx, cfg)
	if err != nil {
		return fmt.Errorf("inspect actual state: %w", err)
	}
	if actual.Exists {
		if err := applier.Recreate(ctx, cfg, policy); err != nil {
			return fmt.Errorf("recreate container: %w", err)
		}
		_, _ = fmt.Fprintf(out, "Box %q: rebuilt composed image and recreated.\n", name)
		return applySessionStart(lock, tracking, true, false, errw)
	}
	if err := applier.Create(ctx, cfg, policy); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	_, _ = fmt.Fprintf(out, "Box %q: built composed image and created.\n", name)
	return applySessionStart(lock, tracking, true, false, errw)
}

// printDryRun assembles the diff plan and writes a human-readable
// summary. Calls the same ComposePolicy + Diff sequence Reconcile
// uses, then stops before any Applier call.
func printDryRun(out io.Writer, cfg *agentbox.Config, actual agentbox.ActualState) error {
	desired, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		// Don't fail the dry-run on a missing policy — still useful
		// to see the container-level diff. Just note it.
		_, _ = fmt.Fprintf(out, "WARNING: cannot compute desired policy: %v\n", err)
		_, _ = fmt.Fprintf(out, "         (showing container-level diff only; policy diff skipped)\n\n")
		desired = actual.PolicyContent // forces "policy unchanged" branch
	}
	desiredImage, _, err := agentbox.ResolvedImage(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(out, "WARNING: cannot resolve runtime image: %v\n", err)
		_, _ = fmt.Fprintf(out, "         (showing diff with the image axis skipped)\n\n")
		desiredImage = actual.Image // forces "image unchanged" branch
	}
	plan := agentbox.Diff(cfg, actual, desiredImage, desired)

	switch {
	case plan.NeedsCreate:
		_, _ = fmt.Fprintf(out, "PLAN: create container (%s)\n", plan.CreateReason)
	case plan.NeedsRecreate:
		_, _ = fmt.Fprintf(out, "PLAN: recreate container (%s)\n", plan.RecreateReason)
	case plan.NeedsRestart:
		_, _ = fmt.Fprintf(out, "PLAN: restart container (%s)\n", plan.RestartReason)
	default:
		if plan.PolicyNote != "" {
			_, _ = fmt.Fprintf(out, "NOTE: %s\n", plan.PolicyNote)
		}
		if len(plan.PortForwardsToAdd) == 0 && len(plan.PortForwardsToRemove) == 0 {
			_, _ = fmt.Fprintln(out, "PLAN: no changes")
			return nil
		}
		_, _ = fmt.Fprintln(out, "PLAN: live port-forward updates only")
	}
	if len(plan.PortForwardsToRemove) > 0 {
		_, _ = fmt.Fprintln(out, "  Port forwards to remove:")
		for _, pf := range plan.PortForwardsToRemove {
			_, _ = fmt.Fprintf(out, "    - %d:%d\n", pf.Published, pf.Target)
		}
	}
	if len(plan.PortForwardsToAdd) > 0 {
		_, _ = fmt.Fprintln(out, "  Port forwards to add:")
		for _, pf := range plan.PortForwardsToAdd {
			_, _ = fmt.Fprintf(out, "    + %d:%d\n", pf.Published, pf.Target)
		}
	}
	return nil
}

// newAgentboxEditCmd wires `yoloctl box edit NAME`. It operates on the box
// manifest rather than the computed, bind-mounted policy.yaml: writes the
// current Config to a
// temp file, opens $EDITOR, parses + Saves the result, then
// reconciles. No-op when the user closes the editor without
// changes.
func newAgentboxEditCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "edit BOX",
		Short: "Edit the box YAML in $EDITOR; reconcile on save",
		Long: `Fetches the box declaration from the running space controller and
opens a temporary copy in your local $EDITOR (default ` + "`vi`" + `). On save,
the controller rejects concurrent changes, validates and writes the result
atomically, then reconciles against the actual VM state — the same flow as
` + "`box reconcile`" + `.

  EDITOR=nano yoloctl box edit coder
  yoloctl box edit reviewer --space prod-untrusted

The box must already have a declaration; ` + "`box edit`" + ` never
creates one (a missing YAML is a clear error).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			return runEdit(cmd.Context(), cmd.OutOrStdout(), spaceName, args[0])
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}

func runEdit(ctx context.Context, out io.Writer, spaceName, name string) error {
	var box *agentsession.BoxLock
	lock := func() (func(), error) {
		return lockManifestEdit(ctx, out, spaceName, func() (func(), error) {
			l, err := lockSessionBoxFn(spaceName, name)
			if err != nil {
				return nil, err
			}
			box = l
			return l.Release, nil
		})
	}
	release, err := lock()
	if err != nil {
		return fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	client := container.New(spaceName)
	cfg, err := loadManagedAgentbox(spaceName, name)
	if err != nil {
		return err
	}

	manifestSnapshot, err := snapshotEditFile(cfg.Path())
	if err != nil {
		return err
	}
	spaceSnapshot, err := snapshotEditSpace(spaceName)
	if err != nil {
		return err
	}
	pre, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal current config: %w", err)
	}
	tmp, err := os.CreateTemp("", "yoloctl-agentbox-"+name+"-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(pre); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	release()
	release = nil
	if err := runEditor(ctx, tmpPath); err != nil {
		return err
	}
	post, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	if bytes.Equal(pre, post) {
		_, _ = fmt.Fprintln(out, "no changes")
		return nil
	}

	edited, err := agentbox.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("parse edited YAML: %w", err)
	}
	if edited.Name != name || edited.Space != spaceName {
		return asUserError(errors.New("box edit cannot change the box name or space; use box copy"))
	}
	release, err = lock()
	if err != nil {
		removeTemp = false
		return fmt.Errorf("reacquire edit lock (edits kept at %s): %w", tmpPath, err)
	}
	if !spaceSnapshot.unchanged() || !manifestSnapshot.unchanged() {
		removeTemp = false
		return asUserError(fmt.Errorf("box or space changed while editing; retry against current state (edits kept at %s)", tmpPath))
	}
	// Open sets edited.path to the temp file; we want Save to land
	// at the canonical config path. Construct a fresh Config with
	// the edited content but no path so Save resolves it.
	edited = withCanonicalPath(edited)
	if err := requireResolvableManifest(ctx, edited); err != nil {
		return err
	}
	if _, err := canonicalizeUserMountSources(edited); err != nil {
		return err
	}
	if err := rejectAccountRootBinds(edited); err != nil {
		return err
	}
	// An edit can RE-POINT the box's account, so the save runs under the
	// selected account's lock with a load check: a missing account
	// fails BEFORE anything is saved (the previous manifest stands), and a
	// racing destroy either wins entirely or sees the saved reference.
	if err := saveRepointableBoxManifest(edited); err != nil {
		return err
	}

	return reconcileAndPrint(ctx, box, out, client, edited, name)
}

// runEditor opens path in the user's $EDITOR (or `vi` when unset) and
// waits for the editor to exit. Editor stdio is wired to the user's
// terminal so curses-based editors work.
func runEditor(ctx context.Context, path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.CommandContext(ctx, editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// withCanonicalPath returns a Config that will Save to the
// canonical BoxPath(name) on the next Save call. agentbox.Open
// sets the path field to the input path, but for `edit` that input
// is a tmp file we don't want to overwrite our config from.
func withCanonicalPath(c *agentbox.Config) *agentbox.Config {
	// Re-marshal + Open from the canonical path's directory: easier
	// to round-trip than to expose a setter. Performance is not an
	// issue at agentbox-edit cadence.
	raw, err := yaml.Marshal(c)
	if err != nil {
		// Should not happen — c just came from Open. Fall through
		// and let Save's Validate catch any latent issue.
		return c
	}
	// Use a path-less copy: Save will resolve BoxPath(c.Name).
	fresh := &agentbox.Config{}
	_ = yaml.Unmarshal(raw, fresh)
	// SchemaVersion is filled in by Open; preserve.
	fresh.SchemaVersion = c.SchemaVersion
	return fresh
}

// newAgentboxSetCmd wires `yoloctl box set NAME [flags]`.
// Mutates one or more scalar Config fields, then reconciles. Each
// flag is optional; only the ones the user explicitly passes are
// applied (Cobra's Changed() check distinguishes "user said
// --read-only=false" from "user didn't say anything").
//
// Scalar fields + ad-hoc --allow appends (which carry list semantics
// but compose additively rather than replacing). ports and
// mounts still need their own surfaces on `set` — `port forward` covers
// the former; mounts are set at create time (--mount) or YAML-edit-only here.
func newAgentboxSetCmd() *cobra.Command {
	var (
		spaceName         string
		image             string
		cpus              string
		memory            string
		readOnly          bool
		unconfinedNetwork bool
		privileged        bool
		sessionCommand    []string
		agentExtraArgs    []string
		sessionResume     string
		featureNames      []string
		extraFeatures     []string
		featureOptions    []string
		capabilities      []string
	)
	cmd := &cobra.Command{
		Use:   "set BOX [flags]",
		Short: "Mutate scalar box fields and reconcile",
		Long: `Updates one or more fields in the box declaration through the running
space controller and reconciles against the actual VM state. Only the flags
you explicitly pass are applied; the rest of the declaration is left untouched.

  yoloctl box set coder --image yoloctl/agentbox:ubuntu-24.04
  yoloctl box set coder --read-only=false --cpus 4 --memory 4g
  yoloctl box set coder --capability network.github
  yoloctl box set coder --features go,github
  yoloctl box set coder --agent-extra-args=--model,opus

` + "`--read-only`" + ` toggles the fs.workdir.write capability (true removes it →
/workdir read-only, false adds it → read-write). ` + "`--capability`" + ` appends
positive grants to the YAML's ` + "`capabilities:`" + ` list (additive — duplicates
are skipped). Remove a capability by editing the YAML with ` + "`box edit`" + `;
inspect the resulting policy with ` + "`box inspect`" + `.

A capability or feature name no catalog defines is rejected before anything
is saved. Values the manifest already holds are not rewritten, but the box is
still reconciled — so repeating a set that failed midway applies it.

` + "`--agent-extra-args`" + ` replaces the persisted argv suffix used when
yoloctl next starts the managed agent session; restart the box/session to affect
an already-running agent. For other list-shaped fields (port forwards, extra mounts) use the
dedicated commands (` + "`port forward`" + `) or ` + "`box edit`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			return runSet(cmd.Context(), cmd.OutOrStdout(), spaceName, args[0], setOptions{
				Image:             stringFlag(cmd, "image", image),
				CPUs:              stringFlag(cmd, "cpus", cpus),
				Memory:            stringFlag(cmd, "memory", memory),
				ReadOnly:          boolFlag(cmd, "read-only", readOnly),
				UnconfinedNetwork: boolFlag(cmd, "unconfined-network", unconfinedNetwork),
				Privileged:        boolFlag(cmd, "privileged", privileged),
				AgentExtraArgs:    stringSliceFlag(cmd, "agent-extra-args", agentExtraArgs),
				SessionResume:     stringFlag(cmd, "agent-session-resume", sessionResume),
				SessionCommand:    stringSliceFlag(cmd, "session-command", sessionCommand),
				Features:          selectedFeatureFlag(cmd, featureNames, extraFeatures),
				FeatureOptions:    featureOptions,
				Capabilities:      capabilities,
			})
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().StringVar(&image, "image", "", "Container image (triggers a recreate)")
	cmd.Flags().StringVar(&cpus, "cpus", "", "CPU limit (passed to nerdctl --cpus; triggers a recreate)")
	cmd.Flags().StringVar(&memory, "memory", "", "Memory limit (shared removes the box cap; e.g. 2g sets one; triggers a recreate)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Toggle the fs.workdir.write capability (true → /workdir read-only; triggers a recreate)")
	cmd.Flags().BoolVar(&unconfinedNetwork, "unconfined-network", false, "Toggle the network confinement layer (true → open egress, no allow-list/L7 proxy via openshell --mode=process; false → re-confine). Other layers stay on. Triggers a recreate.")
	cmd.Flags().BoolVar(&privileged, "privileged", false, privilegedFlagHelp+" box set saves the setting; apply it with box recreate.")
	cmd.Flags().StringArrayVar(&sessionCommand, "session-command", nil, "Replace agentless session_command argv (repeat for each argument; --session-command= clears it). Applied on next session start; non-secret only.")
	cmd.Flags().StringSliceVar(&agentExtraArgs, "agent-extra-args", nil, "Replace persisted agent_extra_args (repeatable / comma-separated). Applied when yoloctl next starts the managed agent session; non-secret only.")
	cmd.Flags().StringVar(&sessionResume, "agent-session-resume", "", "Set the conversation policy: last, never or manual (--agent-session-resume= unsets it). Applied on the next session start.")
	cmd.Flags().StringArrayVar(&extraFeatures, "feature", nil, "Replace explicit features with these names or OCI references (repeatable)")
	cmd.Flags().StringArrayVar(&featureOptions, "feature-option", nil, "Set OCI Feature option FEATURE.OPTION=VALUE (repeatable; non-secret)")
	cmd.Flags().StringSliceVar(&featureNames, "features", nil, "Replace the box's explicit features (repeatable / comma-separated). Changing the effective feature set re-composes the runtime image and triggers a recreate.")
	cmd.Flags().StringSliceVar(&capabilities, "capability", nil, "Append a positive capability grant (repeatable; triggers a restart), e.g. network.github or fs.pty.allocate")
	return cmd
}

// setOptions wraps the optional set fields. nil pointers mean "the
// user didn't pass this flag"; non-nil means "apply this value".
// Capabilities is a slice (additive append) rather than a pointer —
// empty slice = no append.
type setOptions struct {
	Image             *string
	CPUs              *string
	Memory            *string
	ReadOnly          *bool
	UnconfinedNetwork *bool
	Privileged        *bool
	AgentExtraArgs    *[]string
	SessionResume     *string
	SessionCommand    *[]string
	Features          *[]string
	FeatureOptions    []string
	Capabilities      []string
}

// empty reports whether no field flag was passed at all.
func (o setOptions) empty() bool {
	return o.Image == nil && o.CPUs == nil && o.Memory == nil && o.ReadOnly == nil &&
		o.UnconfinedNetwork == nil && o.Privileged == nil && o.AgentExtraArgs == nil && o.SessionResume == nil && o.SessionCommand == nil && o.Features == nil &&
		len(o.Capabilities) == 0 && len(o.FeatureOptions) == 0
}

func selectedFeatureFlag(cmd *cobra.Command, plural, singular []string) *[]string {
	if !cmd.Flags().Changed("features") && !cmd.Flags().Changed("feature") {
		return nil
	}
	values := append(append([]string(nil), plural...), singular...)
	return &values
}

func stringFlag(cmd *cobra.Command, name, val string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	return &val
}

func boolFlag(cmd *cobra.Command, name string, val bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	return &val
}

func stringSliceFlag(cmd *cobra.Command, name string, val []string) *[]string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	return &val
}

func runSet(ctx context.Context, out io.Writer, spaceName, name string, opts setOptions) error {
	lock, err := lockSessionBoxFn(spaceName, name)
	if err != nil {
		return fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer lock.Release()

	client := container.New(spaceName)
	cfg, err := loadManagedAgentbox(spaceName, name)
	if err != nil {
		return err
	}

	if opts.empty() {
		_, _ = fmt.Fprintln(out, "no fields specified; nothing to do (use `box set --help` to list available fields)")
		return nil
	}

	mutated, err := applySetOptions(cfg, opts)
	if err != nil {
		return err
	}

	if !mutated {
		// The manifest already holds what was asked, but it can be ahead of
		// the runtime: a set that died between its save and its reconcile
		// leaves exactly that. Converge anyway, so repeating a set is how you
		// recover from one; an in-sync box is reported as such.
		_, _ = fmt.Fprintf(out, "box %s: manifest already holds the requested values\n", name)
		return setReconcile(ctx, lock, out, client, cfg, name)
	}

	if err := requireResolvableManifest(ctx, cfg); err != nil {
		return err
	}
	if _, err := canonicalizeUserMountSources(cfg); err != nil {
		return err
	}
	if err := rejectAccountRootBinds(cfg); err != nil {
		return err
	}
	if err := warmBoxBeforeEffects(defaultCreationContext(ctx, cfg), client, cfg, true); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	warnBoxAuthority(cfg)

	return setReconcile(ctx, lock, out, client, cfg, name)
}

// applySetOptions resolves the desired settings without saving or applying.
// Warm preparation and locked apply use this same flag semantics.
func applySetOptions(cfg *agentbox.Config, opts setOptions) (bool, error) {
	// mutated tracks whether the manifest really changes: every field is
	// compared, so a flag repeating the stored value is not a rewrite.
	mutated := false
	if opts.Image != nil && cfg.Image != *opts.Image {
		cfg.Image = *opts.Image
		mutated = true
	}
	if opts.CPUs != nil && cfg.Resources.CPUs != *opts.CPUs {
		cfg.Resources.CPUs = *opts.CPUs
		mutated = true
	}
	if opts.Memory != nil && cfg.Resources.Memory != *opts.Memory {
		cfg.Resources.Memory = *opts.Memory
		mutated = true
	}
	if opts.ReadOnly != nil {
		// --read-only toggles the fs.workdir.write capability: true removes
		// it (/workdir read-only), false adds it (read-write).
		wd := capabilities.WorkdirWriteCapability
		had := slices.Contains(cfg.Capabilities, wd)
		if *opts.ReadOnly && had {
			cfg.Capabilities = slices.DeleteFunc(cfg.Capabilities, func(c string) bool { return c == wd })
			mutated = true
		} else if !*opts.ReadOnly && !had {
			cfg.Capabilities = append(cfg.Capabilities, wd)
			mutated = true
		}
	}
	if opts.UnconfinedNetwork != nil && cfg.UnconfinedNetwork != *opts.UnconfinedNetwork {
		// Toggle the network confinement layer (openshell --mode). The change
		// flips the yoloctl.network_unconfined label, which the reconcile diff
		// treats as create-only → recreate.
		cfg.UnconfinedNetwork = *opts.UnconfinedNetwork
		mutated = true
	}
	if opts.Privileged != nil && cfg.Privileged != *opts.Privileged {
		// A launch-mode change: the reconcile refuses it, and only an
		// explicit box recreate applies the saved setting.
		cfg.Privileged = *opts.Privileged
		mutated = true
	}
	if opts.AgentExtraArgs != nil && !slices.Equal(cfg.AgentExtraArgs, *opts.AgentExtraArgs) {
		cfg.AgentExtraArgs = append([]string(nil), (*opts.AgentExtraArgs)...)
		mutated = true
	}
	if opts.SessionResume != nil && cfg.ResumePolicy() != *opts.SessionResume {
		cfg.AgentSession = nil
		if *opts.SessionResume != "" {
			cfg.AgentSession = &agentbox.AgentSession{Resume: *opts.SessionResume}
		}
		mutated = true
	}
	if opts.SessionCommand != nil {
		command := *opts.SessionCommand
		if slices.Equal(command, []string{""}) {
			command = nil
		}
		if !slices.Equal(cfg.SessionCommand, command) {
			cfg.SessionCommand = slices.Clone(command)
			mutated = true
		}
	}
	if opts.Features != nil && !slices.Equal(cfg.Features, *opts.Features) {
		// Replace the box's explicit features. The reconcile recomputes the
		// effective set → composes a new runtime image → recreates.
		cfg.Features = append([]string(nil), (*opts.Features)...)
		mutated = true
	}
	if opts.Features != nil || len(opts.FeatureOptions) > 0 {
		options, err := ocifeature.ApplyOptions(cfg.Features, cfg.FeatureOptions, opts.FeatureOptions)
		if err != nil {
			return false, err
		}
		cfg.FeatureOptions = options
		mutated = true
	}
	for _, capName := range opts.Capabilities {
		// Dedupe against existing capabilities — additive append, like the
		// old --allow behavior. Remove via `box edit`.
		if slices.Contains(cfg.Capabilities, capName) {
			continue
		}
		cfg.Capabilities = append(cfg.Capabilities, capName)
		mutated = true
	}
	return mutated, nil
}

// setReconcile is `box set`'s reconcile tail; a variable so tests can drive
// runSet without a live VM.
var setReconcile = reconcileAndPrint

// managedReconcileRuntime builds a managed reconcile's inspector and
// session-aware applier, and preflightManagedReconcile is its VM-side
// preflight; tests substitute the VM.
var (
	managedReconcileRuntime = func(c *container.Client, notice io.Writer) (agentbox.StateInspector, agentbox.Applier) {
		return agentbox.LiveInspector{Client: c}, newManagedLiveApplier(c, notice)
	}
	preflightManagedReconcile = preflightAgentboxRuntime
)

// reconcileAndPrint runs Reconcile against the live VM via
// LiveInspector + LiveApplier and prints the result. Pulled out so
// edit / set / future set-shape commands share the same finalize
// step. The caller holds the box lock.
func reconcileAndPrint(ctx context.Context, lock *agentsession.BoxLock, out io.Writer, client *container.Client, cfg *agentbox.Config, name string) error {
	result, err := reconcileManaged(ctx, lock, client, cfg, out)
	if err != nil {
		return savedDeclarationError(cfg, err)
	}
	return printReconcileResult(out, name, result)
}

// reconcileManaged preflights and reconciles a managed box whose box lock
// the caller holds, inside its native space lock. A container this call
// created or recreated is the same proof explicit box recreate records: the
// managed applier stopped the old holder and the old container is gone, so a
// known launched conversation closes and the next start resumes it. A failed
// or absent replacement proves nothing; the next start judges the record.
func reconcileManaged(ctx context.Context, lock *agentsession.BoxLock, client *container.Client, cfg *agentbox.Config, notice io.Writer) (agentbox.ReconcileResult, error) {
	if err := preflightManagedReconcile(ctx, client, cfg, true); err != nil {
		return agentbox.ReconcileResult{}, err
	}
	tracking, _, err := sessionTrackingFor(cfg)
	if err != nil {
		return agentbox.ReconcileResult{}, err
	}
	inspector, applier := managedReconcileRuntime(client, notice)
	// A restart or recreate proves the launch ended at its session cleanup,
	// before the container operation; record it there, so a failure after
	// the cleanup cannot leave the record claiming a launch whose evidence is
	// gone.
	if m, ok := applier.(managedApplier); ok && tracking != nil {
		m.onSessionEnded = func(detail string) error { return recordSessionEnded(lock, tracking, detail) }
		applier = m
	}
	result, err := agentbox.Reconcile(ctx, cfg, inspector, applier)
	if err != nil {
		return result, err
	}
	if err := applySessionStart(lock, tracking, result.ContainerCreated, false, notice); err != nil {
		return result, fmt.Errorf("container of box %s was replaced, but recording that for its conversation failed: %w", cfg.Name, err)
	}
	return result, nil
}

// mutateAndReconcile is the standard YAML-driven mutation flow used
// by every CLI command that conceptually means "change one field in
// the agentbox YAML, then apply": port forward / unforward today.
//
// Sequence: box lock → loadManagedAgentbox → caller's mutate
// callback → requireResolvableManifest → Save (which Validates) →
// preflight → Reconcile. Returns the ReconcileResult so callers can
// render strategy-specific output. A port change cannot introduce a
// capability or feature name, but the manifest it edits may already
// fail to resolve; the guard keeps every manifest write under one rule.
// Caller is responsible for translating known agentbox errors (e.g.
// port.ErrPortInUse propagated from LiveApplier) into user errors.
func mutateAndReconcile(
	ctx context.Context,
	spaceName, name string,
	mutate func(*agentbox.Config) error,
) (agentbox.ReconcileResult, error) {
	lock, err := lockSessionBoxFn(spaceName, name)
	if err != nil {
		return agentbox.ReconcileResult{}, fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer lock.Release()

	client := container.New(spaceName)
	cfg, err := loadManagedAgentbox(spaceName, name)
	if err != nil {
		return agentbox.ReconcileResult{}, err
	}
	if err := mutate(cfg); err != nil {
		return agentbox.ReconcileResult{}, err
	}
	if err := requireResolvableManifest(ctx, cfg); err != nil {
		return agentbox.ReconcileResult{}, err
	}
	if _, err := canonicalizeUserMountSources(cfg); err != nil {
		return agentbox.ReconcileResult{}, err
	}
	if err := rejectAccountRootBinds(cfg); err != nil {
		return agentbox.ReconcileResult{}, err
	}
	if err := warmBoxBeforeEffects(defaultCreationContext(ctx, cfg), client, cfg, true); err != nil {
		return agentbox.ReconcileResult{}, err
	}
	if err := cfg.Save(); err != nil {
		return agentbox.ReconcileResult{}, fmt.Errorf("save config: %w", err)
	}
	result, err := reconcileManaged(ctx, lock, client, cfg, io.Discard)
	return result, savedDeclarationError(cfg, err)
}

// printReconcileResult writes the apply summary in the same shape
// as the dry-run plan, but with verbs that reflect what actually
// happened.
func printReconcileResult(out io.Writer, name string, r agentbox.ReconcileResult) error {
	switch {
	case r.ContainerCreated:
		_, _ = fmt.Fprintf(out, "box %s: container created\n", name)
	case r.ContainerRecreated:
		_, _ = fmt.Fprintf(out, "box %s: container recreated\n", name)
	case r.ContainerRestarted:
		_, _ = fmt.Fprintf(out, "box %s: container restarted (policy reloaded)\n", name)
	default:
		if !r.HasChanges() {
			_, _ = fmt.Fprintf(out, "box %s: already in sync\n", name)
			return nil
		}
	}
	for _, pf := range r.PortForwardsRemoved {
		_, _ = fmt.Fprintf(out, "  - port forward %d:%d removed\n", pf.Published, pf.Target)
	}
	for _, pf := range r.PortForwardsAdded {
		_, _ = fmt.Fprintf(out, "  + port forward %d:%d added\n", pf.Published, pf.Target)
	}
	return nil
}
