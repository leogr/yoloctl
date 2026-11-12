package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/auditui"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var auditContainerSignal = installContainerSignal

func auditHostAvailable() bool {
	if spaceenv.Native() || os.Getenv(spaceenv.ContextEnv) != "" {
		return false
	}
	if _, present := os.LookupEnv("OPENSHELL_SANDBOX"); present {
		return false
	}
	if auditContainerSignal() != "" {
		return false
	}
	return true
}

func requireAuditHost() error {
	if !auditHostAvailable() {
		return asUserError(errors.New("audit control and viewing require the host"))
	}
	op, err := loadOperatorClient()
	if err != nil || op != nil {
		return asUserError(errors.New("audit control and viewing require the host"))
	}
	return nil
}

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "View opt-in VM security audit events",
		Long: "Host-only, opt-in Falco auditing inside the space VM. Disabled by default.\nEvents keep selected process/container/path fields for up to seven days or 64 MiB;\nraw argv, environment and buffers are omitted. Detection is best effort, with\nno remediation or complete/tamper-proof trail. Commands never boot the VM.", Args: cobra.NoArgs, RunE: runAuditView}
	cmd.PersistentFlags().String("space", "", "Space (defaults to current context)")
	status := &cobra.Command{Use: "status", Short: "Show desired and actual audit state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, err := auditTarget(cmd)
		if err != nil {
			return err
		}
		result, err := fetchAuditStatus(cmd.Context(), target)
		if err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Desired: %t  State: %s  Driver: %s\nVersion: actual=%s desired=%s\nDrops: %d  Metadata gaps: %d\n%s\n", result.Desired.Enabled, audit.Clean(result.State), audit.Clean(result.Driver), audit.Clean(result.ActualVersion), audit.Clean(result.Desired.Version), result.Drops, result.MetadataGaps, audit.Clean(result.Error))
		if err != nil {
			return err
		}
		selection := "default"
		if result.Rules.SelectionEntries > 0 {
			selection = fmt.Sprintf("customized (%d entries)", result.Rules.SelectionEntries)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Rules: %d files; selection %s; Falco warnings at validation %d; compatible %t\n", result.Rules.Files, selection, result.Rules.Warnings, result.Rules.Compatible)
		return err
	}}
	status.Flags().Bool("json", false, "Print JSON")
	for _, action := range []string{"enable", "disable"} {
		cmd.AddCommand(&cobra.Command{Use: action, Short: action + " VM-native Falco auditing (no VM or box restart)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := auditTarget(cmd)
			if err != nil {
				return err
			}
			var input *auditRequest
			var in io.Reader
			if action == "enable" {
				status, err := fetchAuditStatus(cmd.Context(), target)
				if err != nil {
					return err
				}
				if status.State == "unsupported" {
					return errors.New(status.Error)
				}
				cache, err := layout.AuditCacheDir()
				if err != nil {
					return err
				}
				raw, err := audit.Fetch(cmd.Context(), cache, status.Architecture)
				if err != nil {
					return err
				}
				pin, err := audit.Pinned(status.Architecture)
				if err != nil {
					return err
				}
				input = &auditRequest{ArchiveSHA256: pin.SHA256}
				in = bytes.NewReader(raw)
			}
			var output bytes.Buffer
			err = callAudit(cmd.Context(), target, "audit."+action, input, in, &boundedChatWriter{Out: &output, Remaining: 64 << 10})
			if err != nil {
				return err
			}
			var status audit.Status
			if err := decodeBoundedJSON(&output, &status, 64<<10); err != nil {
				return err
			}
			if status.RuleCleanupWarning != "" {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Warning: rule retirement deferred: "+audit.DiagnosticText(status.RuleCleanupWarning)); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Audit %s: %s (%s)\n", action, audit.Clean(status.State), audit.Clean(status.Driver))
			return err
		}})
	}
	logs := &cobra.Command{Use: "logs", Short: "Read bounded audit history or follow new events",
		Long: "Read retained Falco events. The origin column shows a box name when metadata\nis available, (vm) when Falco reports container=host inside the space VM,\n(no box) for a container without box metadata, or (unknown) when no container\nID was retained. Use --verbose for all retained fields or --json for JSONL.",
		Args: cobra.NoArgs, RunE: runAuditLogs}
	addAuditQueryFlags(logs)
	logs.Flags().Bool("follow", false, "Follow new events until interrupted")
	logs.Flags().Bool("json", false, "Print privacy-filtered JSONL")
	logs.Flags().Bool("verbose", false, "Show all retained fields below each rule message")
	logs.Flags().Int("limit", 500, "Maximum events per history page (1–1000)")
	export := &cobra.Command{Use: "export --output PATH", Short: "Export a finite privacy-filtered JSONL interval", Args: cobra.NoArgs, RunE: runAuditExport}
	addAuditQueryFlags(export)
	export.Flags().String("output", "", "New private host output file (refuses overwrite)")
	_ = export.MarkFlagRequired("output")
	cmd.AddCommand(status, logs, export, newAuditRulesCmd())
	return cmd
}

