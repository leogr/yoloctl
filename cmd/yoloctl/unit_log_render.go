package main

import (
	"log/slog"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
)

// A guest service's log holds yoloctl's own file records (the holder's, for
// instance) and its children's raw output. Only a line framed as an owned
// record, an RFC3339 time then an optional level prefix and a yoloctl actor,
// is read as one; any other line is literal text at information level, at the
// last owned record's time, so no child text becomes a level or an outcome.
func newUnitLogRecord() func(string) slog.Record {
	var last time.Time
	return func(line string) slog.Record {
		if first, rest, ok := strings.Cut(line, " "); ok {
			if _, err := time.Parse(time.RFC3339Nano, first); err == nil && ownedRecordFrame(rest) {
				record := spaceLogRecord(line)
				last = record.Time
				return record
			}
		}
		stamp := last
		if stamp.IsZero() {
			stamp = time.Now()
		}
		return slog.NewRecord(stamp, slog.LevelInfo, line, 0)
	}
}

// ownedRecordFrame reports whether text, after a record's time, starts with
// the file Logger's optional level prefix and then a yoloctl actor.
func ownedRecordFrame(text string) bool {
	for _, prefix := range []string{"error: ", "warning: ", "notice: ", "debug: ", "uncertain: "} {
		if rest, ok := strings.CutPrefix(text, prefix); ok {
			text = rest
			break
		}
	}
	head, _, ok := strings.Cut(text, ": ")
	if !ok {
		return false
	}
	actor, _, _ := strings.Cut(head, "/")
	switch actor {
	case logging.Host, logging.VM, logging.Controller, logging.Builder, logging.Box, logging.Session, logging.Storage, logging.Audit, logging.Chat, logging.Install:
		return true
	}
	return false
}
