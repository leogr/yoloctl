package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Request.Log values are the --log-level values; empty is the default (info).
const (
	LogDebug = "debug"
	LogWarn  = "warn"
	LogError = "error"
)

// LogSetting is the Request.Log value for a host verbosity. A verbosity
// between info and debug (not reachable from the CLI) asks the worker for
// debug: the worker only filters, and the host filters again.
func LogSetting(v prettylog.Verbosity) string {
	switch {
	case v <= prettylog.Errors:
		return LogError
	case v == prettylog.Quiet:
		return LogWarn
	case v == prettylog.Normal:
		return ""
	default:
		return LogDebug
	}
}

// LogVerbosity parses Request.Log.
func LogVerbosity(s string) (prettylog.Verbosity, error) {
	switch s {
	case "":
		return prettylog.Normal, nil
	case LogError:
		return prettylog.Errors, nil
	case LogWarn:
		return prettylog.Quiet, nil
	case LogDebug:
		return prettylog.Trace, nil
	default:
		return prettylog.Normal, errors.New("invalid controller log verbosity")
	}
}

// LogSink receives decoded structured log records. *prettylog.Logger is one;
// the host renders them, the controller relays them as "log" frames.
type LogSink interface {
	Handle(context.Context, slog.Record) error
}

// frameLog relays each record as one "log" frame through the request's
// clientSink, so a vanished client detaches instead of blocking the worker.
type frameLog struct{ out io.Writer }

func (f frameLog) Handle(_ context.Context, r slog.Record) error {
	raw, err := prettylog.MarshalRecord(r, prettylog.Limits{})
	if err != nil {
		return err
	}
	_, err = f.out.Write(raw)
	return err
}

// workerLogJoin bounds how long a worker's log pipe may stay open after the
// worker exited (an escaped descendant holding it). It is stated, not hidden:
// the stream then ends with an explicit incomplete-stream record.
const workerLogJoin = 2 * time.Second

func controllerLogRecord(level slog.Level, msg string) slog.Record {
	r := slog.NewRecord(time.Now(), level, msg, 0)
	r.AddAttrs(prettylog.Actor("controller"))
	return r
}

// relayWorkerLog forwards a worker's framed records in order until EOF. A
// partial final record is discarded and reported. Any other read error is
// returned after draining what remains readable, so a worker never blocks on
// a full pipe; the caller reports it (or reports an abandoned stream).
func relayWorkerLog(ctx context.Context, r io.Reader, sink LogSink) error {
	reader := prettylog.NewWireReader(r)
	for {
		rec, err := reader.Next()
		switch {
		case err == nil:
			_ = sink.Handle(ctx, rec)
			continue
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, prettylog.ErrIncompleteRecord):
			_ = sink.Handle(ctx, controllerLogRecord(prettylog.LevelWarn, "[1 incomplete log record discarded]"))
			return nil
		default:
			_, _ = io.Copy(io.Discard, r)
			return err
		}
	}
}

// joinWorkerLog waits for the relay to reach EOF, at most workerLogJoin after
// the worker exited. An abandoned stream ends with one explicit record.
func joinWorkerLog(read io.Closer, done <-chan struct{}, relayErr *error, exited time.Time, sink LogSink) {
	timer := time.NewTimer(time.Until(exited.Add(workerLogJoin)))
	defer timer.Stop()
	select {
	case <-done:
		if *relayErr != nil {
			_ = sink.Handle(context.Background(), controllerLogRecord(prettylog.LevelWarn, fmt.Sprintf("[log stream discarded: %v]", *relayErr)))
		}
	case <-timer.C:
		_ = read.Close()
		<-done
		_ = sink.Handle(context.Background(), controllerLogRecord(prettylog.LevelWarn, "[log stream incomplete]"))
	}
}

// plainLogSink renders records as plain lines on a byte stream for callers
// that supply no structured sink (fixtures and non-relaying callers).
func plainLogSink(w io.Writer) LogSink {
	if w == nil {
		w = io.Discard
	}
	return prettylog.New(w, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
}