func addAuditQueryFlags(cmd *cobra.Command) {
	cmd.Flags().String("since", "", "Start time (RFC3339)")
	cmd.Flags().String("until", "", "End time (RFC3339)")
	cmd.Flags().String("box", "", "Filter by box (unavailable metadata remains unlabelled)")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return err
		}
		box, _ := cmd.Flags().GetString("box")
		if box != "" {
			return validateAgentboxName(box)
		}
		return nil
	}
}

func auditQuery(cmd *cobra.Command) (audit.Query, error) {
	var query audit.Query
	query.Box, _ = cmd.Flags().GetString("box")
	if query.Box != "" {
		if err := layout.ValidateName("agentbox", query.Box); err != nil {
			return query, err
		}
	}
	for _, flag := range []string{"since", "until"} {
		value, _ := cmd.Flags().GetString(flag)
		if value == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return query, fmt.Errorf("--%s needs an RFC3339 time", flag)
		}
		if flag == "since" {
			query.Since = t
		} else {
			query.Until = t
		}
	}
	if !query.Until.IsZero() && query.Since.After(query.Until) {
		return query, errors.New("--since must not be after --until")
	}
	return query, nil
}

func auditTarget(cmd *cobra.Command) (controllerTarget, error) {
	if err := requireAuditHost(); err != nil {
		return controllerTarget{}, err
	}
	return controllerTargetForCommand(cmd.Context(), cmd)
}
func callAudit(ctx context.Context, target controllerTarget, method string, input *auditRequest, in io.Reader, out io.Writer) error {
	if target.Operator {
		return errors.New("audit requires the host endpoint")
	}
	var payload []byte
	if input != nil {
		var err error
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	return target.Call(ctx, controller.Request{ID: rand.Text(), Method: method, Space: target.Space, Dash: -1, Payload: payload}, controller.ClientIO{In: in, Out: out, Err: io.Discard})
}
func fetchAuditStatus(ctx context.Context, target controllerTarget) (audit.Status, error) {
	var output bytes.Buffer
	var status audit.Status
	if err := callAudit(ctx, target, "audit.status", nil, nil, &boundedChatWriter{Out: &output, Remaining: 64 << 10}); err != nil {
		return status, err
	}
	err := decodeBoundedJSON(&output, &status, 64<<10)
	return status, err
}

// Each request is finite even while its caller follows. Backpressure affects
// this reader only; the collector owns independent descriptors and storage.
func scanAudit(ctx context.Context, target controllerTarget, method string, query audit.Query, emit func(audit.Event) error) (audit.Summary, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := callAudit(ctx, target, method, &auditRequest{Query: query}, nil, writer)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	joined := false
	defer func() {
		cancel()
		_ = reader.Close()
		if !joined {
			<-done
		}
	}()
	buffered := bufio.NewReaderSize(reader, audit.MaxEvent+2048)
	var summary audit.Summary
	complete, count, last := false, 0, uint64(0)
	for {
		line, err := buffered.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil {
			return summary, errors.New("incomplete or oversized audit response")
		}
		var frame audit.Frame
		if decodeBoundedJSON(bytes.NewReader(line), &frame, audit.MaxEvent+2048) != nil {
			return summary, errors.New("invalid audit response")
		}
		if complete {
			return summary, errors.New("audit response continued after completion")
		}
		switch frame.Type {
		case "event":
			if frame.Event == nil || frame.Summary != nil || frame.Event.Cursor <= last {
				return summary, errors.New("invalid audit event cursor")
			}
			if err := emit(*frame.Event); err != nil {
				return summary, err
			}
			last = frame.Event.Cursor
			count++
			if count > 1<<20 {
				return summary, errors.New("audit export exceeds event bound")
			}
		case "summary":
			if frame.Summary == nil || frame.Event != nil {
				return summary, errors.New("invalid audit summary")
			}
			summary = *frame.Summary
			complete = true
		default:
			return summary, errors.New("unknown audit response frame")
		}
	}
	err := <-done
	joined = true
	if err != nil {
		return summary, err
	}
	if !complete || summary.Count != count || summary.Last < last || summary.Upper < summary.Last {
		return summary, errors.New("audit export completion was not verified")
	}
	return summary, nil
}

func runAuditLogs(cmd *cobra.Command, _ []string) error {
	query, err := auditQuery(cmd)
	if err != nil {
		return err
	}
	query.Limit, _ = cmd.Flags().GetInt("limit")
	if query.Limit < 1 || query.Limit > 1000 {
		return errors.New("--limit must be 1–1000")
	}
	query.Tail = query.Since.IsZero()
	follow, _ := cmd.Flags().GetBool("follow")
	if follow && !query.Until.IsZero() {
		return errors.New("--follow does not take --until")
	}
	target, err := auditTarget(cmd)
	if err != nil {
		return err
	}
	return streamAuditLogs(cmd, target, query, follow, waitAuditPoll)
}

func streamAuditLogs(cmd *cobra.Command, target controllerTarget, query audit.Query, follow bool, pause func(context.Context) error) (result error) {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	verbose, _ := cmd.Flags().GetBool("verbose")
	settings, err := logSettings(cmd.Flags(), os.Getenv)
	if err != nil {
		return err
	}
	_, color := prettylog.DecideColor(cmd.OutOrStdout(), settings.Color, os.Getenv, writerIsTerminal)
	options := auditui.EventOptions{Color: color, Verbose: verbose, Width: 100}
	if output, ok := cmd.OutOrStdout().(*os.File); ok && writerIsTerminal(output) {
		if width, _, err := term.GetSize(int(output.Fd())); err == nil {
			options.Width = width
		}
	}
	var printer *auditui.LogPrinter
	if !jsonOutput {
		printer = auditui.NewLogPrinter(cmd.Context(), cmd.OutOrStdout(), settings.Verbosity, options)
		defer func() {
			if err := printer.Close(); result == nil {
				result = err
			}
		}()
	}
	format, summaryColor := prettylog.DecideColor(cmd.ErrOrStderr(), settings.Color, os.Getenv, writerIsTerminal)
	health := prettylog.New(cmd.ErrOrStderr(), prettylog.Options{Format: format, Color: summaryColor, Verbosity: settings.Verbosity})
	defer func() {
		if err := health.Close(); result == nil {
			result = err
		}
	}()
	var previous audit.Summary
	first := true
	for {
		summary, err := scanAudit(cmd.Context(), target, "audit.logs", query, func(event audit.Event) error {
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(event)
			}
			return printer.Add(cmd.Context(), event)
		})
		if err != nil {
			return err
		}
		// Count, cursors and interval timestamps describe each bounded page.
		// Only the initial page and changes in loss/gap state need a notice.
		if first || summary.Drops != previous.Drops || summary.MetadataGaps != previous.MetadataGaps || summary.Gap != previous.Gap {
			level := prettylog.LevelInfo
			if summary.Drops > 0 || summary.MetadataGaps > 0 || summary.Gap || !first {
				level = prettylog.LevelWarn
			}
			record := slog.NewRecord(time.Time{}, level, auditSummaryText(summary), 0)
			record.AddAttrs(prettylog.Actor(logging.Audit), prettylog.Executor(logging.Falco))
			if err := health.Handle(cmd.Context(), record); err != nil {
				return err
			}
		}
		first, previous = false, summary
		if !follow {
			return nil
		}
		query.After, query.Tail = summary.Last, false
		if err := pause(cmd.Context()); err != nil {
			return err
		}
	}
}

