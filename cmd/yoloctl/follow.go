package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/followview"
	"github.com/leogr/yoloctl/internal/mermaid"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/termio"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	followRun         = followview.Run
	followHostAllowed = func() error {
		if spaceenv.Native() || os.Getenv(spaceenv.ContextEnv) != "" {
			return errors.New("follow is a host-only view")
		}
		if _, present := os.LookupEnv("OPENSHELL_SANDBOX"); present {
			return errors.New("follow is unavailable inside an agent box")
		}
		op, err := loadOperatorClient()
		if err != nil {
			return err
		}
		if op != nil {
			return errors.New("box operator cannot open follow")
		}
		if installContainerSignal() != "" {
			return errors.New("follow is a host-only view")
		}
		return nil
	}
	followInteractive = func(cmd *cobra.Command) bool {
		return term.IsTerminal(int(os.Stdout.Fd())) && stdinIsTerminal(cmd.InOrStdin())
	}
	followGrants  = currentFollowGrants
	followCommand = vmhost.ShellCommand
	followDefault = func(ctx context.Context, space string) (string, error) {
		boot, err := checkControllerSpace(ctx, space)
		if err != nil {
			return "", err
		}
		var settings spaceconfig.Config
		if err := controllerJSON(ctx, boot, "space.settings", nil, nil, &settings, os.Stderr); err != nil {
			return "", err
		}
		return settings.DefaultWorkdir, nil
	}
)

func newFollowCmd() *cobra.Command {
	var host bool
	var format string
	cmd := &cobra.Command{Use: "follow PATH", Short: "Watch a granted file in a live text or Markdown view", Long: `Read a file inside the selected running VM, below its current space grants.
Relative paths use the space's default workdir. No VM is started and grants
are never changed. Symlink components in VM paths are refused: use a direct
path inside a grant. --host explicitly reads a host file instead, relative
to the launch directory, without contacting a VM; it cannot use --space.

The view refreshes once per second and on r. Small files are read completely;
Markdown renders through 2 MiB. Larger files use a 1 MiB plain-text window,
including with --format markdown. Page up/down browses older/newer windows.
Edits refresh the active window; this is a live viewer, not lossless logging.
Scroll up to pause end-follow; f or End resumes it. Ctrl+S toggles Markdown
source, drag selects displayed text, y copies through host clipboard policy,
and q closes the view. A failed read retains the last display marked stale.

  yoloctl follow notes.md --space dev
  yoloctl follow --host --format text /tmp/service.log`, Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		if args[0] == "" {
			return errors.New("select a file path")
		}
		if _, err := followview.ParseFormat(format); err != nil {
			return err
		}
		if host && cmd.Flags().Changed("space") {
			return errors.New("--host and --space are mutually exclusive")
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		if err := followHostAllowed(); err != nil {
			return asUserError(err)
		}
		if !followInteractive(cmd) {
			return asUserError(errors.New("follow needs an interactive terminal"))
		}
		// This view owns its local terminal. Keep private channels out of VM children.
		view, err := termio.InheritedView()
		if err != nil {
			return err
		}
		defer runtime.KeepAlive(view)
		requests, err := termio.InheritedTabRequests()
		if err != nil {
			return err
		}
		if requests != nil {
			defer func() { _ = requests.Close() }()
		}
		var path, space string
		var reader followview.Reader
		if host {
			path, err = absoluteFollowPath(args[0], "")
			reader = followview.HostReader{Path: path}
		} else {
			space, err = selectedSpace(cmd)
			if err != nil {
				return asUserError(err)
			}
			// Resolve the absolute name once. Polls use only that pinned name and space.
			base := ""
			if !filepath.IsAbs(args[0]) && !strings.HasPrefix(args[0], "~") {
				ctx, cancel := context.WithTimeout(cmd.Context(), 7*time.Second)
				base, err = followDefault(ctx, space)
				cancel()
				if err != nil {
					return err
				}
			}
			path, err = absoluteFollowPath(args[0], base)
			reader = followVMReader(space, path)
		}
		if err != nil {
			return asUserError(err)
		}
		selectedFormat, _ := followview.ParseFormat(format)
		if selectedFormat == followview.Auto {
			selectedFormat = followview.Text
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".md" || ext == ".markdown" {
				selectedFormat = followview.Markdown
			}
		}
		prefs, err := config.LoadTUI()
		if err != nil {
			return err
		}

		opts := followview.Options{Reader: reader, Path: path, Space: space, Format: selectedFormat}
		if prefs.ClipboardEnabled() {
			opts.Copy = func(text string) tea.Cmd {
				sequence, notice := termio.EvaluateOSC52(termio.OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte(text))}, true)
				if sequence == "" {
					return func() tea.Msg { return followview.CopyResultMsg{Err: errors.New(notice)} }
				}
				return tea.Raw(sequence)
			}
		}
		if prefs.MermaidEnabled() {
			client := mermaid.Default()
			defer client.Close()
			opts.Mermaid = client
		}
		return followRun(cmd.Context(), opts)
	}}
	cmd.Flags().String("space", "", "Space whose grants and VM contain the file")
	cmd.Flags().BoolVar(&host, "host", false, "Read an explicitly selected host file instead of the VM")
	cmd.Flags().StringVar(&format, "format", "auto", "Display format: auto, text or markdown")
	_ = cmd.RegisterFlagCompletionFunc("space", completeSpaces)
	_ = cmd.RegisterFlagCompletionFunc("format", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return []cobra.Completion{"auto", "text", "markdown"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.ValidArgsFunction = completeFollowPaths
	return cmd
}
func absoluteFollowPath(value, base string) (string, error) {
	if !filepath.IsAbs(value) && !strings.HasPrefix(value, "~") && base != "" {
		value = filepath.Join(base, value)
	}
	return expandUserPath(value)
}
func currentFollowGrants(ctx context.Context, space string) ([]string, error) {
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return nil, err
	}
	inst, found, err := vmhost.New().Lookup(ctx, space)
	if err != nil {
		return nil, err
	}
	if !found || inst.Status != "Running" {
		return nil, fmt.Errorf("space %s is not running; start it explicitly", space)
	}
	roots := make([]string, 0, len(boot.Mounts))
	for _, mount := range boot.Mounts {
		roots = append(roots, mount.Source)
	}
	return roots, nil
}
func followVMReader(space, path string) followview.VMReader {
	return followview.VMReader{Path: path, Authority: func(ctx context.Context) ([]string, error) { return followGrants(ctx, space) }, Command: func(ctx context.Context, args ...string) *exec.Cmd { return followCommand(ctx, space, args...) }}
}

