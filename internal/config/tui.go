package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

// TUI is ~/.config/yoloctl/tui.yaml. Zero values mean "use default" —
// resolved through the getters, so a partial file stays valid.
type TUI struct {
	// PaletteKey is the focus-toggle keystroke in bubbletea's
	// canonical form ("ctrl+y" default). The H4 fallbacks (raw byte,
	// bitmasked modifiers) stay active only for the default.
	PaletteKey string `yaml:"palette_key,omitempty"`
	// PanePrefix is the pane prefix keystroke in the same form ("ctrl+q"
	// default). The TUI validates it against the palette key and falls back
	// when it does not parse or clashes.
	PanePrefix string `yaml:"pane_prefix,omitempty"`
	// ScrollbackLines caps each tab's captured scrollback.
	ScrollbackLines int `yaml:"scrollback_lines,omitempty"`
	// PaletteMinHeight and PaletteMaxHeight bound the palette area (the
	// status-bar row plus the overlay), which is otherwise sized to fit
	// its current content. Each is "" (default), an absolute row count
	// ("5"), or a percentage of the terminal height ("75%"). Defaults:
	// min 1 row, max 75%. The runtime applies hard rails — the area is
	// never below 1 row nor above 75% of the terminal height — and
	// resolves percentages against the live terminal size (in the TUI
	// model, like PaletteBg). With nothing to show the overlay collapses
	// to just the input line; once it shows content (suggestions or
	// command output) a trailing blank row separates it from the tab
	// strip below.
	PaletteMinHeight string `yaml:"palette_min_height,omitempty"`
	PaletteMaxHeight string `yaml:"palette_max_height,omitempty"`
	// PaletteBg is the "raised surface" background behind the palette
	// input row, the tab strip, and the open overlay — everything but
	// the active tab, so the active tab reads as connected to the
	// content below. "" / "auto" (default) detects the terminal
	// background and derives a raised shade from it (~15% of the channel
	// range — lighter on dark themes, darker on light); "off" / "none"
	// disables it; an integer 0-255 is a 256-color index; "#rrggbb" is
	// a truecolor value.
	PaletteBg string `yaml:"palette_bg,omitempty"`
	// TabTitleMaxActive and TabTitleMaxInactive cap tab-strip titles in
	// display columns (resolved through the getters; floor 1). The
	// active tab gets more room by default so the focused title reads
	// further while the rest stay compact.
	TabTitleMaxActive   int `yaml:"tab_title_max_active,omitempty"`
	TabTitleMaxInactive int `yaml:"tab_title_max_inactive,omitempty"`
	// AgentIcons maps an agent name to the {agent-icon} its box session
	// titles show, plus the active-tab color of that icon on dark and
	// light backgrounds. An entry replaces the built-in default of the
	// same name; an empty icon disables it. Resolved through
	// GetAgentIcons.
	AgentIcons map[string]AgentIcon `yaml:"agent_icons,omitempty"`
	Session    TUISession           `yaml:"session,omitempty"`
	Seams      TUISeams             `yaml:"seams,omitempty"`
}

// AgentIcon is one agent_icons entry. Icon is plain text — terminal
// titles cannot carry color — so Dark and Light ("#rrggbb") color it
// only where the TUI tab strip finds it leading the active tab's title. A blank or
// unparseable color leaves the icon in the tab's own color. Shared glyphs
// use the first agent name alphabetically. Icon text must not introduce a
// {title} placeholder during template expansion.
type AgentIcon struct {
	Icon  string `yaml:"icon"`
	Dark  string `yaml:"dark,omitempty"`
	Light string `yaml:"light,omitempty"`
}

// TUISession is the session-holder tuning block: values the host reads at
// holder start and forwards to the VM-side holder. Applied when a holder
// starts, never retroactively to a live session.
type TUISession struct {
	// RingBufferBytes sizes the session holder's replay ring.
	RingBufferBytes int `yaml:"ring_buffer_bytes,omitempty"`
	// BoxTitleTemplate seeds a BOX session's title with an empty {title},
	// then composes updates from the agent (OSC 0/1/2). Box sessions are
	// the only holder-managed sessions, and the placeholders are box
	// identity. Valid placeholders: {title} (the agent-set title, at
	// most once), {box}, {space}, {agent}, {account}, {agent-icon}; a
	// generic box's agent/account render as "none". With {agent-icon},
	// the holder also strips that icon from the front of each agent
	// title, so it does not show twice. Validation and expansion live in
	// internal/session.ExpandBoxTitleTemplate, next to the rewriter that
	// consumes the result. "{title}" alone disables composition.
	BoxTitleTemplate string `yaml:"box_title_template,omitempty"`
}

