package mainlaunch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The launcher and the hand-over run in real shells: the launcher as the
// main process ("<first step> -c LauncherScript"), the hand-over with an
// exec-like environment and the record on stdin. A stand-in login shell
// records its argv, environment and PID; a wrapper env records the
// launcher's removal argv (names only).

const secret = "s3cr3t-value-that-must-not-leak"

type box struct {
	t       *testing.T
	home    string
	bin     string
	out     string
	inherit []string // the main process's inherited environment
	// loginShell is the launcher's $0: the stand-in login shell.
	loginShell string
}

func newBox(t *testing.T) *box {
	t.Helper()
	dir := t.TempDir()
	b := &box{t: t, home: filepath.Join(dir, "home"), bin: filepath.Join(dir, "bin"), out: filepath.Join(dir, "out")}
	for _, d := range []string{b.home, b.bin, b.out} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	realEnv, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env(1)")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	// Bash scripts: dash would drop non-identifier names on the way.
	b.script("env", "#!"+bash+"\nprintf '%s\\n' \"$@\" > \""+b.out+"/env-argv\"\nexec "+realEnv+" \"$@\"\n")
	b.loginShell = filepath.Join(b.bin, "login-shell")
	b.script("login-shell", "#!"+bash+"\numask > \""+b.out+"/umask\"\nprintf '%s' \"$2\" > \""+b.out+"/cmd\"\nprintf '%s\\n' \"$1\" > \""+b.out+"/flag\"\necho $$ > \""+b.out+"/pid\"\n"+realEnv+" -0 > \""+b.out+"/environ\"\n")
	path := b.bin + ":/usr/bin:/bin"
	b.inherit = []string{"PATH=" + path, "HOME=" + b.home, "SHELL=" + filepath.Join(b.bin, "login-shell"), "TERM=xterm-256color",
		"KEEP=kept", "SECRET=" + secret, "IMAGE_ONLY=extra", "IMG.ONLY=extra", "MY.VAR=dotted", "my-var=dashed"}
	return b
}

func (b *box) script(name, body string) {
	b.t.Helper()
	if err := os.WriteFile(filepath.Join(b.bin, name), []byte(body), 0o700); err != nil {
		b.t.Fatal(err)
	}
}

