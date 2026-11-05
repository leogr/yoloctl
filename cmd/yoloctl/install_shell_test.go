package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallShellSetupFiles(t *testing.T) {
	home := "/home/u"
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	for _, c := range []struct {
		shell, key, value string
		rc, completion    string
		ok                bool
	}{
		{"/bin/zsh", "", "", "/home/u/.zshrc", "/home/u/.zshrc", true},
		{"/bin/zsh", "ZDOTDIR", "/home/u/.config/zsh", "/home/u/.config/zsh/.zshrc", "/home/u/.config/zsh/.zshrc", true},
		{"/bin/zsh", "ZDOTDIR", "relative", "", "", false},
		{"/usr/bin/bash", "", "", "/home/u/.bashrc", "/home/u/.bashrc", true},
		{"/usr/bin/fish", "", "", "/home/u/.config/fish/config.fish", "/home/u/.config/fish/completions/yoloctl.fish", true},
		{"/usr/bin/fish", "XDG_CONFIG_HOME", "/xdg", "/xdg/fish/config.fish", "/xdg/fish/completions/yoloctl.fish", true},
		{"/bin/tcsh", "", "", "", "", false},
	} {
		env = map[string]string{}
		if c.key != "" {
			env[c.key] = c.value
		}
		got, ok := installShellSetup(c.shell, home, getenv)
		if ok != c.ok || ok && (got.RC != c.rc || got.Completion != c.completion) {
			t.Errorf("%s %s=%s: %+v %v", c.shell, c.key, c.value, got, ok)
		}
	}
}

