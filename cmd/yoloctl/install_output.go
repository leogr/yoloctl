package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Install output roles use the TUI's dark theme colors
// (internal/tui/spaceview/theme.go newTheme and internal/tui/tui.go
// cOrange/cDim). Commands and versions use the terminal's own color in bold,
// which stays readable on light backgrounds.
const (
	roleOK    = 108 // ✓, running
	roleWarn  = 221 // !, the update label
	roleBad   = 203 // ✗
	roleBrand = 215 // space names
	roleLabel = 110 // paths
	roleDim   = 243 // explanations
	roleBold  = -1  // default color, bold
)

// installOut writes the install command's curated output: colored on a
// terminal, plain when NO_COLOR is set, TERM is dumb or the output is not a
// terminal. Text and glyphs are the same either way.
type installOut struct {
	w     io.Writer
	color bool
}

func newInstallOut(w io.Writer) installOut {
	return installOut{w: w, color: installColor(w)}
}

func installColor(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// paint styles text in a role; plain output returns it unchanged.
func (o installOut) paint(role int, text string) string {
	if !o.color || text == "" {
		return text
	}
	switch role {
	case roleBold:
		return "\x1b[1m" + text + "\x1b[0m"
	case roleBrand:
		return fmt.Sprintf("\x1b[1;38;5;%dm%s\x1b[0m", role, text)
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", role, text)
}

func (o installOut) line(parts ...string) { _, _ = fmt.Fprintln(o.w, strings.Join(parts, "")) }

func (o installOut) ok(parts ...string) {
	o.line(append([]string{o.paint(roleOK, "✓ ")}, parts...)...)
}

func (o installOut) warn(parts ...string) {
	o.line(append([]string{o.paint(roleWarn, "! ")}, parts...)...)
}

func (o installOut) bad(parts ...string) {
	o.line(append([]string{o.paint(roleBad, "✗ ")}, parts...)...)
}

// dim writes explanation text wrapped at width columns after indent.
func (o installOut) dim(indent int, text string) {
	pad := strings.Repeat(" ", indent)
	for _, row := range wrapWords(text, max(72-indent, 20)) {
		o.line(pad, o.paint(roleDim, row))
	}
}

// wrapWords splits text into rows of at most width runes, breaking at spaces.
func wrapWords(text string, width int) []string {
	var rows []string
	row := ""
	for _, word := range strings.Fields(text) {
		switch {
		case row == "":
			row = word
		case len([]rune(row))+1+len([]rune(word)) <= width:
			row += " " + word
		default:
			rows = append(rows, row)
			row = word
		}
	}
	if row != "" {
		rows = append(rows, row)
	}
	return rows
}

// shellArg quotes a word only when the shell would otherwise change it.
func shellArg(value string) string {
	if value != "" {
		plain := true
		for _, r := range value {
			if !plainPathRune(r) && r != '=' && r != ':' {
				plain = false
				break
			}
		}
		if plain {
			return value
		}
	}
	return shellWord(value)
}

// installCommand is a follow-up command line: the install link (never
// whatever yoloctl is first on PATH) and an explicit YOLOCTL_HOME, as the
// install found them.
func installCommand(link string, args ...string) string {
	words := []string{shellArg(link)}
	for _, arg := range args {
		words = append(words, shellArg(arg))
	}
	prefix := ""
	if home := os.Getenv("YOLOCTL_HOME"); home != "" {
		prefix = "YOLOCTL_HOME=" + shellArg(home) + " "
	}
	return prefix + strings.Join(words, " ")
}
