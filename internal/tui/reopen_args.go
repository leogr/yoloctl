package tui

import (
	"io"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/pflag"
)

// reopenView describes only view-only or explicitly interactive commands.
// No placement predicate, printed output or arbitrary argv grants replay.
type reopenView struct {
	args []string
	kind string
	// space is explicit when present; otherwise the launch path must retain
	// the resolved space before offering a later reopen.
	space           string
	positionalSpace bool
	spaceExplicit   bool
	// host is follow's explicit host-file target: no space applies, so it
	// is never pinned and needs no space to be replayed.
	host bool
}

func parseReopenView(args []string) (reopenView, bool) {
	v := reopenView{args: slices.Clone(args)}
	if len(args) == 0 {
		return reopenView{}, false
	}
	f := pflag.NewFlagSet("reopen", pflag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolP("help", "h", false, "")
	tail := args[1:]
	kind := ""
	switch args[0] {
	case "attach":
		kind = "attach"
		f.String("space", "", "")
		reopenAttachFlags(f)
	case "chat":
		kind = "chat"
		f.String("space", "", "")
		f.String("human", "", "")
		f.String("box", "", "")
		f.String("catalog-read", "single", "")
	case "follow":
		kind = "follow"
		f.String("space", "", "")
		f.Bool("host", false, "")
		f.String("format", "auto", "")
	case "space", "sp":
		if len(args) < 2 {
			return reopenView{}, false
		}
		kind = args[1]
		tail = args[2:]
		v.positionalSpace = true
		switch kind {
		case "view":
			f.Duration("interval", 0, "")
			f.Bool("no-motion", false, "")
		case "logs":
			f.BoolP("follow", "f", false, "")
			f.IntP("lines", "n", 100, "")
		case "ssh", "shell":
			f.Bool("start", false, "")
			f.Bool("root", false, "")
		default:
			return reopenView{}, false
		}
	case "box":
		if len(args) < 2 {
			return reopenView{}, false
		}
		kind = args[1]
		tail = args[2:]
		f.String("space", "", "")
		switch kind {
		case "logs":
			f.BoolP("follow", "f", false, "")
		case "attach":
			reopenAttachFlags(f)
		case "exec":
		default:
			return reopenView{}, false
		}
	default:
		return reopenView{}, false
	}
	if f.Parse(tail) != nil {
		return reopenView{}, false
	}
	if help, _ := f.GetBool("help"); help {
		return reopenView{}, false
	}
	pos := f.Args()
	if v.positionalSpace {
		if len(pos) > 1 {
			return reopenView{}, false
		}
		if len(pos) == 1 {
			v.space = pos[0]
			v.spaceExplicit = true
		}
		if (kind == "ssh" || kind == "shell") && f.ArgsLenAtDash() >= 0 && f.ArgsLenAtDash() != len(pos) {
			return reopenView{}, false
		}
	} else {
		v.space, _ = f.GetString("space")
		v.spaceExplicit = f.Changed("space")
		switch kind {
		case "attach":
			if len(pos) != 1 || layout.ValidateName("agentbox", pos[0]) != nil || f.Changed("box-use") {
				return reopenView{}, false
			}
		case "chat":
			if len(pos) != 0 {
				return reopenView{}, false
			}
		case "follow":
			// Mirror follow's own argument checks: one nonempty path, a known
			// format and --host never combined with --space.
			v.host, _ = f.GetBool("host")
			format, _ := f.GetString("format")
			if len(pos) != 1 || pos[0] == "" || v.host && v.spaceExplicit ||
				format != "auto" && format != "text" && format != "markdown" {
				return reopenView{}, false
			}
		case "logs":
			if len(pos) != 1 || layout.ValidateName("agentbox", pos[0]) != nil {
				return reopenView{}, false
			}
		case "exec":
			if len(pos) < 2 || layout.ValidateName("agentbox", pos[0]) != nil || !plainInteractiveShell(pos[1:]) {
				return reopenView{}, false
			}
		}
	}
	if kind == "logs" {
		if follow, _ := f.GetBool("follow"); !follow {
			return reopenView{}, false
		}
	}
	if v.spaceExplicit && layout.ValidateExistingSpaceName(v.space) != nil {
		return reopenView{}, false
	}
	v.kind = kind
	return v, true
}

func plainInteractiveShell(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "sh", "bash", "zsh", "fish",
		"/bin/sh", "/bin/bash", "/bin/zsh", "/bin/fish",
		"/usr/bin/sh", "/usr/bin/bash", "/usr/bin/zsh", "/usr/bin/fish",
		"/usr/local/bin/sh", "/usr/local/bin/bash", "/usr/local/bin/zsh", "/usr/local/bin/fish":
	default:
		return false
	}
	for _, arg := range args[1:] {
		if arg == "--login" || arg == "--interactive" {
			continue
		}
		if !strings.HasPrefix(arg, "-") || len(arg) < 2 || strings.Trim(arg[1:], "il") != "" {
			return false
		}
	}
	return true
}

// pinReopenArgs resolves only an implicit target of a strictly classified
// view. Explicit/invalid argv is unchanged; a failed context lookup is left
// to the original command to report, without offering unpinned replay.
func pinReopenArgs(args []string, resolve func() (string, error)) []string {
	v, ok := parseReopenView(args)
	if !ok || v.spaceExplicit || v.host {
		return slices.Clone(args)
	}
	space, err := resolve()
	if err != nil || layout.ValidateExistingSpaceName(space) != nil {
		return slices.Clone(args)
	}
	at := slices.Index(args, "--")
	if at < 0 {
		at = len(args)
	}
	out := slices.Clone(args[:at])
	if !v.positionalSpace {
		out = append(out, "--space")
	}
	out = append(out, space)
	return append(out, args[at:]...)
}

func reopenAttachFlags(f *pflag.FlagSet) {
	f.Bool("no-autoupdate", false, "")
	f.Bool("new-session", false, "")
	f.Bool("passthrough", false, "")
	f.Bool("box-use", false, "")
	f.String("chat-room-id", "", "")
	f.String("chat-member-id", "", "")
	f.String("chat-box-id", "", "")
	f.Bool("chat-allow-start", false, "")
}
