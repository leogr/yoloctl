package main

import (
	"log/slog"
	"testing"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Only a host step names host.log, with its own space.
func TestHostLogPointer(t *testing.T) {
	root := prettylog.StepView{Title: "Starting space dev", Attrs: []slog.Attr{slog.String(logging.KeySpace, "dev")}}
	if got := hostLogPointer(logging.Host, root); got != "VM launcher log: yoloctl space logs dev --host" {
		t.Fatal(got)
	}
	if got := hostLogPointer("builder", root); got != "" {
		t.Fatal("an image step has no kept output", got)
	}
	if got := hostLogPointer(logging.Host, prettylog.StepView{}); got != "" {
		t.Fatal("no space, no pointer", got)
	}
	bad := prettylog.StepView{Attrs: []slog.Attr{slog.String(logging.KeySpace, "../x")}}
	if got := hostLogPointer(logging.Host, bad); got != "" {
		t.Fatal("an invalid space is never printed", got)
	}
}
