package config

import (
	"os"
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/tui/term"
	"gopkg.in/yaml.v3"
)

func boolPointer(v bool) *bool { return &v }

func TestHostIntegrationPolicies(t *testing.T) {
	var absent *TUI
	if absent.BrowserPolicy() != "on" || !absent.ClipboardEnabled() {
		t.Fatal("unexpected default desktop policies")
	}
	for _, policy := range []string{"", "ask", "on", "off", "unknown"} {
		var cfg TUI
		if err := yaml.Unmarshal([]byte("seams:\n  browser: "+policy+"\n  clipboard: false\n"), &cfg); err != nil {
			t.Fatal(err)
		}
		want := policy
		if want == "" {
			want = "on"
		}
		if want == "unknown" {
			want = "ask"
		}
		if cfg.BrowserPolicy() != want || cfg.ClipboardEnabled() {
			t.Fatalf("policy %q not honored: %+v", policy, cfg.Seams)
		}
	}
}

func TestClipboardEnabledDefaultsOnWithExplicitOptOut(t *testing.T) {
	var nilConfig *TUI
	for name, cfg := range map[string]*TUI{
		"nil":     nilConfig,
		"empty":   {},
		"enabled": {Seams: TUISeams{Clipboard: boolPointer(true)}},
	} {
		t.Run(name, func(t *testing.T) {
			if !cfg.ClipboardEnabled() {
				t.Fatal("OSC 52 clipboard unexpectedly disabled")
			}
		})
	}
	if cfg := (&TUI{Seams: TUISeams{Clipboard: boolPointer(false)}}); cfg.ClipboardEnabled() {
		t.Fatal("explicit OSC 52 clipboard opt-out was ignored")
	}
}

