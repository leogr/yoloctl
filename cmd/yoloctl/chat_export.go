package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/chatexport"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/spf13/cobra"
)

func newChatExportCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "export", Short: "Export the room's chat history to files on this host", Long: "Export the chat history the server still keeps, as the human, one file per channel (mode 0600). " +
		"Every channel of the room by default; channels this session has not joined are joined for the export and left again, which other participants see. " +
		"Paging runs from each channel's newest message back to the oldest one the server keeps; --since and --until then select by message time. " +
		"Direct messages, join/part events and attachment files are not exported; published files appear as their name and size. " +
		"Your open chat view holds your nickname: close it first, or use --take-over, which closes it. " + chatexport.Retention, Args: cobra.NoArgs}
	cmd.Flags().StringArray("channel", nil, "Channel to export (repeatable; default every channel of the room)")
	cmd.Flags().String("since", "", "Only messages at or after this time (RFC 3339, or YYYY-MM-DD as 00:00 UTC)")
	cmd.Flags().String("until", "", "Only messages before this time (RFC 3339, or YYYY-MM-DD as 00:00 UTC)")
	cmd.Flags().String("format", chatexport.FormatText, "Output format: text (a readable transcript) or jsonl (one JSON object per message)")
	cmd.Flags().StringP("output", "o", "", "File for one channel, existing directory for several, or - for standard output (one channel); default the current directory")
	cmd.Flags().Bool("force", false, "Replace existing files; an old file is replaced only once its channel's export succeeded")
	cmd.Flags().Bool("take-over", false, "Close your open chat view, which holds your nickname, and export")
	cmd.RunE = runChatExport
	return cmd
}

// chatExportPlan is a validated export request and where each channel goes.
type chatExportPlan struct {
	space        string
	channels     []string
	targets      map[string]string // channel → file; "-" is standard output
	format       string
	since, until time.Time
	force        bool
	exported     time.Time
}

func parseExportTime(flag, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--%s %q is not an RFC 3339 time or a YYYY-MM-DD date", flag, value)
}

// planChatExport validates the flags against the room's channels and
// decides every target before anything is read: an existing file is refused
// unless --force.
func planChatExport(cmd *cobra.Command, space string, catalog []string, now time.Time) (chatExportPlan, error) {
	p := chatExportPlan{space: space, targets: map[string]string{}, exported: now.UTC()}
	p.format, _ = cmd.Flags().GetString("format")
	if p.format != chatexport.FormatText && p.format != chatexport.FormatJSONL {
		return p, fmt.Errorf("--format %q: use text or jsonl", p.format)
	}
	var err error
	since, _ := cmd.Flags().GetString("since")
	until, _ := cmd.Flags().GetString("until")
	if p.since, err = parseExportTime("since", since); err != nil {
		return p, err
	}
	if p.until, err = parseExportTime("until", until); err != nil {
		return p, err
	}
	if !p.since.IsZero() && !p.until.IsZero() && !p.since.Before(p.until) {
		return p, errors.New("--since must be before --until")
	}
	p.force, _ = cmd.Flags().GetBool("force")
	named, _ := cmd.Flags().GetStringArray("channel")
	for _, name := range named {
		if !strings.HasPrefix(name, "#") {
			name = "#" + name
		}
		if !slices.Contains(catalog, name) {
			return p, fmt.Errorf("unknown channel %s (the room has %s)", name, strings.Join(catalog, ", "))
		}
		if !slices.Contains(p.channels, name) {
			p.channels = append(p.channels, name)
		}
	}
	if len(p.channels) == 0 {
		p.channels = slices.Clone(catalog)
	}
	ext := ".txt"
	if p.format == chatexport.FormatJSONL {
		ext = ".jsonl"
	}
	name := func(channel string) string {
		return "chat-" + space + "-" + strings.TrimPrefix(channel, "#") + "-" + p.exported.Format("20060102T150405Z") + ext
	}
	output, _ := cmd.Flags().GetString("output")
	dir := ""
	switch output {
	case "-":
		if len(p.channels) != 1 {
			return p, errors.New("-o - writes one channel; name it with --channel")
		}
		p.targets[p.channels[0]] = "-"
		return p, nil
	case "":
		dir = "."
	default:
		if info, err := os.Stat(output); err == nil && info.IsDir() {
			dir = output
		} else if len(p.channels) > 1 {
			return p, fmt.Errorf("-o %s must be an existing directory when exporting %d channels", output, len(p.channels))
		} else {
			p.targets[p.channels[0]] = output
		}
	}
	if dir != "" {
		for _, channel := range p.channels {
			p.targets[channel] = filepath.Join(dir, name(channel))
		}
	}
	for _, channel := range p.channels {
		if target := p.targets[channel]; !p.force {
			if _, err := os.Lstat(target); err == nil {
				return p, fmt.Errorf("%s already exists; use --force to replace it", target)
			}
		}
	}
	return p, nil
}