// launch starts the launcher with first ("sh" or "bash --posix").
func (b *box) launch(first []string) (*exec.Cmd, *bytes.Buffer) {
	b.t.Helper()
	if _, err := exec.LookPath(first[0]); err != nil {
		b.t.Skip("no " + first[0])
	}
	// $0 is the login shell, as the supervisor's argv supplies it.
	cmd := exec.Command(first[0], append(first[1:], "-c", LauncherScript, b.loginShell)...)
	cmd.Env = b.inherit
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	fifo := filepath.Join(b.home, LauncherDir, "fifo")
	for i := 0; ; i++ {
		if info, err := os.Lstat(fifo); err == nil && info.Mode()&os.ModeNamedPipe != 0 {
			break
		}
		if i > 500 {
			b.t.Fatalf("the launcher made no FIFO: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cmd, &stderr
}

// handover runs the hand-over command with env (an exec's environment).
func (b *box) handover(env []string, record []byte) int {
	b.t.Helper()
	cmd := exec.Command("/bin/sh", "-c", HandoverScript)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(record)
	out, err := cmd.CombinedOutput()
	if len(out) != 0 {
		b.t.Fatalf("the hand-over printed %q", out)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		b.t.Fatal(err)
	}
	return 0
}

func (b *box) execEnv() []string {
	return []string{"PATH=" + b.bin + ":/usr/bin:/bin", "HOME=" + b.home, "SHELL=" + filepath.Join(b.bin, "login-shell"), "TERM=dumb",
		"KEEP=other-value-ignored", "SECRET=" + secret, "MY.VAR=x", "my-var=y"}
}

func wait(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(10 * time.Second):
		t.Fatal("the launcher did not end")
	}
	return -1
}

func environNames(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, entry := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		name, value, _ := strings.Cut(entry, "=")
		out[name] = value
	}
	return out
}

var firsts = map[string][]string{"sh": {"/bin/sh"}, "bash-posix": {"bash", "--posix"}}

// Delivered: the command arrives byte for byte (trailing newlines kept), runs
// in the login shell as the launcher's own process, with exactly the exec's
// names and the launcher's inherited values, TERM as given; extras removed
// by name; no value in any argv; the FIFO and its directory are gone.
func TestLaunchDelivered(t *testing.T) {
	for label, first := range firsts {
		t.Run(label, func(t *testing.T) {
			b := newBox(t)
			launcher, stderr := b.launch(first)
			command := []byte("exec agent --flag 'multi\nline' \"$X\"\n\n")
			token, err := NewToken(command)
			if err != nil {
				t.Fatal(err)
			}
			record, err := Record("xterm-ghostty", token, command)
			if err != nil {
				t.Fatal(err)
			}
			if status := b.handover(b.execEnv(), record); status != HandoverDelivered {
				t.Fatalf("hand-over status %d", status)
			}
			if code := wait(t, launcher); code != 0 {
				t.Fatalf("launcher exit %d: %s", code, stderr.String())
			}
			got, _ := os.ReadFile(filepath.Join(b.out, "cmd"))
			flag, _ := os.ReadFile(filepath.Join(b.out, "flag"))
			pid, _ := os.ReadFile(filepath.Join(b.out, "pid"))
			if !bytes.Equal(got, command) || string(flag) != "-lc\n" || strings.TrimSpace(string(pid)) != strconv.Itoa(launcher.Process.Pid) {
				t.Fatalf("cmd %q flag %q pid %q (launcher %d)", got, flag, pid, launcher.Process.Pid)
			}
			env := environNames(t, filepath.Join(b.out, "environ"))
			if env["TERM"] != "xterm-ghostty" || env["KEEP"] != "kept" || env["SECRET"] != secret {
				t.Fatalf("environment %v", env)
			}
			for _, extra := range []string{"IMAGE_ONLY", "IMG.ONLY"} {
				if _, ok := env[extra]; ok {
					t.Fatalf("image-only %s reached the session", extra)
				}
			}
			argv, _ := os.ReadFile(filepath.Join(b.out, "env-argv"))
			// Identifier extras are unset by the shell; others go by name to env -u.
			if strings.Contains(string(argv), secret) || strings.Contains(string(argv), "IMAGE_ONLY") || !strings.Contains(string(argv), "-u\nIMG.ONLY\n") {
				t.Fatalf("env argv %q", argv)
			}
			if label == "bash-posix" {
				// Bash keeps non-identifier names: the dotted and dashed ones reach the session.
				if env["MY.VAR"] != "dotted" || env["my-var"] != "dashed" {
					t.Fatalf("non-identifier names lost under bash: %v", env)
				}
			}
			if _, err := os.Lstat(filepath.Join(b.home, LauncherDir)); !os.IsNotExist(err) {
				t.Fatal("the FIFO directory was kept", err)
			}
		})
	}
}

// A listed name the launcher does not have: one line naming it, non-zero,
// nothing run. No value is printed.
func TestLaunchMissingName(t *testing.T) {
	b := newBox(t)
	launcher, stderr := b.launch(firsts["sh"])
	token, _ := NewToken([]byte("exec agent"))
	record, _ := Record("xterm", token, []byte("exec agent"))
	if status := b.handover(append(b.execEnv(), "EXEC_ONLY=v"), record); status != HandoverDelivered {
		t.Fatalf("hand-over status %d", status)
	}
	if code := wait(t, launcher); code != LauncherMissingName {
		t.Fatalf("launcher exit %d", code)
	}
	if got := stderr.String(); got != "yoloctl: this box lacks the session variable EXEC_ONLY\n" {
		t.Fatalf("stderr %q", got)
	}
	if _, err := os.Stat(filepath.Join(b.out, "cmd")); !os.IsNotExist(err) {
		t.Fatal("the command ran")
	}
}

func TestHandoverAnswers(t *testing.T) {
	b := newBox(t)
	token, _ := NewToken([]byte("exec agent"))
	record, _ := Record("xterm", token, []byte("exec agent"))
	// No launcher waiting: no FIFO.
	if status := b.handover(b.execEnv(), record); status != HandoverNoFIFO {
		t.Fatalf("no FIFO: status %d", status)
	}
	// A name containing the token: nothing written, the launcher still waits.
	b.inherit = append(b.inherit, "TRICKY=kept-by-launcher")
	launcher, _ := b.launch(firsts["sh"])
	if status := b.handover(append(b.execEnv(), "X"+token+"=v"), record); status != HandoverNameHasToken {
		t.Fatalf("token in a name: status %d", status)
	}
	if launcher.ProcessState != nil {
		t.Fatal("the launcher ended")
	}
	// A value with a newline and a line equal to the token changes nothing.
	env := append(b.execEnv(), "TRICKY=a\n"+token+"\nFAKE=1")
	if status := b.handover(env, record); status != HandoverDelivered {
		t.Fatalf("tricky value: status %d", status)
	}
	if code := wait(t, launcher); code != 0 {
		t.Fatalf("launcher exit %d", code)
	}
}

// Without mkfifo the launcher waits for nothing (sleep) and the hand-over
// answers "no mkfifo".
func TestNoMkfifo(t *testing.T) {
	b := newBox(t)
	dir := t.TempDir()
	for _, tool := range []string{"sh", "cat", "printf", "sleep", "tr", "sed"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(dir, tool))
		}
	}
	env := []string{"PATH=" + dir, "HOME=" + b.home}
	cmd := exec.Command("/bin/sh", "-c", HandoverScript)
	cmd.Env = env
	cmd.Stdin = strings.NewReader("x")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != HandoverNoMkfifo {
		t.Fatalf("hand-over without mkfifo: %v", err)
	}
	launcher := exec.Command("/bin/sh", "-c", LauncherScript)
	launcher.Env = env
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = launcher.Process.Kill(); _, _ = launcher.Process.Wait() }()
	time.Sleep(300 * time.Millisecond)
	comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", launcher.Process.Pid))
	if strings.TrimSpace(string(comm)) != "sleep" {
		t.Fatalf("without mkfifo the launcher is %q, not sleep", comm)
	}
}

