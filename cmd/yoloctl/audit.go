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
	"os"
	"path/filepath"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/auditui"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/termio"
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
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Audit %s: %s (%s)\n", action, audit.Clean(status.State), audit.Clean(status.Driver))
			return err
		}})
	}
	logs := &cobra.Command{Use: "logs", Short: "Read bounded audit history or follow new events", Args: cobra.NoArgs, RunE: runAuditLogs}
	addAuditQueryFlags(logs)
	logs.Flags().Bool("follow", false, "Follow new events until interrupted")
	logs.Flags().Bool("json", false, "Print privacy-filtered JSONL")
	logs.Flags().Int("limit", 500, "Maximum events per history page (1–1000)")
	export := &cobra.Command{Use: "export --output PATH", Short: "Export a finite privacy-filtered JSONL interval", Args: cobra.NoArgs, RunE: runAuditExport}
	addAuditQueryFlags(export)
	export.Flags().String("output", "", "New private host output file (refuses overwrite)")
	_ = export.MarkFlagRequired("output")
	cmd.AddCommand(status, logs, export)
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
	return selectControllerTarget(cmd.Context(), cmd)
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
	jsonOutput, _ := cmd.Flags().GetBool("json")
	for {
		summary, err := scanAudit(cmd.Context(), target, "audit.logs", query, func(event audit.Event) error {
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(event)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), auditui.EventLine(event))
			return err
		})
		if err != nil {
			return err
		}
		reportAuditSummary(cmd.ErrOrStderr(), summary)
		if !follow {
			return nil
		}
		query.After, query.Tail = summary.Last, false
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(time.Second):
		}
	}
}

func reportAuditSummary(out io.Writer, s audit.Summary) {
	_, _ = fmt.Fprintf(out, "Audit interval %s – %s: %d events; drops=%d metadata-gaps=%d retention-gap=%t\n", s.Since.UTC().Format(time.RFC3339), s.Until.UTC().Format(time.RFC3339), s.Count, s.Drops, s.MetadataGaps, s.Gap)
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
	_, err = tea.NewProgram(auditui.New(cmd.Context(), target.Space, fetch), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout())).Run()
	return err
}
