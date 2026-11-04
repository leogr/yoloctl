package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

const npmLatestCacheTTL = time.Hour

func newAgentStatusCmd() *cobra.Command {
	var (
		spaceName     string
		allSpaces     bool
		noCheckLatest bool
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show installed-vs-latest agent CLI versions in running boxes",
		Long: `Reports the agent CLI version installed inside each running
box that was created with ` + "`--agent`" + `, then compares it with the
latest npm or native release for that agent.

Latest-version checks use a one-hour cache under
` + "`~/.cache/yoloctl/npm-latest/`" + `. Pass --no-check-latest for a fast
offline view that only probes installed versions.

  yoloctl agent status
  yoloctl agent status --space prod-untrusted
  yoloctl agent status --all-spaces
  yoloctl agent status --no-check-latest
  yoloctl agent status --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allSpaces && flagChanged(cmd, "space") {
				return asUserError(fmt.Errorf("--space and --all-spaces are mutually exclusive"))
			}
			var rows []agentboxRow
			if allSpaces {
				var err error
				rows, err = listAgentboxesAcrossSpaces(cmd.Context(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}
			} else {
				if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
					return err
				}
				containers, err := container.New(spaceName).List(cmd.Context(), agentbox.LabelAgentbox)
				if err != nil {
					return err
				}
				rows = make([]agentboxRow, 0, len(containers))
				for _, c := range containers {
					rows = append(rows, agentboxRow{Space: spaceName, Container: c})
				}
			}
			statusRows := buildAgentStatusRows(cmd.Context(), rows, agentStatusOptions{
				CheckLatest: !noCheckLatest,
				LatestTTL:   npmLatestCacheTTL,
				Stderr:      cmd.ErrOrStderr(),
			})
			if asJSON {
				return writeAgentStatusJSON(cmd.OutOrStdout(), statusRows)
			}
			return printAgentStatus(cmd.OutOrStdout(), statusRows, allSpaces)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space to query (incompatible with --all-spaces)")
	cmd.Flags().BoolVar(&allSpaces, "all-spaces", false, "Walk every Running space and merge the results")
	cmd.Flags().BoolVar(&noCheckLatest, "no-check-latest", false, "Skip registry-latest checks and report installed versions only")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the table view")
	return cmd
}

type agentStatusOptions struct {
	CheckLatest bool
	LatestTTL   time.Duration
	Stderr      io.Writer
}

type agentStatusRow struct {
	Space            string `json:"space,omitempty"`
	Name             string `json:"name"`
	RuntimeStatus    string `json:"runtime_status"`
	Agent            string `json:"agent"`
	Package          string `json:"package,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	Drift            string `json:"drift"`
	Error            string `json:"error,omitempty"`
}

func buildAgentStatusRows(ctx context.Context, rows []agentboxRow, opts agentStatusOptions) []agentStatusRow {
	if opts.LatestTTL <= 0 {
		opts.LatestTTL = npmLatestCacheTTL
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	latestByPackage := map[string]*agentLatestInfo{}
	latestErrByPackage := map[string]error{}
	out := make([]agentStatusRow, 0, len(rows))
	for _, r := range rows {
		if !containerStatusRunning(r.Container.Status) {
			continue
		}
		labels := parseLabels(r.Container.Labels)
		agentName := labels[agentbox.LabelAgent]
		if agentName == "" {
			continue
		}
		row := agentStatusRow{
			Space:         r.Space,
			Name:          r.Container.Name,
			RuntimeStatus: r.Container.Status,
			Agent:         agentName,
			Drift:         "unknown",
		}
		profile, err := agentprofile.Lookup(agentName)
		if err != nil {
			row.Error = fmt.Sprintf("load agent: %v", err)
			_, _ = fmt.Fprintf(opts.Stderr, "warning: %s/%s: %v\n", r.Space, r.Container.Name, row.Error)
			out = append(out, row)
			continue
		}
		row.Package = profile.Distribution()
		if profile.Distribution() == "" {
			row.Error = "agent has no managed distribution"
			out = append(out, row)
			continue
		}
		c := container.New(r.Space)
		installed, err := fetchInstalledAgentVersion(ctx, c, r.Container.Name, profile)
		if err != nil {
			row.Error = fmt.Sprintf("read installed version: %v", err)
			_, _ = fmt.Fprintf(opts.Stderr,
				"warning: %s/%s: read installed %s version: %v\n",
				r.Space, r.Container.Name, profile.Distribution(), err)
		} else {
			row.InstalledVersion = installed
		}
		if opts.CheckLatest {
			latest, ok := latestByPackage[profile.Distribution()]
			latestErr, hadErr := latestErrByPackage[profile.Distribution()]
			if !ok && !hadErr {
				latest, latestErr = fetchAgentLatestCached(ctx, profile, opts.LatestTTL)
				if latestErr != nil {
					latestErrByPackage[profile.Distribution()] = latestErr
					_, _ = fmt.Fprintf(opts.Stderr,
						"warning: query %s: %v\n", profile.Distribution(), latestErr)
				} else {
					latestByPackage[profile.Distribution()] = latest
				}
			}
			if latest != nil {
				row.LatestVersion = latest.Latest
			} else if latestErr != nil && row.Error == "" {
				row.Error = fmt.Sprintf("query latest: %v", latestErr)
			}
		}
		row.Drift = agentVersionDrift(row.InstalledVersion, row.LatestVersion)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Space != out[j].Space {
			return out[i].Space < out[j].Space
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func printAgentStatus(w io.Writer, rows []agentStatusRow, withSpace bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if withSpace {
		_, _ = fmt.Fprintln(tw, "SPACE\tNAME\tAGENT\tPACKAGE\tINSTALLED\tLATEST\tDRIFT")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tAGENT\tPACKAGE\tINSTALLED\tLATEST\tDRIFT")
	}
	for _, r := range rows {
		pkg := dash(r.Package)
		installed := dash(r.InstalledVersion)
		latest := dash(r.LatestVersion)
		if withSpace {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Space, r.Name, r.Agent, pkg, installed, latest, r.Drift)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Name, r.Agent, pkg, installed, latest, r.Drift)
		}
	}
	return tw.Flush()
}

func writeAgentStatusJSON(w io.Writer, rows []agentStatusRow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func containerStatusRunning(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == "running" || strings.HasPrefix(s, "running ") ||
		s == "up" || strings.HasPrefix(s, "up ")
}

type cachedNpmLatest struct {
	Package   string    `json:"package"`
	Latest    string    `json:"latest"`
	Published time.Time `json:"published,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

func readNpmLatestCache(pkg string, ttl time.Duration, now time.Time) (*agentLatestInfo, bool) {
	path, err := layout.NpmLatestCachePath(pkg)
	if err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var cached cachedNpmLatest
	if err := json.Unmarshal(raw, &cached); err != nil {
		return nil, false
	}
	if cached.Package != pkg || cached.Latest == "" || cached.FetchedAt.IsZero() {
		return nil, false
	}
	if now.Sub(cached.FetchedAt) > ttl {
		return nil, false
	}
	return &agentLatestInfo{Latest: cached.Latest, Published: cached.Published}, true
}

func writeNpmLatestCache(pkg string, info *agentLatestInfo, fetchedAt time.Time) error {
	path, err := layout.NpmLatestCachePath(pkg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cachedNpmLatest{
		Package:   pkg,
		Latest:    info.Latest,
		Published: info.Published,
		FetchedAt: fetchedAt,
	}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func agentVersionDrift(installed, latest string) string {
	if installed == "" || latest == "" {
		return "unknown"
	}
	if installed == latest {
		return "up to date"
	}
	iv, okI := parseSemverCore(installed)
	lv, okL := parseSemverCore(latest)
	if !okI || !okL {
		return "unknown"
	}
	if iv.greaterThan(lv) {
		return "ahead"
	}
	if iv.major == lv.major && iv.minor == lv.minor && iv.patch < lv.patch {
		behind := lv.patch - iv.patch
		if behind == 1 {
			return "1 patch behind"
		}
		return fmt.Sprintf("%d patches behind", behind)
	}
	return "behind"
}

type semverCore struct {
	major int
	minor int
	patch int
}

func parseSemverCore(s string) (semverCore, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "v"))
	if s == "" {
		return semverCore{}, false
	}
	for _, cut := range []string{"-", "+"} {
		if idx := strings.Index(s, cut); idx >= 0 {
			s = s[:idx]
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semverCore{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return semverCore{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return semverCore{}, false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return semverCore{}, false
	}
	if major < 0 || minor < 0 || patch < 0 {
		return semverCore{}, false
	}
	return semverCore{major: major, minor: minor, patch: patch}, true
}

func (v semverCore) greaterThan(other semverCore) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch > other.patch
}

func loadAgentboxShowMetadata(ctx context.Context, c *container.Client, d *container.Details, space, name string, errw io.Writer) agentboxShowMetadata {
	meta := agentboxShowMetadata{
		AgentName: d.Labels[agentbox.LabelAgent],
		Runtime:   agentbox.ClassifyRuntime(d),
	}
	cfg, err := agentbox.Load(space, name)
	if err == nil {
		if cfg.HasAgent() {
			meta.AgentName = cfg.Agent
		}
		meta.Policy = policyDisplayFromConfig(cfg)
		meta.WorkdirMode = cfg.Workdir.Mode
		meta.Workdir = cfg.Workdir.MountSource()
		meta.WorkdirContainer = cfg.Workdir.ContainerPath()
		meta.UnconfinedNetwork = cfg.UnconfinedNetwork
		meta.Privileged = cfg.Privileged
	} else if !errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintf(errw, "warning: box manifest unavailable: %v\n", err)
	}
	// "none" is the generic-shell sentinel, never a profile — also guard the
	// label-derived AgentName (a generic-shell box carries yoloctl.agent=none).
	if !hasRealAgent(meta.AgentName) {
		return meta
	}
	profile, err := agentprofile.Lookup(meta.AgentName)
	if err != nil {
		meta.AgentVersionNote = "version unknown"
		_, _ = fmt.Fprintf(errw, "warning: load agent %s: %v\n", meta.AgentName, err)
		return meta
	}
	meta.AgentPackage = profile.Distribution()
	if profile.Distribution() == "" {
		return meta
	}
	if !d.Running {
		meta.AgentVersionNote = "stopped"
		return meta
	}
	version, err := fetchInstalledAgentVersion(ctx, c, name, profile)
	if err != nil {
		meta.AgentVersionNote = "version unknown"
		_, _ = fmt.Fprintf(errw,
			"warning: installed agent version probe failed: %v\n", err)
		return meta
	}
	meta.AgentVersion = version
	return meta
}

type agentboxShowMetadata struct {
	AgentName        string
	AgentPackage     string
	AgentVersion     string
	AgentVersionNote string
	Policy           string
	WorkdirMode      string
	Workdir          string
	WorkdirContainer string
	// UnconfinedNetwork is the declared network confinement posture
	// (Config.UnconfinedNetwork). When true, box show prints a loud warning
	// and the egress allow-list / L7 proxy are not in effect.
	UnconfinedNetwork bool
	// Privileged is the declared privileged setting; Runtime is the
	// inspected launch-mode evidence. Either one prints the privileged warning.
	Privileged bool
	Runtime    agentbox.RuntimeEvidence
}

func (m agentboxShowMetadata) privilegedView() bool {
	return m.Privileged || m.Runtime.AnyPrivileged
}

func agentDisplayLine(meta agentboxShowMetadata) string {
	if meta.AgentName == "" {
		return "(none)"
	}
	if meta.AgentVersion != "" {
		return meta.AgentName + " " + meta.AgentVersion
	}
	if meta.AgentVersionNote != "" {
		return fmt.Sprintf("%s (%s)", meta.AgentName, meta.AgentVersionNote)
	}
	return meta.AgentName
}

// policyDisplayFromConfig renders a one-line summary of a box's policy intent
// for `box show`: its features, any explicit capabilities beyond the
// workdir-write default, and whether /workdir is read-only. The full
// effective capability set (with origins) and the generated policy are in
// `box inspect`.
func policyDisplayFromConfig(cfg *agentbox.Config) string {
	if cfg == nil {
		return ""
	}
	var parts []string
	if len(cfg.Features) > 0 {
		features := append([]string(nil), cfg.Features...)
		sort.Strings(features)
		parts = append(parts, "features: "+strings.Join(features, ","))
	}
	var extraCaps []string
	for _, c := range cfg.Capabilities {
		if c == capabilities.WorkdirWriteCapability {
			continue
		}
		extraCaps = append(extraCaps, c)
	}
	if len(extraCaps) > 0 {
		sort.Strings(extraCaps)
		parts = append(parts, "capabilities: "+strings.Join(extraCaps, ","))
	}
	if !cfg.WorkdirWritable() {
		parts = append(parts, "read-only")
	}
	return strings.Join(parts, " + ")
}

func displayImageRef(ref string) string {
	if ref == agentboximage.RefAgentbox || ref == "docker.io/"+agentboximage.RefAgentbox {
		return agentboximage.RefAgentbox
	}
	return ref
}

func formatCreatedForDisplay(created string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return created
	}
	return fmt.Sprintf("%s (%s ago)", t.UTC().Format("2006-01-02 15:04:05 UTC"), compactAgo(now.Sub(t)))
}

func compactAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