// A record cut at any byte, or with one token line after its header, is
// rejected: exit 93, nothing run.
func TestCutRecordsAreRejected(t *testing.T) {
	command := []byte("exec agent 'a\nb'\n")
	token, _ := NewToken(command)
	head, _ := Record("xterm", token, command)
	full := append(slices.Clone(head), []byte("HOME\nPATH\nSHELL\nTERM\n"+token+"\n")...)
	cases := map[string][]byte{"one token after the header": head}
	for i := 0; i < len(full); i++ {
		cases[fmt.Sprintf("cut at %d", i)] = full[:i]
	}
	for label, record := range cases {
		b := newBox(t)
		launcher, _ := b.launch(firsts["sh"])
		if err := os.WriteFile(filepath.Join(b.home, LauncherDir, "fifo"), record, 0); err != nil {
			t.Fatal(label, err)
		}
		if code := wait(t, launcher); code != LauncherBadRecord {
			t.Fatalf("%s: launcher exit %d", label, code)
		}
		if _, err := os.Stat(filepath.Join(b.out, "cmd")); !os.IsNotExist(err) {
			t.Fatalf("%s: the command ran", label)
		}
	}
}

func TestRecordRefusesATokenInTheCommand(t *testing.T) {
	token, _ := NewToken(nil)
	if _, err := Record("xterm", token, []byte("echo "+token)); err == nil {
		t.Fatal("a token inside the command was accepted")
	}
	if got, err := NewToken([]byte("x")); err != nil || len(got) != 32 {
		t.Fatal(got, err)
	}
}

// Names with a control character (a line break, 0x01) are set aside on both
// sides: the exec's are not listed (nothing missing), the launcher's are not
// removed.
func TestControlCharacterNamesAreSetAside(t *testing.T) {
	b := newBox(t)
	b.inherit = append(b.inherit, "IMG\x01CTL=kept")
	launcher, stderr := b.launch(firsts["bash-posix"])
	token, _ := NewToken([]byte("exec agent"))
	record, _ := Record("xterm", token, []byte("exec agent"))
	env := append(b.execEnv(), "NEW\nLINE=v", "SOH\x01NAME=v")
	if status := b.handover(env, record); status != HandoverDelivered {
		t.Fatalf("hand-over status %d", status)
	}
	if code := wait(t, launcher); code != 0 {
		t.Fatalf("launcher exit %d: %s", code, stderr.String())
	}
	if got := environNames(t, filepath.Join(b.out, "environ")); got["IMG\x01CTL"] != "kept" {
		t.Fatalf("a set-aside launcher name was removed: %v", got)
	}
	argv, _ := os.ReadFile(filepath.Join(b.out, "env-argv"))
	if strings.Contains(string(argv), "\x01") {
		t.Fatalf("a control-character name reached env -u: %q", argv)
	}
}

// An env that cannot remove by name: the non-identifier extra stays (the
// documented limit) and the session still starts.
func TestEnvWithoutUnsetKeepsTheExtra(t *testing.T) {
	b := newBox(t)
	bash, _ := exec.LookPath("bash")
	b.script("env", "#!"+bash+"\nif [ \"$1\" = -u ]; then exit 125; fi\nexec /usr/bin/env \"$@\"\n")
	launcher, stderr := b.launch(firsts["bash-posix"])
	token, _ := NewToken([]byte("exec agent"))
	record, _ := Record("xterm", token, []byte("exec agent"))
	if status := b.handover(b.execEnv(), record); status != HandoverDelivered {
		t.Fatalf("hand-over status %d", status)
	}
	if code := wait(t, launcher); code != 0 {
		t.Fatalf("launcher exit %d: %s", code, stderr.String())
	}
	env := environNames(t, filepath.Join(b.out, "environ"))
	if env["IMG.ONLY"] != "extra" {
		t.Fatalf("the extra was not kept: %v", env)
	}
	if _, ok := env["IMAGE_ONLY"]; ok {
		t.Fatal("an identifier extra was not unset by the shell")
	}
}

