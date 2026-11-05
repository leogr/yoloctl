package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/leogr/yoloctl/internal/atomicfile"
)

// Shell setup at install: one marked block per purpose (PATH, completion),
// appended to the shell's own startup file. It is offered at a terminal
// (default No) or requested with --add-to-path / --completion; it never
// runs when nothing was installed. Other lines are never changed.

const rcFileLimit = 1 << 20

// shellSetup is where a supported shell keeps its startup files.
type shellSetup struct {
	Shell      string // zsh, bash or fish
	RC         string // the PATH block's file
	Completion string // the completion block's file
}

// installShellSetup resolves the startup files for $SHELL, honoring zsh's
// ZDOTDIR and fish's XDG_CONFIG_HOME. ok is false for any other shell, or an
// override that is not an absolute clean path (setup then prints only).
func installShellSetup(shell, home string, getenv func(string) string) (shellSetup, bool) {
	abs := func(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }
	if home == "" || !abs(home) {
		return shellSetup{}, false
	}
	switch filepath.Base(shell) {
	case "zsh":
		dir := home
		if z := getenv("ZDOTDIR"); z != "" {
			if !abs(z) {
				return shellSetup{}, false
			}
			dir = z
		}
		rc := filepath.Join(dir, ".zshrc")
		return shellSetup{Shell: "zsh", RC: rc, Completion: rc}, true
	case "bash":
		rc := filepath.Join(home, ".bashrc")
		return shellSetup{Shell: "bash", RC: rc, Completion: rc}, true
	case "fish":
		config := filepath.Join(home, ".config")
		if x := getenv("XDG_CONFIG_HOME"); x != "" {
			if !abs(x) {
				return shellSetup{}, false
			}
			config = x
		}
		return shellSetup{Shell: "fish", RC: filepath.Join(config, "fish", "config.fish"),
			Completion: filepath.Join(config, "fish", "completions", "yoloctl.fish")}, true
	}
	return shellSetup{}, false
}

func blockMarkers(purpose string) (begin, end string) {
	return "# >>> yoloctl " + purpose + " (added by yoloctl install) >>>", "# <<< yoloctl " + purpose + " <<<"
}

// pathBlockBody adds display (a $HOME/... or plain path, see installPathHint)
// to PATH.
func pathBlockBody(shell, display string) string {
	if shell == "fish" {
		return fmt.Sprintf("fish_add_path -g \"%s\"", display)
	}
	return fmt.Sprintf("export PATH=\"%s:$PATH\"", display)
}

// completionBlockBody loads the installed link's own completion, so it never
// depends on PATH order. zsh needs compinit to have run first.
func completionBlockBody(shell, link string) string {
	l := shellArg(link)
	switch shell {
	case "zsh":
		return fmt.Sprintf("(( $+functions[compdef] )) && source <(%s completion zsh)", l)
	case "bash":
		return fmt.Sprintf("[ -x %s ] && source <(%s completion bash)", l, l)
	}
	return l + " completion fish | source"
}

func renderBlock(purpose, body string) string {
	begin, end := blockMarkers(purpose)
	return begin + "\n" + body + "\n" + end + "\n"
}

var (
	errBlockPresent = errors.New("already set up")
	errBlockOther   = errors.New("has a yoloctl block for another install; replace it by hand")
	errBlockBroken  = errors.New("has an incomplete yoloctl block; fix or remove it by hand")
)

// manualBlockError is a refusal to edit a startup file: the caller prints
// the block for the user to add.
type manualBlockError struct{ reason string }

func (e *manualBlockError) Error() string { return e.reason }

// blockState reports whether content already holds this purpose's block:
// errBlockPresent for the same body, errBlockOther for another body,
// errBlockBroken for unmatched or repeated markers, nil when absent.
func blockState(content, purpose, body string) error {
	begin, end := blockMarkers(purpose)
	lines := strings.Split(content, "\n")
	b, e := -1, -1
	for i, line := range lines {
		switch strings.TrimRight(line, "\r") {
		case begin:
			if b >= 0 {
				return errBlockBroken
			}
			b = i
		case end:
			if e >= 0 {
				return errBlockBroken
			}
			e = i
		}
	}
	switch {
	case b < 0 && e < 0:
		return nil
	case b < 0 || e < 0 || e <= b:
		return errBlockBroken
	case strings.Join(lines[b+1:e], "\n") == body:
		return errBlockPresent
	}
	return errBlockOther
}

