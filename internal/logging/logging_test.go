package logging

import (
	"bytes"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestFromEnv(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want prettylog.Verbosity
	}{
		{nil, prettylog.Normal},
		{map[string]string{"YOLOCTL_DEBUG": "1"}, prettylog.Trace},
		{map[string]string{"YOLOCTL_LOG": "warn"}, prettylog.Quiet},
		{map[string]string{"YOLOCTL_LOG": "error"}, prettylog.Errors},
		{map[string]string{"YOLOCTL_LOG": "debug"}, prettylog.Trace},
		{map[string]string{"YOLOCTL_LOG": "info", "YOLOCTL_DEBUG": "1"}, prettylog.Normal},
		{map[string]string{"YOLOCTL_LOG": "trace"}, prettylog.Normal},
	} {
		s := DefaultSettings()
		FromEnv(&s, func(k string) string { return c.env[k] })
		if s.Verbosity != c.want {
			t.Errorf("%v: got %v want %v", c.env, s.Verbosity, c.want)
		}
	}
}

func TestSetLoggerRestoresProcessLoggerAndSlogDefault(t *testing.T) {
	before, beforeDefault := Logger(), slog.Default()
	var b bytes.Buffer
	l := prettylog.New(&b, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal})
	restore := SetLogger(l)
	if Init(DefaultSettings()) != l {
		t.Fatal("Init replaced an installed Logger")
	}
	slog.Warn("via default")
	L().Info("via L", slog.String(prettylog.KeyActor, Host))
	restore()
	if Logger() != before || slog.Default() != beforeDefault {
		t.Fatal("not restored")
	}
	if b.String() != "warning: via default\nhost: via L\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestSetPaletteReachesProcessLogger(t *testing.T) {
	var b bytes.Buffer
	l := prettylog.New(&b, prettylog.Options{Format: prettylog.FormatPretty, Color: true, Verbosity: prettylog.Normal})
	defer SetLogger(l)()
	defer SetPalette(prettylog.ANSIPalette)
	SetPalette(prettylog.Palette{Warn: prettylog.Index(42)})
	L().Warn("x")
	if !strings.Contains(b.String(), "38;5;42") {
		t.Fatalf("%q", b.String())
	}
}

func TestNoUIDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/leogr/yoloctl/internal/logging").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.Contains(dep, "bubbletea") || strings.Contains(dep, "/internal/tui") || strings.Contains(dep, "lipgloss") {
			t.Fatalf("internal/logging depends on UI package %s", dep)
		}
	}
}