// The BASH_ENV control (addendum 1, acceptance 4a): with a hook that appends
// x to MARK, the launched command sees what today's single login launch
// (bash -lc from the exec's environment) sees, under either first step.
func TestBashEnvHookRunsAsToday(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	hook := filepath.Join(dir, "hook")
	if err := os.WriteFile(hook, []byte("export MARK=\"${MARK}x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mark := func(out string) string { return "printf %s \"$MARK\" > " + out }
	// Today: the server's login shell on the exec's environment.
	today := filepath.Join(dir, "today")
	base := []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "SHELL=" + bash, "BASH_ENV=" + hook, "ENV=" + hook}
	cmd := exec.Command(bash, "-lc", mark(today))
	cmd.Env = base
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	want, _ := os.ReadFile(today)
	t.Logf("today's login launch sees MARK=%q", want)
	for label, first := range firsts {
		t.Run(label, func(t *testing.T) {
			b := newBox(t)
			b.inherit = []string{"PATH=" + b.bin + ":/usr/bin:/bin", "HOME=" + b.home, "SHELL=" + bash, "TERM=dumb", "BASH_ENV=" + hook, "ENV=" + hook}
			b.loginShell = bash
			launcher, stderr := b.launch(first)
			out := filepath.Join(b.out, "mark")
			command := []byte(mark(out)) // evaluated by the login shell itself
			token, _ := NewToken(command)
			record, _ := Record("xterm", token, command)
			env := []string{"PATH=" + b.bin + ":/usr/bin:/bin", "HOME=" + b.home, "SHELL=" + bash, "TERM=dumb", "BASH_ENV=" + hook, "ENV=" + hook}
			if status := b.handover(env, record); status != HandoverDelivered {
				t.Fatalf("hand-over status %d", status)
			}
			if code := wait(t, launcher); code != 0 {
				t.Fatalf("launcher exit %d: %s", code, stderr.String())
			}
			got, _ := os.ReadFile(out)
			if string(got) != string(want) {
				t.Fatalf("MARK %q, today %q", got, want)
			}
		})
	}
}

// Review F1–F3 (fd919692): the session keeps the launcher's original umask;
// an inherited variable named like the script's own keeps its value; a
// declared SHELL is passed on as a value and never picks the executable.
func TestLaunchParityUmaskPrefixAndShell(t *testing.T) {
	for label, first := range firsts {
		t.Run(label, func(t *testing.T) {
			b := newBox(t)
			b.inherit = append(b.inherit, "__yoloctl_mb_term=public-original", "__yoloctl_mb_cmd=line1\nline2")
			for i, e := range b.inherit {
				if strings.HasPrefix(e, "SHELL=") {
					b.inherit[i] = "SHELL=/bin/false"
				}
			}
			cmd := exec.Command(first[0], append(first[1:], "-c", LauncherScript, b.loginShell)...)
			cmd.Env = b.inherit
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			oldMask := syscallUmask(0o022)
			err := cmd.Start()
			syscallUmask(oldMask)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
			waitFIFO(t, b)
			token, _ := NewToken([]byte("exec agent"))
			record, _ := Record("xterm", token, []byte("exec agent"))
			env := append(b.execEnv(), "__yoloctl_mb_term=public-original", "__yoloctl_mb_cmd=line1\nline2")
			for i, e := range env {
				if strings.HasPrefix(e, "SHELL=") {
					env[i] = "SHELL=/bin/false"
				}
			}
			if status := b.handover(env, record); status != HandoverDelivered {
				t.Fatalf("hand-over status %d", status)
			}
			if code := wait(t, cmd); code != 0 {
				t.Fatalf("launcher exit %d: %s", code, stderr.String())
			}
			mask, _ := os.ReadFile(filepath.Join(b.out, "umask"))
			got := environNames(t, filepath.Join(b.out, "environ"))
			if strings.TrimSpace(string(mask)) != "0022" {
				t.Fatalf("session umask %q, want 0022", mask)
			}
			if got["__yoloctl_mb_term"] != "public-original" || got["__yoloctl_mb_cmd"] != "line1\nline2" || got["SHELL"] != "/bin/false" || got["TERM"] != "xterm" {
				t.Fatalf("environment %q", got)
			}
		})
	}
}

func waitFIFO(t *testing.T, b *box) {
	t.Helper()
	fifo := filepath.Join(b.home, LauncherDir, "fifo")
	for i := 0; ; i++ {
		if info, err := os.Lstat(fifo); err == nil && info.Mode()&os.ModeNamedPipe != 0 {
			return
		}
		if i > 500 {
			t.Fatal("the launcher made no FIFO")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