// readRC reads a startup file without following a symlink and refuses what
// an atomic replacement would silently change: another owner or more than
// one hard link. A missing file reads as empty.
func readRC(path string) (content string, info os.FileInfo, err error) {
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, &manualBlockError{err.Error()}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return "", nil, &manualBlockError{"is a symlink to " + target}
	}
	if !info.Mode().IsRegular() {
		return "", nil, &manualBlockError{"is not a regular file"}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if uint64(st.Nlink) > 1 {
			return "", nil, &manualBlockError{"has more than one hard link"}
		}
		if int(st.Uid) != os.Getuid() {
			return "", nil, &manualBlockError{"is not owned by you"}
		}
	}
	if info.Size() > rcFileLimit {
		return "", nil, &manualBlockError{"is larger than 1 MiB"}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, &manualBlockError{err.Error()}
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", nil, &manualBlockError{"changed while it was read"}
	}
	raw, err := io.ReadAll(io.LimitReader(f, rcFileLimit+1))
	if err != nil || len(raw) > rcFileLimit {
		return "", nil, &manualBlockError{"could not be read whole"}
	}
	return string(raw), info, nil
}

// checkBlock reports a startup file's block state without writing.
func checkBlock(path, purpose, body string) error {
	content, _, err := readRC(path)
	if err != nil {
		return err
	}
	return blockState(content, purpose, body)
}

// addBlock appends this purpose's block to path through an atomic
// replacement that keeps the file's mode. A missing file is created 0644
// (parents 0755). It never edits other lines.
func addBlock(path, purpose, body string) error {
	content, info, err := readRC(path)
	if err != nil {
		return err
	}
	if err := blockState(content, purpose, body); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	next := content
	if next != "" {
		if !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		next += "\n"
	}
	next += renderBlock(purpose, body)
	// The file must still be the one just read.
	now, err := os.Lstat(path)
	switch {
	case info == nil && !errors.Is(err, os.ErrNotExist),
		info != nil && (err != nil || !os.SameFile(info, now) || now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime())):
		return &manualBlockError{"changed while it was being updated"}
	}
	return atomicfile.Write(path, []byte(next), mode)
}

// shellRequest is what the install command was asked for: explicit flags,
// whether it may ask at a terminal, and whether it may write at all (never
// after a declined, print-only or dry-run install).
type shellRequest struct {
	addPath, completion bool
	interactive, apply  bool
	in                  io.Reader
	prompt              io.Writer
}

