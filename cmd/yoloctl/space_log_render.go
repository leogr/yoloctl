package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

const spaceLogLineLimit = 64 << 10

// spaceLogOutput formats a human log view, never a protocol or data stream.
// Debug log level preserves the original file bytes. Other levels bound
// partial lines and render through the same follow Logger as other log views.
// Plain file headers carry presentation hints only: parsing them grants no
// authority and does not participate in operation or disk-grow settlement.
type spaceLogOutput struct {
	target    io.Writer
	logger    *prettylog.Logger
	pending   []byte
	showBlank bool
}

func newSpaceLogOutput(w io.Writer, settings logging.Settings) *spaceLogOutput {
	out := &spaceLogOutput{
		target:    w,
		showBlank: settings.Verbosity.Allows(slog.NewRecord(time.Time{}, slog.LevelInfo, "", 0)),
	}
	if settings.Verbosity != prettylog.Trace {
		out.logger = logging.NewFollowLogger(w, settings, nil)
	}
	return out
}

func (w *spaceLogOutput) Write(p []byte) (int, error) {
	if w.logger == nil {
		return w.target.Write(p)
	}
	used := 0
	for len(p) != 0 {
		end := bytes.IndexByte(p, '\n')
		take := len(p)
		if end >= 0 {
			take = end + 1
		}
		take = min(take, spaceLogLineLimit-len(w.pending))
		w.pending = append(w.pending, p[:take]...)
		used += take
		p = p[take:]
		if len(w.pending) == spaceLogLineLimit || w.pending[len(w.pending)-1] == '\n' {
			line := bytes.TrimSuffix(w.pending, []byte{'\n'})
			err := w.emit(string(line))
			w.pending = w.pending[:0]
			if err != nil {
				return used, err
			}
		}
	}
	return used, nil
}

func (w *spaceLogOutput) Close() error {
	if w.logger == nil {
		return nil
	}
	var err error
	if len(w.pending) != 0 {
		err = w.emit(string(w.pending))
		w.pending = nil
	}
	return errors.Join(err, w.logger.Close())
}

func (w *spaceLogOutput) emit(line string) error {
	if line == "" {
		if err := w.logger.Flush(); err != nil {
			return err
		}
		if !w.showBlank {
			return nil
		}
		_, err := io.WriteString(w.target, "\n")
		return err
	}
	return w.logger.Handle(context.Background(), spaceLogRecord(line))
}

func spaceLogRecord(line string) slog.Record {
	stamp := time.Now()
	if first, rest, ok := strings.Cut(line, " "); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, first); err == nil {
			stamp, line = parsed, rest
		}
	}
	level := slog.LevelInfo
	var attrs []slog.Attr
	for _, prefix := range []struct {
		text  string
		level slog.Level
	}{
		{"error: ", slog.LevelError}, {"warning: ", slog.LevelWarn}, {"notice: ", prettylog.LevelNotice}, {"debug: ", slog.LevelDebug}, {"uncertain: ", slog.LevelWarn},
	} {
		if strings.HasPrefix(line, prefix.text) {
			line = strings.TrimPrefix(line, prefix.text)
			level = prefix.level
			if prefix.text == "uncertain: " {
				attrs = append(attrs, prettylog.Outcome(prettylog.OutcomeUnknown))
			}
			break
		}
	}
	if head, rest, ok := strings.Cut(line, ": "); ok {
		actor, executor, _ := strings.Cut(head, "/")
		switch actor {
		case logging.Host, logging.VM, logging.Controller, logging.Builder, logging.Box, logging.Session, logging.Storage, logging.Audit, logging.Chat, logging.Install:
			attrs = append(attrs, prettylog.Actor(actor))
			if executor != "" {
				attrs = append(attrs, prettylog.Executor(executor))
			}
			line = rest
		}
	}
	record := slog.NewRecord(stamp, level, line, 0)
	record.AddAttrs(attrs...)
	return record
}