// TUISeams is the typed portion of the host-integration seam config that has
// shipped. Future seam fields belong here as their implementations land; yaml
// ignores the still-documented design placeholders in partial configs.
type TUISeams struct {
	// Clipboard controls all yoloctl clipboard integration, defaulting on.
	Clipboard *bool `yaml:"clipboard,omitempty"`
	// Browser is on (default), ask, or off, independent of clipboard policy.
	Browser string `yaml:"browser,omitempty"`
	// Images controls chat image previews, defaulting on, independently of
	// the clipboard policy.
	Images *bool `yaml:"images,omitempty"`
	// Legacy opt-outs remain deny-only to avoid silently enabling clipboard
	// access when loading an existing file. New configs use only clipboard.
	OSC52Clipboard *bool `yaml:"osc52_clipboard,omitempty"`
	ImagePaste     *bool `yaml:"image_paste,omitempty"`
}

func (t *TUI) GetPaletteKey() string {
	if t.PaletteKey == "" {
		return "ctrl+y"
	}
	return t.PaletteKey
}

// DefaultPanePrefix is the pane prefix when pane_prefix is unset.
const DefaultPanePrefix = "ctrl+q"

// GetPanePrefix returns the configured pane prefix, unvalidated; the TUI
// parses it and resolves a clash with the palette key.
func (t *TUI) GetPanePrefix() string {
	if t == nil || strings.TrimSpace(t.PanePrefix) == "" {
		return DefaultPanePrefix
	}
	return strings.TrimSpace(t.PanePrefix)
}

// GetPaletteBg returns the trimmed palette_bg spec ("" means auto).
// Classification (auto / off / index / hex) lives in the TUI model,
// since the auto path needs the runtime-detected terminal background.
func (t *TUI) GetPaletteBg() string {
	return strings.TrimSpace(t.PaletteBg)
}

// GetPaletteMinHeight / GetPaletteMaxHeight return the trimmed palette
// area bounds ("" means default). Parsing (rows vs %) and the hard rails
// live in the TUI model, which knows the live terminal height.
func (t *TUI) GetPaletteMinHeight() string { return strings.TrimSpace(t.PaletteMinHeight) }
func (t *TUI) GetPaletteMaxHeight() string { return strings.TrimSpace(t.PaletteMaxHeight) }

// Defaults for the getter-resolved fields below. Also spelled out in
// defaultTUIYAML, which materializes them into a fresh tui.yaml —
// TestDefaultTUIYAMLMatchesDefaults keeps the two in sync.
const (
	DefaultTabTitleMaxActive   = 40
	DefaultTabTitleMaxInactive = 30
	DefaultBoxTitleTemplate    = "{agent-icon} {account}\\{box}: {title}"
)

// DefaultAgentIcons is the built-in agent_icons registry. Icons are plain
// text from Dingbats, Geometric Shapes, Miscellaneous Symbols, Greek or
// ASCII — never emoji, whose width varies by terminal — and the colors
// echo each tool's own mark.
var DefaultAgentIcons = map[string]AgentIcon{
	"claude":    {Icon: "✳", Dark: "#D97757", Light: "#B9532F"},
	"codex":     {Icon: ">_", Dark: "#7B83FF", Light: "#4450E0"},
	"agy":       {Icon: "⋏", Dark: "#8AB4F8", Light: "#1967D2"},
	"gemini":    {Icon: "✦", Dark: "#4FA8F5", Light: "#0F6FC9"},
	"pi":        {Icon: "π", Dark: "#DCDCDC", Light: "#3C3C3C"},
	"copilot":   {Icon: "⚇", Dark: "#C07CF0", Light: "#9236CC"},
	"cursor":    {Icon: "⬢", Dark: "#FFFFFF", Light: "#111111"},
	"opencode":  {Icon: "OC", Dark: "#A8A8A8", Light: "#5E5E5E"},
	"ds4-agent": {Icon: "DS", Dark: "#E5566B", Light: "#B3243B"},
}