func completeFollowPaths(cmd *cobra.Command, args []string, token string) ([]cobra.Completion, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	if len(args) > 0 || followHostAllowed() != nil {
		return nil, directive
	}
	host, _ := cmd.Flags().GetBool("host")
	if host {
		if cmd.Flags().Changed("space") {
			return nil, directive
		}
		return listPathCandidates(token, false, nil)
	}
	space, err := selectedSpace(cmd)
	if err != nil {
		return nil, directive
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
	defer cancel()
	roots, err := followGrants(ctx, space)
	if err != nil {
		return nil, directive
	}
	var out []cobra.Completion
	selectedRoot := false
	for _, root := range roots {
		candidate := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
		if candidate == token {
			selectedRoot = true
		} else if strings.HasPrefix(candidate, token) {
			out = append(out, cobra.CompletionWithDesc(candidate, "space grant"))
		}
	}
	if !selectedRoot && (token == "" || len(out) > 0) {
		sort.Strings(out)
		return out, directive | cobra.ShellCompDirectiveNoSpace
	}
	// A fully selected grant is a directory to list, including when another
	// grant is nested below it. Prefix suggestions must not intercept it.
	out = nil
	literalParent, base := splitCompletionToken(token)
	lookup := literalParent
	if lookup == "" {
		lookup = "."
	}
	workdir := ""
	if !filepath.IsAbs(lookup) && !strings.HasPrefix(lookup, "~") {
		workdir, err = followDefault(ctx, space)
		if err != nil {
			return nil, directive
		}
	}
	path, err := absoluteFollowPath(lookup, workdir)
	if err != nil {
		return nil, directive
	}
	entries, err := followVMReader(space, path).List(ctx, path, base)
	if err != nil {
		return nil, directive
	}
	sawDir := false
	for _, entry := range entries {
		out = append(out, literalParent+entry)
		sawDir = sawDir || strings.HasSuffix(entry, "/")
	}
	if sawDir {
		directive |= cobra.ShellCompDirectiveNoSpace
	}
	return out, directive
}
