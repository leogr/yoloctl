package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func hostLogRecord(raw string) slog.Record {
	diagnostic, ok := lima.ParseHostDiagnostic(raw)
	if !ok {
		// Unknown application text is text, never an encoded outcome.
		r := slog.NewRecord(time.Now(), slog.LevelInfo, strings.TrimSuffix(raw, "\n"), 0)
		r.AddAttrs(prettylog.Actor(logging.Host))
		return r
	}
	r := slog.NewRecord(diagnostic.Time, slog.LevelInfo, diagnostic.Operation, 0)
	r.AddAttrs(prettylog.Actor(logging.Host), slog.String(prettylog.KeyErr, strings.TrimRight(diagnostic.Text, "\n")))
	if diagnostic.Space != "" {
		r.AddAttrs(slog.String("space", diagnostic.Space))
	}
	return r
}

func tailHostLog(ctx context.Context, space string, lines int, follow bool, stdout io.Writer) error {
	return tailHostLogWithSettings(ctx, space, lines, follow, stdout, logging.CurrentSettings())
}

func tailHostLogWithSettings(ctx context.Context, space string, lines int, follow bool, stdout io.Writer, settings logging.Settings) error {
	if settings.Verbosity == prettylog.Trace {
		return lima.TailHostDiagnostics(ctx, space, lines, follow, func(raw []byte) error {
			n, err := stdout.Write(raw)
			if err == nil && n != len(raw) {
				return io.ErrShortWrite
			}
			return err
		})
	}
	log := logging.NewFollowLogger(stdout, settings, nil)
	err := lima.TailHostDiagnostics(ctx, space, lines, follow, func(raw []byte) error {
		return log.Handle(ctx, hostLogRecord(string(raw)))
	})
	return errors.Join(err, log.Close())
}