// GetTabTitleMaxActive / GetTabTitleMaxInactive return the tab-strip
// title caps in display columns. Unset or non-positive means default;
// any explicit positive value is honored (the strip's own oversize
// clamp still bounds a label by the live terminal width).
func (t *TUI) GetTabTitleMaxActive() int {
	if t == nil || t.TabTitleMaxActive <= 0 {
		return DefaultTabTitleMaxActive
	}
	return t.TabTitleMaxActive
}

func (t *TUI) GetTabTitleMaxInactive() int {
	if t == nil || t.TabTitleMaxInactive <= 0 {
		return DefaultTabTitleMaxInactive
	}
	return t.TabTitleMaxInactive
}

// GetBoxTitleTemplate returns the box session title template (blank
// means default). The raw value is preserved — leading/trailing spaces
// may be deliberate formatting — and is NOT validated here; the holder
// start path expands and validates it, warning and falling back to the
// default on a bad template.
func (t *TUI) GetBoxTitleTemplate() string {
	if t == nil || strings.TrimSpace(t.Session.BoxTitleTemplate) == "" {
		return DefaultBoxTitleTemplate
	}
	return t.Session.BoxTitleTemplate
}

// GetAgentIcons returns the effective agent_icons registry: the built-in
// defaults, each replaced by a configured entry of the same name, minus
// entries whose icon is empty (so `icon: ""` disables a default). The
// result is a fresh map.
func (t *TUI) GetAgentIcons() map[string]AgentIcon {
	icons := make(map[string]AgentIcon, len(DefaultAgentIcons))
	for name, icon := range DefaultAgentIcons {
		icons[name] = icon
	}
	if t != nil {
		for name, icon := range t.AgentIcons {
			icons[name] = icon
		}
	}
	for name, icon := range icons {
		if icon.Icon == "" {
			delete(icons, name)
		}
	}
	return icons
}

// AgentIconGlyphs is GetAgentIcons reduced to agent name -> icon, the
// part of the registry that travels to the VM-side session holder.
func (t *TUI) AgentIconGlyphs() map[string]string {
	glyphs := make(map[string]string)
	for name, icon := range t.GetAgentIcons() {
		glyphs[name] = icon.Icon
	}
	return glyphs
}

// ClipboardEnabled reports the shared host clipboard policy. Old opt-outs
// conservatively disable all integration until replaced by seams.clipboard.
func (t *TUI) ClipboardEnabled() bool {
	if t == nil {
		return true
	}
	for _, enabled := range []*bool{t.Seams.Clipboard, t.Seams.OSC52Clipboard, t.Seams.ImagePaste} {
		if enabled != nil && !*enabled {
			return false
		}
	}
	return true
}

// ImagesEnabled reports whether chat image previews may run: off means the
// TUI gives chat panes no image channel, so nothing is probed, downloaded
// or drawn. It applies when the TUI starts.
func (t *TUI) ImagesEnabled() bool {
	return t == nil || t.Seams.Images == nil || *t.Seams.Images
}

func (t *TUI) BrowserPolicy() string {
	if t == nil || t.Seams.Browser == "" {
		return "on"
	}
	if t.Seams.Browser == "on" || t.Seams.Browser == "off" {
		return t.Seams.Browser
	}
	// An unrecognized explicit policy must not silently authorize opening.
	return "ask"
}