func TestTUISeamsAcceptDocumentedMixedValueBlock(t *testing.T) {
	var cfg TUI
	err := yaml.Unmarshal([]byte(`
seams:
  file_drop: true
  file_drop_max_bytes: 16777216
  inbox_dir: .yoloctl-inbox
  clipboard: true
  links: true
  notifications: badge
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ClipboardEnabled() {
		t.Fatal("clipboard was not parsed from the documented seams block")
	}
}

func TestTabTitleMaxGetters(t *testing.T) {
	var nilConfig *TUI
	for name, cfg := range map[string]*TUI{"nil": nilConfig, "empty": {}, "negative": {TabTitleMaxActive: -1, TabTitleMaxInactive: -1}} {
		t.Run(name, func(t *testing.T) {
			if got := cfg.GetTabTitleMaxActive(); got != DefaultTabTitleMaxActive {
				t.Errorf("active = %d, want default %d", got, DefaultTabTitleMaxActive)
			}
			if got := cfg.GetTabTitleMaxInactive(); got != DefaultTabTitleMaxInactive {
				t.Errorf("inactive = %d, want default %d", got, DefaultTabTitleMaxInactive)
			}
		})
	}
	cfg := &TUI{TabTitleMaxActive: 12, TabTitleMaxInactive: 60} // inactive > active is the user's call
	if got := cfg.GetTabTitleMaxActive(); got != 12 {
		t.Errorf("explicit active = %d, want 12", got)
	}
	if got := cfg.GetTabTitleMaxInactive(); got != 60 {
		t.Errorf("explicit inactive = %d, want 60", got)
	}
}

func TestGetBoxTitleTemplate(t *testing.T) {
	var nilConfig *TUI
	for name, cfg := range map[string]*TUI{
		"nil":   nilConfig,
		"empty": {},
		"blank": {Session: TUISession{BoxTitleTemplate: "   "}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cfg.GetBoxTitleTemplate(); got != DefaultBoxTitleTemplate {
				t.Errorf("template = %q, want default %q", got, DefaultBoxTitleTemplate)
			}
		})
	}
	// Explicit values keep their exact bytes — surrounding spaces may be
	// deliberate formatting.
	cfg := &TUI{Session: TUISession{BoxTitleTemplate: " {title} · {box} "}}
	if got := cfg.GetBoxTitleTemplate(); got != " {title} · {box} " {
		t.Errorf("explicit template = %q, want it verbatim", got)
	}
}

// A missing tui.yaml is materialized with the commented defaults so every
// knob is discoverable on disk.
func TestLoadTUIMaterializesDefaultFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := LoadTUI()
	if err != nil {
		t.Fatalf("LoadTUI: %v", err)
	}
	if got := cfg.GetTabTitleMaxActive(); got != DefaultTabTitleMaxActive {
		t.Errorf("fresh config active cap = %d, want %d", got, DefaultTabTitleMaxActive)
	}
	path, err := layout.TUIPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("tui.yaml was not materialized: %v", err)
	}
	if string(data) != defaultTUIYAML {
		t.Errorf("materialized file differs from defaultTUIYAML")
	}
	// The materialized file must load back to the same (all-default) config.
	reloaded, err := LoadTUI()
	if err != nil {
		t.Fatalf("reload materialized tui.yaml: %v", err)
	}
	if reloaded.GetPaletteKey() != cfg.GetPaletteKey() ||
		reloaded.GetTabTitleMaxActive() != cfg.GetTabTitleMaxActive() ||
		reloaded.GetBoxTitleTemplate() != cfg.GetBoxTitleTemplate() {
		t.Errorf("materialized file loads differently from the zero config")
	}
}

// An existing file is the user's: LoadTUI must never rewrite it (comments
// and formatting survive), even when fields are missing.
func TestLoadTUINeverRewritesExistingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := layout.TUIPath()
	if err != nil {
		t.Fatal(err)
	}
	const custom = "# mine\npalette_key: ctrl+q\n"
	if err := os.MkdirAll(path[:len(path)-len("/tui.yaml")], 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadTUI()
	if err != nil {
		t.Fatalf("LoadTUI: %v", err)
	}
	if got := cfg.GetPaletteKey(); got != "ctrl+q" {
		t.Errorf("palette key = %q, want ctrl+q", got)
	}
	if got := cfg.GetTabTitleMaxActive(); got != DefaultTabTitleMaxActive {
		t.Errorf("missing field did not fall back to default: %d", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Errorf("existing tui.yaml was rewritten:\n%s", data)
	}
}

// defaultTUIYAML spells every default out explicitly; this pins it to the
// real defaults so the materialized file cannot drift from the code.
func TestDefaultTUIYAMLMatchesDefaults(t *testing.T) {
	var parsed TUI
	if err := yaml.Unmarshal([]byte(defaultTUIYAML), &parsed); err != nil {
		t.Fatalf("defaultTUIYAML does not parse: %v", err)
	}
	var zero TUI
	if parsed.GetPaletteKey() != zero.GetPaletteKey() {
		t.Errorf("palette_key %q != default %q", parsed.GetPaletteKey(), zero.GetPaletteKey())
	}
	if parsed.GetPanePrefix() != zero.GetPanePrefix() {
		t.Errorf("pane_prefix %q != default %q", parsed.GetPanePrefix(), zero.GetPanePrefix())
	}
	if parsed.GetPaletteMinHeight() != "1" || parsed.GetPaletteMaxHeight() != "75%" {
		t.Errorf("palette heights %q/%q != documented defaults 1/75%%",
			parsed.GetPaletteMinHeight(), parsed.GetPaletteMaxHeight())
	}
	if parsed.GetPaletteBg() != "auto" {
		t.Errorf("palette_bg %q != auto", parsed.GetPaletteBg())
	}
	if parsed.ScrollbackLines != term.DefaultScrollbackLines {
		t.Errorf("scrollback_lines %d != term.DefaultScrollbackLines %d",
			parsed.ScrollbackLines, term.DefaultScrollbackLines)
	}
	if parsed.GetTabTitleMaxActive() != zero.GetTabTitleMaxActive() ||
		parsed.GetTabTitleMaxInactive() != zero.GetTabTitleMaxInactive() {
		t.Errorf("tab title caps %d/%d != defaults %d/%d",
			parsed.GetTabTitleMaxActive(), parsed.GetTabTitleMaxInactive(),
			zero.GetTabTitleMaxActive(), zero.GetTabTitleMaxInactive())
	}
	if parsed.Session.RingBufferBytes != session.DefaultRingBytes {
		t.Errorf("ring_buffer_bytes %d != session.DefaultRingBytes %d",
			parsed.Session.RingBufferBytes, session.DefaultRingBytes)
	}
	if parsed.GetBoxTitleTemplate() != zero.GetBoxTitleTemplate() {
		t.Errorf("box_title_template %q != default %q",
			parsed.GetBoxTitleTemplate(), zero.GetBoxTitleTemplate())
	}
	if !parsed.ClipboardEnabled() {
		t.Error("clipboard in the template is not the enabled default")
	}
	if !reflect.DeepEqual(parsed.AgentIcons, DefaultAgentIcons) {
		t.Errorf("agent_icons %v != DefaultAgentIcons %v", parsed.AgentIcons, DefaultAgentIcons)
	}
}

// Configured agent_icons entries replace the default of the same name,
// add new agents, and disable a default with an empty icon.
func TestGetAgentIcons(t *testing.T) {
	var nilConfig *TUI
	if got := nilConfig.GetAgentIcons(); !reflect.DeepEqual(got, DefaultAgentIcons) {
		t.Errorf("nil config icons = %v, want defaults", got)
	}
	cfg := &TUI{AgentIcons: map[string]AgentIcon{
		"claude":   {Icon: "C"},
		"codex":    {Icon: ""},
		"my-agent": {Icon: "MA", Dark: "#A8A8A8"},
	}}
	got := cfg.GetAgentIcons()
	if got["claude"] != (AgentIcon{Icon: "C"}) {
		t.Errorf("claude = %+v, want the configured entry", got["claude"])
	}
	if _, ok := got["codex"]; ok {
		t.Error("codex with an empty icon should be disabled")
	}
	if got["my-agent"].Icon != "MA" || got["gemini"] != DefaultAgentIcons["gemini"] || got["agy"].Icon != "⋏" {
		t.Errorf("icons = %v, want my-agent added and builtin icons kept", got)
	}
	if glyphs := cfg.AgentIconGlyphs(); glyphs["claude"] != "C" || glyphs["my-agent"] != "MA" || glyphs["codex"] != "" {
		t.Errorf("glyphs = %v", glyphs)
	}
}

func TestLegacyClipboardOptOutRemainsDisabled(t *testing.T) {
	for _, field := range []string{"osc52_clipboard", "image_paste"} {
		var cfg TUI
		if err := yaml.Unmarshal([]byte("seams:\n  clipboard: true\n  "+field+": false\n"), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.ClipboardEnabled() {
			t.Fatalf("legacy %s opt-out lost", field)
		}
	}
}

func TestDefaultBoxTitleBackslash(t *testing.T) {
	var parsed TUI
	if err := yaml.Unmarshal([]byte(defaultTUIYAML), &parsed); err != nil {
		t.Fatal(err)
	}
	for name, tpl := range map[string]string{"constant": DefaultBoxTitleTemplate, "yaml": parsed.GetBoxTitleTemplate()} {
		t.Run(name, func(t *testing.T) {
			got, strip, err := session.ExpandBoxTitleTemplate(tpl, "opus", "dev", "claude", "leosys", "✳")
			if want := `✳ leosys\opus: {title}`; err != nil || got != want || strip != "✳" {
				t.Fatalf("expanded default = %q, strip = %q, err = %v; want %q with one backslash", got, strip, err, want)
			}
		})
	}
}

func TestImagesSeamDefaultsOn(t *testing.T) {
	var cfg *TUI
	if !cfg.ImagesEnabled() || !(&TUI{}).ImagesEnabled() {
		t.Fatal("previews not on by default")
	}
	var off TUI
	if err := yaml.Unmarshal([]byte("seams:\n  images: false\n"), &off); err != nil || off.ImagesEnabled() {
		t.Fatalf("images: false not honoured: %v", err)
	}
	var def TUI
	if err := yaml.Unmarshal([]byte(defaultTUIYAML), &def); err != nil || def.Seams.Images == nil || !*def.Seams.Images {
		t.Fatal("default config does not spell images out")
	}
}