func runChatExport(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	target, err := selectControllerTarget(ctx, cmd)
	if err != nil {
		return err
	}
	if target.Operator {
		return errors.New("chat export requires the human host endpoint")
	}
	var status managedchat.Status
	if err := callChatViewControllerInto(ctx, cmd, "chat.status", chatRuntimeRequest{}, &status); err != nil {
		return err
	}
	if status.Room == nil || !status.ServerReady {
		return errors.New("the chat server is not running; open it with yoloctl chat or inspect yoloctl chat status")
	}
	header, err := chatstream.RoomHeader(status.Room)
	if err != nil {
		return err
	}
	var catalog []string
	for _, channel := range header.Channels {
		catalog = append(catalog, channel.Name)
	}
	plan, err := planChatExport(cmd, target.Space, catalog, time.Now())
	if err != nil {
		return err
	}
	takeOver, _ := cmd.Flags().GetBool("take-over")
	return exportChat(ctx, plan, header.Channel, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return callChatStream(ctx, target, takeOver, in, out)
	}, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

// exportChat opens one human stream with call, exports plan.channels over
// it and writes each channel as soon as it is complete.
func exportChat(parent context.Context, plan chatExportPlan, defaultChannel string, call func(context.Context, io.Reader, io.Writer) error, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	requestRead, requestWrite := io.Pipe()
	responseRead, responseWrite := io.Pipe()
	transportDone := make(chan struct{})
	go func() {
		defer close(transportDone)
		err := call(ctx, requestRead, responseWrite)
		_ = responseWrite.CloseWithError(err)
		_ = requestRead.CloseWithError(err)
	}()
	events := make(chan chatstream.Event, 256)
	older := make(chan chatstream.HistoryRequest, 1)
	actions := make(chan chatstream.Action, 1)
	driverDone := make(chan struct{})
	go func() {
		defer close(driverDone)
		defer close(events)
		_ = chatstream.RunChannels(ctx, responseRead, requestWrite, nil, older, actions, nil, func(e chatstream.Event) { events <- e })
	}()
	report := stdout
	if plan.targets[plan.channels[0]] == "-" {
		report = stderr // the transcript owns standard output
	}
	var failed []string
	err := chatstream.Export(ctx, events, older, actions, defaultChannel, plan.channels, func(r chatstream.ExportResult) {
		if err := writeChatExport(plan, r, stdout, report); err != nil {
			failed = append(failed, r.Channel)
			_, _ = fmt.Fprintf(stderr, "%s: export failed: %v\n", r.Channel, err)
		}
		if r.Joined && r.PartErr != nil {
			_, _ = fmt.Fprintf(stderr, "%s: the export joined this channel but leaving it again failed: %v\n", r.Channel, r.PartErr)
		}
	})
	cancel()
	for range events { // let the stopped driver finish its last events
	}
	<-driverDone
	_ = responseRead.Close()
	_ = requestWrite.Close()
	<-transportDone
	switch {
	case chatNickHeld(err):
		return errors.New("your chat view is open and holds your nickname; close it, or rerun with --take-over, which closes it")
	case err != nil && parent.Err() != nil:
		return errors.New("export interrupted; completed channel files were kept")
	case err != nil:
		return fmt.Errorf("chat connection: %w", err)
	case len(failed) > 0:
		return fmt.Errorf("%d of %d channels failed: %s", len(failed), len(plan.channels), strings.Join(failed, ", "))
	}
	return nil
}

// writeChatExport writes one channel's result to its target and reports it.
// A failed channel writes nothing; a file is replaced only by its final
// rename, so an export that fails never touches an existing file.
func writeChatExport(plan chatExportPlan, r chatstream.ExportResult, stdout, report io.Writer) error {
	if r.Err != nil {
		return r.Err
	}
	selected := chatexport.Select(r.Messages, plan.since, plan.until)
	meta := chatexport.Meta{Space: plan.space, Channel: r.Channel, Exported: plan.exported, Received: r.Messages, Since: plan.since, Until: plan.until}
	var buf bytes.Buffer
	if err := chatexport.Write(&buf, plan.format, meta, selected); err != nil {
		return err
	}
	target := plan.targets[r.Channel]
	if target == "-" {
		if _, err := stdout.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("writing standard output: %w", err)
		}
	} else if plan.force {
		if err := atomicfile.Write(target, buf.Bytes(), 0o600); err != nil {
			return err
		}
	} else if err := atomicfile.Create(target, buf.Bytes(), 0o600); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s appeared during the export; it was not replaced", target)
		}
		return err
	}
	oldest, newest := chatexport.Span(r.Messages)
	where := target
	if target == "-" {
		where = "standard output"
	}
	_, _ = fmt.Fprintf(report, "%s: %d messages written to %s (received %d, oldest %s, newest %s)\n", r.Channel, len(selected), where, len(r.Messages), exportTime(oldest), exportTime(newest))
	return nil
}

func exportTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