// defaultTUIYAML is the commented template materialized into a fresh
// tui.yaml: every implemented field with its default spelled out, so the
// knobs are discoverable on disk. An
// existing file is never rewritten — user comments and formatting win —
// so fields added by later releases appear only in freshly created files.
const defaultTUIYAML = `# yoloctl TUI and session presentation config. Auto-created with defaults;
# edit values freely. A missing field falls back to its built-in default,
# so a partial file stays valid. yoloctl never rewrites this file.

# Palette focus-toggle keystroke (bubbletea canonical form); double-tap
# sends the literal key through to the active tab.
palette_key: ctrl+y

# Pane prefix: press it, then a pane key (| - split, arrows focus, z zoom,
# x detach, c n p digits w tabs, ? the full list). Pressed twice, it sends
# itself to the focused pane. ctrl+b suits tmux habits, at a cost: Claude
# Code's "run in background" and tmux inside a box then need a double tap.
# One equal to palette_key, or unparsable, falls back to ctrl+q (or ctrl+^).
pane_prefix: ctrl+q

# Per-tab captured scrollback lines (and the palette overlay buffer).
scrollback_lines: 10000

# Palette area floor/ceiling (incl. the status row): rows or "N%".
# Hard rails: never below 1 row, never above 75% of the terminal.
palette_min_height: 1
palette_max_height: 75%

# Chrome surface behind the palette and tab strip: auto (detect the
# terminal background) | off | <0-255> (256-color index) | "#rrggbb".
palette_bg: auto

# Tab-strip title caps in display columns; the active tab gets more room.
tab_title_max_active: 40
tab_title_max_inactive: 30

# {agent-icon} per agent name, for box session titles. icon is plain text;
# dark/light ("#rrggbb") color it in the active tab on dark/light terminal
# backgrounds. An entry replaces the built-in default of the same name;
# icon: "" disables one. Agents without an entry get no icon. Shared glyphs
# use the first agent name alphabetically for their color. Icon text must
# not introduce a {title} placeholder during template expansion.
agent_icons:
  claude: {icon: "✳", dark: "#D97757", light: "#B9532F"}
  codex: {icon: ">_", dark: "#7B83FF", light: "#4450E0"}
  agy: {icon: "⋏", dark: "#8AB4F8", light: "#1967D2"}
  gemini: {icon: "✦", dark: "#4FA8F5", light: "#0F6FC9"}
  pi: {icon: "π", dark: "#DCDCDC", light: "#3C3C3C"}
  copilot: {icon: "⚇", dark: "#C07CF0", light: "#9236CC"}
  cursor: {icon: "⬢", dark: "#FFFFFF", light: "#111111"}
  opencode: {icon: "OC", dark: "#A8A8A8", light: "#5E5E5E"}
  ds4-agent: {icon: "DS", dark: "#E5566B", light: "#B3243B"}

session:
  # Session holder replay-ring size in bytes. Applied when a session
  # starts, never retroactively.
  ring_buffer_bytes: 1048576
  # Box session title composition: the holder starts with an empty {title},
  # then applies each agent-set terminal title (OSC 0/1/2).
  # Placeholders: {title} (the agent-set title, at most once), {box},
  # {space}, {agent}, {account}, {agent-icon}; a generic box's
  # agent/account render as "none". An empty {agent-icon} also drops the
  # spaces after it. With {agent-icon} present, an agent title starting
  # with that icon loses it (and the spaces after it) on every update.
  # "{title}" alone disables composition. Applied when a session starts
  # (run / restart / recreate), never to a live session.
  box_title_template: "{agent-icon} {account}\\{box}: {title}"

seams:
  # All yoloctl clipboard integration: explicit copy, agent OSC 52 writes,
  # and Ctrl+V image paste. Native guest clipboard reads are not supported.
  # This does not change shortcuts handled directly by your host terminal.
  clipboard: true
  # Open a box's HTTP(S) links on this host: on (default) | ask | off.
  # "ask" opens a prompt with the destination; Ctrl+O opens, Esc cancels.
  browser: "on"
  # Chat image previews in Kitty and Ghostty (on by default). Off never
  # downloads an image or sends graphics to your terminal; chats keep their
  # file chips. Applies when the TUI starts.
  images: true
`

// LoadTUI reads tui.yaml. A missing file is materialized with explicit
// defaults first — best-effort: an unwritable config dir must not block
// the TUI or attach, and the zero config below IS those defaults, so a
// failed write changes nothing but the file's absence.
func LoadTUI() (*TUI, error) {
	var t TUI
	path, err := layout.TUIPath()
	if err != nil {
		return &t, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		_ = writeDefaultTUI(path)
		return &t, nil
	}
	if err != nil {
		return &t, err
	}
	if err := yaml.Unmarshal(data, &t); err != nil {
		return &t, fmt.Errorf("parse %s: %w", path, err)
	}
	return &t, nil
}

// writeDefaultTUI materializes defaultTUIYAML atomically. Its mode matches
// context.yaml because nothing in this file is secret.
func writeDefaultTUI(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), contextDirMode); err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(defaultTUIYAML), contextFileMode)
}
