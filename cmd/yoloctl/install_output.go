package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Roles name instructional data spans. Log records receive their colours
// from the process logger; generated instructions carry no embedded styling.
const (
	roleOK = iota
	roleWarn
	roleBad
	roleBrand
	roleLabel
	roleDim
	roleBold
)

// installOut keeps generated instructions on their data writer. Result and
// warning records use the one process Logger and its shared colour policy.
type installOut struct{ w io.Writer }

func newInstallOut(w io.Writer) installOut { return installOut{w: w} }

// paint retains instructional text; styling belongs to log records.
func (o installOut) paint(_ int, text string) string { return text }

func (o installOut) line(parts ...string) { _, _ = fmt.Fprintln(o.w, strings.Join(parts, "")) }

func (o installOut) ok(parts ...string) {
	logging.L().LogAttrs(context.Background(), slog.LevelInfo, strings.Join(parts, ""),
		prettylog.Actor(logging.Install), slog.String(prettylog.KeyState, string(prettylog.StepOK)))
}

func (o installOut) warn(parts ...string) { cliWarn(logging.Install, "%s", strings.Join(parts, "")) }
func (o installOut) bad(parts ...string) {
	cliLog(slog.LevelError, logging.Install, "%s", strings.Join(parts, ""))
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