func TestBlockState(t *testing.T) {
	block := renderBlock("PATH", "export PATH=\"/x:$PATH\"")
	begin, end := blockMarkers("PATH")
	for _, c := range []struct {
		name, content string
		want          error
	}{
		{"absent", "alias ll='ls -l'\n", nil},
		{"same", "a\n\n" + block, errBlockPresent},
		{"other install", "a\n" + renderBlock("PATH", "export PATH=\"/y:$PATH\""), errBlockOther},
		{"begin only", begin + "\nx\n", errBlockBroken},
		{"end only", "x\n" + end + "\n", errBlockBroken},
		{"end first", end + "\n" + begin + "\n", errBlockBroken},
		{"repeated", block + block, errBlockBroken},
		{"other purpose", renderBlock("completion", "x"), nil},
	} {
		if got := blockState(c.content, "PATH", "export PATH=\"/x:$PATH\""); !errors.Is(got, c.want) || (c.want == nil) != (got == nil) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAddBlockAppendsAtomicallyAndRefusesUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	body := "export PATH=\"$HOME/.local/bin:$PATH\""
	created := filepath.Join(dir, "new", ".bashrc")
	if err := addBlock(created, "PATH", body); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(created); string(raw) != renderBlock("PATH", body) {
		t.Fatalf("created %q", raw)
	}
	if info, _ := os.Stat(created); info.Mode().Perm() != 0o644 {
		t.Fatalf("created mode %v", info.Mode())
	}
	rc := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(rc, []byte("setopt prompt_subst\nexport A=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addBlock(rc, "PATH", body); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(rc)
	if string(raw) != "setopt prompt_subst\nexport A=1\n\n"+renderBlock("PATH", body) {
		t.Fatalf("appended %q", raw)
	}
	if info, _ := os.Stat(rc); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode not kept: %v", info.Mode())
	}
	if err := addBlock(rc, "PATH", body); !errors.Is(err, errBlockPresent) {
		t.Fatalf("second add: %v", err)
	}
	if again, _ := os.ReadFile(rc); !bytes.Equal(again, raw) {
		t.Fatal("second add rewrote the file")
	}

	var manual *manualBlockError
	target := filepath.Join(dir, "dotfiles-zshrc")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := addBlock(link, "PATH", body); !errors.As(err, &manual) || !strings.Contains(manual.reason, "symlink to "+target) {
		t.Fatalf("symlink: %v", err)
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(target, hard); err != nil {
		t.Fatal(err)
	}
	if err := addBlock(hard, "PATH", body); !errors.As(err, &manual) || !strings.Contains(manual.reason, "hard link") {
		t.Fatalf("hard link: %v", err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "x\n" {
		t.Fatal("a refused file changed")
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, bytes.Repeat([]byte("#\n"), rcFileLimit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := addBlock(big, "PATH", body); !errors.As(err, &manual) {
		t.Fatalf("big: %v", err)
	}
}

// shellFixture is a temp HOME with bash as $SHELL and a bin dir not on PATH.
func shellFixture(t *testing.T) (home, bin, link string) {
	t.Helper()
	home = t.TempDir()
	bin = filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(bin, "yoloctl")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/usr/bin/bash")
	t.Setenv("PATH", "/usr/bin:/bin")
	return home, bin, link
}

func runShellSteps(t *testing.T, bin, link string, req shellRequest, answers string) (string, error) {
	t.Helper()
	return runShellStepsFrom(t.Context(), bin, link, req, strings.NewReader(answers))
}

func runShellStepsFrom(ctx context.Context, bin, link string, req shellRequest, in io.Reader) (string, error) {
	var out, prompts bytes.Buffer
	req.in, req.prompt = in, &prompts
	err := installShellSteps(ctx, installOut{w: &out}, bin, link, req)
	return out.String() + prompts.String(), err
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("terminal gone") }

// An unreadable answer or an interrupted command stops shell setup: the
// error is returned and nothing later is asked or written, even with a
// flag that would have written without asking.
func TestInstallShellStepsStopOnInputErrorOrInterrupt(t *testing.T) {
	home, bin, link := shellFixture(t)
	rc := filepath.Join(home, ".bashrc")
	_, err := runShellStepsFrom(t.Context(), bin, link, shellRequest{apply: true, interactive: true, completion: true}, failingReader{})
	if _, statErr := os.Stat(rc); err == nil || !strings.Contains(err.Error(), "terminal gone") || !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("input error: %v, rc %v", err, statErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, shell := range []string{"/usr/bin/bash", "/bin/tcsh"} {
		t.Setenv("SHELL", shell)
		for _, req := range []shellRequest{{apply: true, addPath: true, completion: true}, {}, {interactive: true}} {
			_, err = runShellStepsFrom(ctx, bin, link, req, strings.NewReader(""))
			if _, statErr := os.Stat(rc); !errors.Is(err, context.Canceled) || !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("interrupted %s %+v: %v, rc %v", shell, req, err, statErr)
			}
		}
	}
}

// Declining prints the line for the file setup resolved: ZDOTDIR's .zshrc,
// not ~/.zshrc.
func TestInstallShellStepsManualLineUsesResolvedFile(t *testing.T) {
	home, bin, link := shellFixture(t)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", filepath.Join(home, ".config", "zsh"))
	out, err := runShellSteps(t, bin, link, shellRequest{apply: true}, "")
	if err != nil || !strings.Contains(out, `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.config/zsh/.zshrc`) || strings.Contains(out, ">> ~/.zshrc") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Setenv("ZDOTDIR", "relative/zsh")
	out, err = runShellSteps(t, bin, link, shellRequest{apply: true}, "")
	if err != nil || strings.Contains(out, ".zshrc") || !strings.Contains(out, "add it in your shell's startup file") {
		t.Fatalf("an unusable ZDOTDIR names no guessed file: %v\n%s", err, out)
	}
	if got := manualPathCommand(shellSetup{Shell: "bash", RC: "/odd dir/.bashrc"}, "/x", "/odd dir/.bashrc"); got != `echo 'export PATH="/x:$PATH"' >> '/odd dir/.bashrc'` {
		t.Fatal(got)
	}
	if got := manualPathCommand(shellSetup{Shell: "fish"}, "$HOME/.local/bin", ""); got != `fish_add_path "$HOME/.local/bin"` {
		t.Fatal(got)
	}
}

// The exact zsh and fish blocks. They are sourced below only where those
// shells are installed.
func TestInstallShellBlockBodies(t *testing.T) {
	link := "/home/u/.local/bin/yoloctl"
	for _, c := range []struct{ got, want string }{
		{pathBlockBody("zsh", "$HOME/.local/bin"), `export PATH="$HOME/.local/bin:$PATH"`},
		{pathBlockBody("fish", "$HOME/.local/bin"), `fish_add_path -g "$HOME/.local/bin"`},
		{completionBlockBody("zsh", link), `(( $+functions[compdef] )) && source <(/home/u/.local/bin/yoloctl completion zsh)`},
		{completionBlockBody("bash", link), `[ -x /home/u/.local/bin/yoloctl ] && eval "$(/home/u/.local/bin/yoloctl completion bash)"`},
		{completionBlockBody("fish", link), `/home/u/.local/bin/yoloctl completion fish | source`},
		{completionBlockBody("bash", "/it's/yoloctl"), `[ -x '/it'\''s/yoloctl' ] && eval "$('/it'\''s/yoloctl' completion bash)"`},
		{completionBlockBody("zsh", "/it's/yoloctl"), `(( $+functions[compdef] )) && source <('/it'\''s/yoloctl' completion zsh)`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
}

func TestInstallShellStepsAskWriteOnceAndPrintOnly(t *testing.T) {
	home, bin, link := shellFixture(t)
	rc := filepath.Join(home, ".bashrc")

	out, err := runShellSteps(t, bin, link, shellRequest{apply: true}, "")
	if _, statErr := os.Stat(rc); err != nil || !errors.Is(statErr, os.ErrNotExist) || !strings.Contains(out, "is not on your PATH") {
		t.Fatalf("no terminal, no flags: print only\n%s", out)
	}
	out, err = runShellSteps(t, bin, link, shellRequest{addPath: true, completion: true}, "")
	if _, statErr := os.Stat(rc); err != nil || !errors.Is(statErr, os.ErrNotExist) || !strings.Contains(out, "Would add ~/.local/bin to PATH in ~/.bashrc.") || !strings.Contains(out, "Would add bash completion") {
		t.Fatalf("nothing installed: flags only describe\n%s", out)
	}
	out, err = runShellSteps(t, bin, link, shellRequest{apply: true, interactive: true}, "y\nn\n")
	raw, _ := os.ReadFile(rc)
	if err != nil || string(raw) != renderBlock("PATH", `export PATH="$HOME/.local/bin:$PATH"`) ||
		!strings.Contains(out, "Add ~/.local/bin to PATH in ~/.bashrc? [y/N]: ") || !strings.Contains(out, "Install bash completion for yoloctl in ~/.bashrc? [y/N]: ") {
		t.Fatalf("asked: PATH yes, completion no\n%s\n%s", out, raw)
	}
	out, err = runShellSteps(t, bin, link, shellRequest{apply: true, completion: true}, "")
	raw, _ = os.ReadFile(rc)
	if err != nil || !strings.Contains(string(raw), renderBlock("completion", completionBlockBody("bash", link))) || !strings.Contains(out, "is set on PATH in ~/.bashrc; open a new terminal") {
		t.Fatalf("--completion writes without asking\n%s\n%s", out, raw)
	}
	out, err = runShellSteps(t, bin, link, shellRequest{apply: true, interactive: true, addPath: true, completion: true}, "")
	again, _ := os.ReadFile(rc)
	if err != nil || !bytes.Equal(again, raw) || strings.Contains(out, "? [y/N]") || strings.Contains(out, "Added") {
		t.Fatalf("present blocks: no question, no rewrite\n%s", out)
	}
}

// A block written for another install is left alone: a warning with the
// block to use, failing only when that setup was explicitly requested.
func TestInstallShellStepsLeaveAnotherInstallsBlock(t *testing.T) {
	home, bin, link := shellFixture(t)
	rc := filepath.Join(home, ".bashrc")
	old := renderBlock("completion", completionBlockBody("bash", "/opt/old/yoloctl"))
	if err := os.WriteFile(rc, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runShellSteps(t, bin, link, shellRequest{apply: true, interactive: true}, "n\n")
	if err != nil || !strings.Contains(out, "has a yoloctl block for another install") || !strings.Contains(out, completionBlockBody("bash", link)) {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := runShellSteps(t, bin, link, shellRequest{apply: true, completion: true}, ""); err == nil {
		t.Fatal("a requested completion that cannot be written fails the command")
	}
	if raw, _ := os.ReadFile(rc); string(raw) != old {
		t.Fatal("another install's block changed")
	}
}

// The written bash blocks work when sourced: PATH gains the bin dir and the
// link's completion registers. zsh and fish run only where installed.
func TestInstallShellBlocksSourceInBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	home, bin, link := shellFixture(t)
	if err := os.WriteFile(link, []byte("#!/bin/sh\necho 'complete -W fake yoloctl'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".bashrc")
	for _, step := range []struct{ purpose, body string }{
		{"PATH", pathBlockBody("bash", "$HOME/.local/bin")},
		{"completion", completionBlockBody("bash", link)},
	} {
		if err := addBlock(rc, step.purpose, step.body); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bash, "--norc", "--noprofile", "-c", `source "$HOME/.bashrc"; echo "$PATH"; complete -p yoloctl`)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	raw, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(raw), bin+":") || !strings.Contains(string(raw), "complete -W 'fake' yoloctl") {
		t.Fatalf("%v\n%s", err, raw)
	}
}

// zsh: PATH always; completion registers only after compinit (silent
// before it). fish: PATH and the completions file. Skipped where the shell is
// not installed: string tests above are then the only check.
func TestInstallShellBlocksSourceInZshAndFish(t *testing.T) {
	for _, shell := range []string{"zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			exe, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable: its blocks were not sourced", shell)
			}
			home, bin, link := shellFixture(t)
			script := map[string]string{
				"zsh":  "#!/bin/sh\necho 'compdef _files yoloctl'\n",
				"fish": "#!/bin/sh\necho 'complete -c yoloctl -a fake'\n",
			}[shell]
			if err := os.WriteFile(link, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			setup, _ := installShellSetup("/bin/"+shell, home, func(string) string { return "" })
			if err := addBlock(setup.RC, "PATH", pathBlockBody(shell, "$HOME/.local/bin")); err != nil {
				t.Fatal(err)
			}
			if err := addBlock(setup.Completion, "completion", completionBlockBody(shell, link)); err != nil {
				t.Fatal(err)
			}
			env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
			run := func(args ...string) string {
				cmd := exec.Command(exe, args...)
				cmd.Env = env
				raw, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v\n%s", err, raw)
				}
				return string(raw)
			}
			if shell == "zsh" {
				before := run("-f", "-c", `source "$HOME/.zshrc"; print -r -- "$PATH"; (( $+_comps[yoloctl] )) && print registered || print silent`)
				after := run("-f", "-c", `autoload -Uz compinit && compinit -u -d "$HOME/.zcompdump"; source "$HOME/.zshrc"; (( $+_comps[yoloctl] )) && print registered || print silent`)
				if !strings.HasPrefix(before, bin+":") || !strings.Contains(before, "silent") || !strings.Contains(after, "registered") {
					t.Fatalf("before compinit:\n%s\nafter:\n%s", before, after)
				}
				return
			}
			out := run("-c", `source "$XDG_CONFIG_HOME/fish/config.fish"; echo $PATH[1]; source "$XDG_CONFIG_HOME/fish/completions/yoloctl.fish"; complete -c yoloctl`)
			if !strings.Contains(out, bin) || !strings.Contains(out, "fake") {
				t.Fatal(out)
			}
		})
	}
}