// installShellSteps offers the PATH and completion blocks after an install.
// The install itself already succeeded: a failure here is reported with the
// block to add by hand, and returned so the exit status says so.
func installShellSteps(o installOut, bin, link string, req shellRequest) error {
	home, _ := os.UserHomeDir()
	shell := os.Getenv("SHELL")
	setup, known := installShellSetup(shell, home, os.Getenv)
	tilde := func(p string) string {
		if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok && home != "" {
			return "~/" + rest
		}
		return p
	}
	ask := func(question string) bool {
		if !req.interactive || !req.apply {
			return false
		}
		ok, err := confirmInstall(newInstallOut(req.prompt).paint(roleDim, question), req.in, req.prompt)
		return err == nil && ok
	}
	failed := false
	manualLines := func(purpose, body string) {
		for _, line := range strings.Split(strings.TrimSuffix(renderBlock(purpose, body), "\n"), "\n") {
			o.line("    ", o.paint(roleBold, line))
		}
	}
	// existing reports a block that is not ours to rewrite; it fails the
	// command only when that setup was explicitly requested.
	existing := func(path, purpose, body string, state error, requested bool) {
		var manual *manualBlockError
		if errors.As(state, &manual) {
			o.warn(tilde(path), " ", manual.reason, "; add these lines yourself:")
		} else {
			o.warn(tilde(path), " ", state.Error(), ":")
		}
		manualLines(purpose, body)
		failed = failed || requested
	}
	apply := func(path, purpose, body, done string) {
		err := addBlock(path, purpose, body)
		var manual *manualBlockError
		var commit *atomicfile.CommitError
		switch {
		case err == nil:
			o.ok(done)
			return
		case errors.As(err, &commit):
			failed = true
			o.bad(tilde(path), " was updated, but saving its directory failed: ", commit.Err.Error())
			return
		case errors.As(err, &manual):
			o.bad(tilde(path), " ", manual.reason, "; add these lines yourself:")
		default:
			o.bad(tilde(path), ": ", err.Error(), "; add these lines yourself:")
		}
		failed = true
		manualLines(purpose, body)
	}

	text, command := installPathHint(bin, os.Getenv("PATH"), shell, home)
	if text != "" {
		display, plain := pathDisplay(bin, home)
		switch {
		case !known || !plain || command == "":
			o.line()
			o.warn(text)
			if command != "" {
				o.line("    ", o.paint(roleBold, command))
			}
		default:
			body := pathBlockBody(setup.Shell, display)
			state := checkBlock(setup.RC, "PATH", body)
			switch {
			case errors.Is(state, errBlockPresent):
				o.warn(tilde(bin), " is set on PATH in ", tilde(setup.RC), "; open a new terminal")
			case state != nil:
				o.line()
				existing(setup.RC, "PATH", body, state, req.addPath)
			case !req.apply && req.addPath:
				o.line(o.paint(roleDim, "Would add "+tilde(bin)+" to PATH in "+tilde(setup.RC)+"."))
			case req.addPath || ask("Add "+tilde(bin)+" to PATH in "+tilde(setup.RC)+"? [y/N]: "):
				apply(setup.RC, "PATH", body, "Added "+tilde(bin)+" to PATH in "+tilde(setup.RC)+" (open a new terminal)")
			default:
				o.line()
				o.warn(text)
				o.line("    ", o.paint(roleBold, command))
			}
		}
	}

	if !known {
		if req.completion {
			o.warn("completion setup supports zsh, bash and fish; for ", filepath.Base(shell), " see yoloctl completion --help")
		}
		return setupResult(failed)
	}
	body := completionBlockBody(setup.Shell, link)
	state := checkBlock(setup.Completion, "completion", body)
	switch {
	case errors.Is(state, errBlockPresent):
		// Already set up: nothing to ask or rewrite.
	case state != nil:
		if req.completion || req.interactive {
			existing(setup.Completion, "completion", body, state, req.completion)
		}
	case !req.apply:
		if req.completion {
			o.line(o.paint(roleDim, "Would add "+setup.Shell+" completion to "+tilde(setup.Completion)+"."))
		}
	case req.completion || ask("Install "+setup.Shell+" completion for yoloctl in "+tilde(setup.Completion)+"? [y/N]: "):
		apply(setup.Completion, "completion", body, "Added "+setup.Shell+" completion to "+tilde(setup.Completion)+" (open a new terminal)")
		if setup.Shell == "zsh" {
			o.dim(2, "It loads where compinit already ran (most setups, e.g. oh-my-zsh). Otherwise add `autoload -Uz compinit && compinit` above the yoloctl block.")
		}
	}
	return setupResult(failed)
}

func setupResult(failed bool) error {
	if failed {
		return asUserError(errors.New("shell setup incomplete; the install itself succeeded"))
	}
	return nil
}

// pathDisplay is bin as a PATH block writes it: $HOME/... under home, and
// plain is false when it needs shell quoting (then only a hint is printed).
func pathDisplay(bin, home string) (display string, plain bool) {
	for _, r := range bin {
		if !plainPathRune(r) {
			return "", false
		}
	}
	display = bin
	if rest, ok := strings.CutPrefix(bin, home+string(filepath.Separator)); ok && home != "" {
		display = "$HOME/" + rest
	}
	return display, true
}
