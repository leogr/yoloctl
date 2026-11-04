package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// newAgentCmd is the parent for agent-tooling subcommands:
// `versions` (query distribution metadata) and `update` (in-place
// refresh inside running boxes of a given agent).
func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agents (catalog) and agent CLI tooling (Antigravity, Claude Code, Codex, Gemini, OpenCode, Pi)",
	}
	// Catalog verbs (ls/show/copy/edit/destroy) over the
	// built-in + custom agent registry, alongside the existing tooling verbs.
	cmd.AddCommand(agentCatalogCommands()...)
	cmd.AddCommand(newAgentVersionsCmd(), newAgentStatusCmd(), newAgentUpdateCmd())
	return cmd
}

// newAgentUpdateCmd is `yoloctl agent update AGENT` — runs
// the configured distribution installer inside every running box whose
// `yoloctl.agent` label equals AGENT, refreshing the installed
// agent CLI to its latest published version.
//
// PROFILE is ALWAYS an agent profile name (claude, codex, ...), never
// an agentbox name. To restrict the update to a single agentbox, pass
// --agentbox AGENTBOX — that agentbox must still carry the PROFILE label.
// Self-consistent: the positional argument has one meaning regardless
// of how many matching agentboxes exist.
//
// Pre-flight defensive check: before each per-agentbox update, yoloctl
// verifies the agent process (profile.Command) is actually running
// inside the container. If it isn't, the user is prompted — they may
// have stopped the agent or be running something else in there.
// --force skips the check entirely; non-interactive shells without
// --force return an error rather than silently proceed.
//
// The update runs via `nerdctl exec --user sandbox`, which lands in
// the outer container netns where eth0 has direct internet — bypasses
// openshell-sandbox's L7 proxy + the inner-netns iptables fence.
// Intentional for tool maintenance: the in-sandbox policy still
// applies to whatever the agent runs *after* the update, but the
// maintenance op itself is not subject to the egress allow-list.
// Agent workloads must still use the OpenShell launch path.
func newAgentUpdateCmd() *cobra.Command {
	var (
		spaceName    string
		agentboxName string
		force        bool
	)
	cmd := &cobra.Command{
		Use:   "update AGENT",
		Short: "Refresh the agent CLI inside every running box of a given agent",
		Long: `Installs the latest configured npm package or verified native release as the sandbox user inside
every box carrying the ` + "`yoloctl.agent=<AGENT>`" + ` label.

  yoloctl agent update claude                      # every claude box
  yoloctl agent update claude --agentbox tester    # only one
  yoloctl agent update codex --space prod-untrusted

AGENT is an agent name (agy, claude, codex, gemini, opencode, pi), NOT a box name.
To target a single box, pass --agentbox BOX — that box must have been created
with --agent AGENT (so its yoloctl.agent label matches). Boxes without an agent
label are never matched.

Pre-flight check: yoloctl verifies the agent process is actually
running inside each target box before updating. If it isn't, you
are prompted to confirm; --force skips the prompt. Useful when you've
killed the agent and are running something else in the box.

Run ` + "`yoloctl agent versions`" + ` first to see what's available, then
` + "`box exec BOX -- claude --version`" + ` after to confirm the new
version is in place. To refresh OS/runtime layers, run
` + "`yoloctl image refresh`" + `, then ` + "`yoloctl box reconcile BOX --rebuild`" + `
for each affected box; existing composed images keep the old base.

The update downloads from the configured distribution via the outer container netns
(which has direct internet), so it works regardless of the agent's
openshell-sandbox policy.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileName := args[0]
			profile, lookupErr := lookupAgentProfile(profileName)
			if lookupErr != nil {
				if IsUserError(lookupErr) {
					// Override the generic "unknown profile" wording with
					// agent-update-specific guidance (positional is always a
					// profile, not an agentbox).
					return asUserError(fmt.Errorf(
						"unknown agent %q; available: %v "+
							"(AGENT must be an agent name, not a box name — "+
							"use --agentbox BOX to restrict to a single box)",
						profileName, agentProfileNames()))
				}
				return lookupErr
			}
			if profile.Distribution() == "" {
				return asUserError(fmt.Errorf(
					"agent %q has no managed distribution configured",
					profileName))
			}
			pkg := profile.Distribution()
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if agentboxName != "" {
				if err := validateAgentboxName(agentboxName); err != nil {
					return err
				}
			}

			c := container.New(spaceName)
			targets, err := selectAgentUpdateTargets(cmd.Context(), c, profileName, agentboxName, spaceName)
			if err != nil {
				return err
			}

			errw := cmd.ErrOrStderr()
			_, _ = fmt.Fprintf(errw, "updating %s in %d box(es) (latest release)\n", pkg, len(targets))
			for i, name := range targets {
				_, _ = fmt.Fprintf(errw, "[%d/%d] %s\n", i+1, len(targets), name)
				if !force {
					proceed, err := preflightAgentRunning(cmd, spaceName, name, profile.Command)
					if err != nil {
						return err
					}
					if !proceed {
						_, _ = fmt.Fprintf(errw, "  skipped (agent not running, no confirmation)\n")
						continue
					}
				}
				_, _ = fmt.Fprintf(errw, "  updating %s...\n", pkg)
				if err := installAgentLatest(cmd.Context(), c, name, profile); err != nil {
					return fmt.Errorf("update %s in %s: %w", pkg, name, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the boxes")
	cmd.Flags().StringVar(&agentboxName, "agentbox", "", "Restrict the update to one box (must have yoloctl.agent=AGENT label)")
	cmd.Flags().BoolVar(&force, "force", false, "Skip the pre-flight check that verifies the agent process is running in each target")
	return cmd
}

// selectAgentUpdateTargets resolves the list of agentbox names the
// update will iterate over. Bulk mode (agentboxName == "") returns
// every container in the space whose yoloctl.agent label matches
// profileName, sorted by name. Single mode (agentboxName != "")
// returns just that one, but only after verifying the agentbox exists
// AND has the matching agent label — otherwise the user gets a clear
// error explaining the mismatch rather than a silent no-op.
func selectAgentUpdateTargets(ctx context.Context, c *container.Client, profileName, agentboxName, spaceName string) ([]string, error) {
	containers, err := c.List(ctx, agentbox.LabelAgentbox)
	if err != nil {
		return nil, fmt.Errorf("list boxes: %w", err)
	}
	if agentboxName != "" {
		var found *container.Container
		for i, ctr := range containers {
			if ctr.Name == agentboxName {
				found = &containers[i]
				break
			}
		}
		if found == nil {
			return nil, asUserError(fmt.Errorf(
				"box %q not found in space %s", agentboxName, spaceName))
		}
		actualAgent := parseLabels(found.Labels)[agentbox.LabelAgent]
		if actualAgent != profileName {
			if actualAgent == "" {
				return nil, asUserError(fmt.Errorf(
					"box %q has no yoloctl.agent label (created without --agent or by an older build); "+
						"recreate it with `box create %s --agent %s` to participate in `agent update`",
					agentboxName, agentboxName, profileName))
			}
			return nil, asUserError(fmt.Errorf(
				"box %q has yoloctl.agent=%s, not %s — refusing to update with the %s package",
				agentboxName, actualAgent, profileName, profileName))
		}
		return []string{agentboxName}, nil
	}
	var targets []string
	for _, ctr := range containers {
		if parseLabels(ctr.Labels)[agentbox.LabelAgent] == profileName {
			targets = append(targets, ctr.Name)
		}
	}
	if len(targets) == 0 {
		return nil, asUserError(fmt.Errorf(
			"no boxes with yoloctl.agent=%s in space %s "+
				"(create one with `yoloctl box create <name> --agent %s`)",
			profileName, spaceName, profileName))
	}
	sort.Strings(targets)
	return targets, nil
}

// preflightAgentRunning is the defensive check that verifies the
// agent's process is currently running inside the named agentbox.
// Returns true when the update should proceed:
//   - agent running → true, no prompt
//   - agent not running, interactive shell, user says yes → true
//   - agent not running, interactive shell, user says no → false
//
// Returns an error (and the caller aborts) when:
//   - agent not running, non-interactive shell, --force not passed
//   - the pgrep check itself errored AND wasn't a plain "not found"
//     exit (handled as best-effort: we proceed without prompting).
//
// The check is best-effort: pgrep missing, exec exit codes >=2, or
// limactl errors all yield a warning + proceed, on the basis that a
// noisy half-broken check shouldn't block a maintenance operation
// the user explicitly asked for.
func preflightAgentRunning(cmd *cobra.Command, spaceName, agentboxName, agentCommand string) (bool, error) {
	errw := cmd.ErrOrStderr()
	running, err := agentProcessRunning(cmd.Context(), spaceName, agentboxName, agentCommand)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "  warning: pre-flight check could not run (%v); proceeding\n", err)
		return true, nil
	}
	if running {
		return true, nil
	}
	in := cmd.InOrStdin()
	if !isInteractive(in) {
		return false, asUserError(fmt.Errorf(
			"no %q process running in box %q and stdin is non-interactive; "+
				"rerun with --force to skip the pre-flight check or to update anyway",
			agentCommand, agentboxName))
	}
	prompt := fmt.Sprintf(
		"  pre-flight: no %q process running in box %q; another binary may be in use here.\n"+
			"  update anyway? [y/N]: ",
		agentCommand, agentboxName)
	return promptYesNo(prompt, in, errw)
}

// agentProcessRunning returns true when `pgrep -f <agentCommand>`
// inside the agentbox container reports at least one match.
//
// Implementation uses os/exec directly rather than container.Client
// because we need access to the exit code (1 = nothing matched =
// answer is "no", which is a valid result, not an error). Output of
// pgrep is discarded — we only care about the exit code.
//
// agentCommand is the agent profile's Command (e.g., "claude" or
// "codex"). pgrep -f matches against /proc/<pid>/cmdline so it works
// for npm-shipped JS shims that show up as `node /path/to/claude`.
// False positives (vim editing claude-notes.txt) are possible; that's
// acceptable for a "skip the prompt" pre-flight.
func agentProcessRunning(ctx context.Context, spaceName, agentboxName, agentCommand string) (bool, error) {
	shCmd := vmhost.ShellCommand(ctx, spaceName,
		"sudo", "nerdctl", "exec", "--user", "sandbox", agentboxName,
		"pgrep", "-f", agentCommand,
	)
	shCmd.Stdout = io.Discard
	shCmd.Stderr = io.Discard
	err := shCmd.Run()
	return interpretPgrepExit(err)
}

// interpretPgrepExit maps the os/exec error from `pgrep -f` to our
// three-valued result:
//   - (true, nil)  → pgrep exited 0: at least one match.
//   - (false, nil) → pgrep exited 1: no match. NOT an error from the
//     caller's POV — the pre-flight question was answered.
//   - (false, err) → anything else: pgrep itself failed (missing
//     binary, bad args, exec couldn't run). Treated as inconclusive;
//     the caller proceeds with a warning.
func interpretPgrepExit(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("pgrep exit code %d", exitErr.ExitCode())
	}
	return false, err
}

// promptYesNo writes prompt to out, reads one line from in, returns
// true for "y"/"yes" (case-insensitive). Anything else (including
// empty input) returns false — matches the "[y/N]" convention where
// pressing Enter is "no". Pure I/O: the caller is responsible for
// deciding whether the input is interactive enough to prompt at all
// (see isInteractive + preflightAgentRunning).
func promptYesNo(prompt string, in io.Reader, out io.Writer) (bool, error) {
	_, _ = fmt.Fprint(out, prompt)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

// isInteractive reports whether r is a terminal, so that prompting the user
// is meaningful. A character device is not enough: /dev/null and /dev/zero
// are not terminals. Returns false for io.Pipe, bytes.Reader,
// strings.Reader, and any non-*os.File: those are tests / CI / shell
// redirection.
func isInteractive(r io.Reader) bool { return stdinIsTerminal(r) }

func newAgentVersionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "versions",
		Short: "Print the latest available version of each managed agent CLI",
		Long: `Queries npm or the configured native release manifest for each registered
agent and prints the latest version and publish date (when available).

The version inside any given built box image is whatever was
` + "`latest`" + ` when its feature-composed image was built or rebuilt.
Compare via:

  yoloctl box exec <name> -- claude --version

When they drift, run ` + "`yoloctl agent update <agent>`" + ` to refresh
in place across every matching running box, or
` + "`yoloctl box reconcile <name> --rebuild`" + ` to rebuild that box's
feature-composed image and recreate it.

Network call timeout is 5s per package; per-package failures log to
stderr but don't fail the command — useful information is still
emitted for the packages that did respond.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentVersions(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runAgentVersions iterates every installed agent profile, fetches
// each one's latest distribution version, and prints a table.
// Profiles without a managed distribution are skipped (the table just doesn't
// list them). Goes through agentprofile.All so user-added profiles
// participate identically to the built-ins.
func runAgentVersions(ctx context.Context, out, errw io.Writer) error {
	profiles, err := agentprofile.All()
	if err != nil {
		return err
	}
	// Stable order so the table doesn't shuffle between runs.
	names := make([]string, 0, len(profiles))
	for n, p := range profiles {
		if p.Distribution() == "" {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "AGENT\tPACKAGE\tLATEST\tPUBLISHED")
	for _, n := range names {
		profile := profiles[n]
		pkg := profile.Distribution()
		info, err := fetchAgentLatest(ctx, &profile)
		if err != nil {
			_, _ = fmt.Fprintf(errw, "warning: query %s: %v\n", pkg, err)
			_, _ = fmt.Fprintf(tw, "%s\t%s\t(error)\t-\n", n, pkg)
			continue
		}
		published := "-"
		if !info.Published.IsZero() {
			published = info.Published.Format("2006-01-02")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n, pkg, info.Latest, published)
	}
	return tw.Flush()
}

// agentLatestInfo is the common version view of npm/native manifests. Native
// manifests currently publish no timestamp.
type agentLatestInfo struct {
	Latest    string
	Published time.Time
}

// fetchNpmLatest hits registry.npmjs.org for a single package. Uses
// the full packument endpoint (/<pkg>) rather than /<pkg>/latest
// because /latest doesn't return the per-version `time` map needed
// for the published date.
func fetchNpmLatest(ctx context.Context, pkg string) (*agentLatestInfo, error) {
	url := "https://registry.npmjs.org/" + pkg

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry status %s", resp.Status)
	}
	var packument struct {
		DistTags map[string]string    `json:"dist-tags"`
		Time     map[string]time.Time `json:"time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&packument); err != nil {
		return nil, fmt.Errorf("parse registry response: %w", err)
	}
	latest := packument.DistTags["latest"]
	if latest == "" {
		return nil, fmt.Errorf("registry response missing `latest` dist-tag")
	}
	info := &agentLatestInfo{Latest: latest}
	if t, ok := packument.Time[latest]; ok {
		info.Published = t
	}
	return info, nil
}