func waitAuditPoll(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return nil
	}
}

func auditSummaryText(s audit.Summary) string {
	return fmt.Sprintf("Audit interval %s – %s: %d events; drops=%d metadata-gaps=%d retention-gap=%t", s.Since.UTC().Format(time.RFC3339), s.Until.UTC().Format(time.RFC3339), s.Count, s.Drops, s.MetadataGaps, s.Gap)
}
func reportAuditSummary(out io.Writer, s audit.Summary) {
	_, _ = fmt.Fprintln(out, auditSummaryText(s))
}

func runAuditExport(cmd *cobra.Command, _ []string) error {
	query, err := auditQuery(cmd)
	if err != nil {
		return err
	}
	target, err := auditTarget(cmd)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	output, err = expandUserPath(output)
	if err != nil {
		return err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	summary, err := publishAuditExport(output, func(out io.Writer) (audit.Summary, error) {
		return scanAudit(cmd.Context(), target, "audit.export", query, func(event audit.Event) error { return json.NewEncoder(out).Encode(event) })
	})
	if err != nil {
		return err
	}
	reportAuditSummary(cmd.ErrOrStderr(), summary)
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "exported audit events to %s\n", output)
	return err
}

func publishAuditExport(output string, write func(io.Writer) (audit.Summary, error)) (audit.Summary, error) {
	var summary audit.Summary
	root, err := os.OpenRoot(filepath.Dir(output))
	if err != nil {
		return summary, err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(output)
	if _, err := root.Lstat(name); err == nil {
		return summary, errors.New("audit export output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, err
	}
	temp := ".yoloctl-audit-" + rand.Text() + ".tmp"
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return summary, err
	}
	defer func() { _ = file.Close(); _ = root.Remove(temp) }()
	summary, err = write(file)
	if err != nil {
		return summary, err
	}
	if err := file.Sync(); err != nil {
		return summary, err
	}
	if err := file.Close(); err != nil {
		return summary, err
	}
	if err := root.Link(temp, name); err != nil {
		return summary, err
	}
	if err := root.Remove(temp); err != nil {
		return summary, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return summary, err
	}
	defer func() { _ = dir.Close() }()
	return summary, dir.Sync()
}

func runAuditView(cmd *cobra.Command, _ []string) error {
	if err := requireAuditHost(); err != nil {
		return err
	}
	if !stdinIsTerminal(cmd.InOrStdin()) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("audit needs a terminal; use audit status/logs/export for scripts")
	}
	target, err := auditTarget(cmd)
	if err != nil {
		return err
	}
	view, err := termio.InheritedView()
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(view)
	fetch := func(ctx context.Context, q audit.Query) (auditui.Batch, error) {
		var batch auditui.Batch
		var err error
		batch.Status, err = fetchAuditStatus(ctx, target)
		if err != nil {
			return batch, err
		}
		batch.Summary, err = scanAudit(ctx, target, "audit.logs", q, func(e audit.Event) error { batch.Events = append(batch.Events, e); return nil })
		return batch, err
	}
	settings, err := logSettings(cmd.Flags(), os.Getenv)
	if err != nil {
		return err
	}
	_, color := prettylog.DecideColor(cmd.OutOrStdout(), settings.Color, os.Getenv, writerIsTerminal)
	model := auditui.New(cmd.Context(), target.Space, fetch).WithColor(color).WithVerbosity(settings.Verbosity)
	defer func() { _ = model.Close() }()
	_, err = tea.NewProgram(model, tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout())).Run()
	return err
}
