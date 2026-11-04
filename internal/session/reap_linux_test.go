package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// reapHelperEnv makes the test binary a stand-in daemon: "plain" exits on
// SIGTERM, "drain" ignores the first SIGTERM and exits on the second (Codex's
// forced shutdown), "stubborn" ignores SIGTERM. It prints a line once its
// handlers are installed.
const reapHelperEnv = "YOLOCTL_REAP_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(reapHelperEnv); mode != "" {
		runReapHelper(mode)
	}
	os.Exit(m.Run())
}

func runReapHelper(mode string) {
	terms := make(chan os.Signal, 2)
	switch mode {
	case "drain":
		signal.Notify(terms, syscall.SIGTERM)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
	}
	fmt.Println("ready")
	if mode == "drain" {
		<-terms
		<-terms
		os.Exit(0)
	}
	select {}
}

// startReapHelper starts the test binary with argv as its command line.
func startReapHelper(t *testing.T, mode string, argv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Args = argv
	cmd.Env = append(os.Environ(), reapHelperEnv+"="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper %q did not start: %q, %v", argv, line, err)
	}
	return cmd
}

// waitExit returns how the helper ended, failing if it is still running.
func waitExit(t *testing.T, cmd *exec.Cmd) syscall.WaitStatus {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatal(err)
		}
		return cmd.ProcessState.Sys().(syscall.WaitStatus)
	case <-time.After(5 * time.Second):
		t.Fatalf("helper %q is still running", cmd.Args)
		return 0
	}
}

// The reaper stops every same-user process whose argv starts with a declared
// prefix, escalating from SIGTERM to a second SIGTERM to SIGKILL, and leaves
// near misses running. Its words are unique to this run, so nothing else on
// the machine can match.
func TestReapScriptStopsOnlyTheDeclaredDaemons(t *testing.T) {
	saved := [3]int{reapTermWait, reapForceWait, reapKillWait}
	t.Cleanup(func() { reapTermWait, reapForceWait, reapKillWait = saved[0], saved[1], saved[2] })
	reapTermWait, reapForceWait, reapKillWait = 5, 5, 20
	for _, shell := range []string{"sh", "bash"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			word := "yrt" + strings.ToLower(rand.Text()[:12])
			script, err := ReapScript([]string{word + " app-server"})
			if err != nil {
				t.Fatal(err)
			}
			plain := startReapHelper(t, "plain", "/nonexistent/"+word, "app-server", "--listen", "unix://")
			drain := startReapHelper(t, "drain", word, "app-server", "daemon", "pid-update-loop")
			stubborn := startReapHelper(t, "stubborn", "/x/"+word, "app-server")
			misses := []*exec.Cmd{
				startReapHelper(t, "plain", "/x/"+word+"x", "app-server"),
				startReapHelper(t, "plain", "/x/"+word, "app-serverx"),
				startReapHelper(t, "plain", "/x/"+word),
				startReapHelper(t, "plain", "node", "/x/"+word, "app-server"),
				startReapHelper(t, "plain", "/x/"+word+" app-server"),
			}
			begin := time.Now()
			out, err := exec.Command(shell, "-c", reaperCommand("", script)).CombinedOutput()
			if err != nil || len(out) != 0 {
				t.Fatalf("reaper = %v, output %q", err, out)
			}
			if elapsed := time.Since(begin); elapsed > 4*time.Second {
				t.Fatalf("the reaper took %v", elapsed)
			}
			if ws := waitExit(t, plain); ws.Signal() != syscall.SIGTERM {
				t.Fatalf("plain daemon ended with %v, want SIGTERM", ws)
			}
			if ws := waitExit(t, drain); !ws.Exited() || ws.ExitStatus() != 0 {
				t.Fatalf("draining daemon ended with %v, want its exit after the second SIGTERM", ws)
			}
			if ws := waitExit(t, stubborn); ws.Signal() != syscall.SIGKILL {
				t.Fatalf("stubborn daemon ended with %v, want SIGKILL", ws)
			}
			for _, m := range misses {
				if err := m.Process.Signal(syscall.Signal(0)); err != nil {
					t.Errorf("near miss %q was stopped: %v", m.Args, err)
				}
			}
		})
	}
}

// The reaper reads no input: typeahead queued before the agent starts
// reaches the agent.
func TestReapBeforeKeepsTypeahead(t *testing.T) {
	script, err := ReapScript([]string{"yrt" + strings.ToLower(rand.Text()[:12]) + " app-server"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", reapBefore(script, "exec cat"))
	cmd.Stdin = strings.NewReader("typed ahead\n")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "typed ahead\n" {
		t.Fatalf("agent read %q, %v", out, err)
	}
}

// stallingScan shadows tr and sleep with TERM-ignoring stand-ins carrying a
// marker, so the reaper's scan stalls past any budget. The stand-ins leave
// the caller's output pipe (and the reaper's fd 3 on it), so a regression
// that lets them survive fails the marker check instead of hanging a read.
func stallingScan(t *testing.T) (path, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = "yrt-stall-" + strings.ToLower(rand.Text()[:12])
	t.Cleanup(func() {
		for _, pid := range processesWith(t, marker) {
			var n int
			if _, err := fmt.Sscan(pid, &n); err == nil {
				_ = syscall.Kill(n, syscall.SIGKILL)
			}
		}
	})
	shim := "#!/bin/sh\ntrap '' TERM\nexec >/dev/null 2>&1 3>&-\nexec sleep 30 " + marker + "\n"
	if err := os.WriteFile(dir+"/tr", []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/sleep", []byte("#!/bin/sh\ntrap '' TERM\nexec >/dev/null 2>&1 3>&-\nwhile :; do sleep_is_shadowed=1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + ":" + os.Getenv("PATH"), marker
}

// The launch budget bounds the whole reaper, its /proc scan included, under
// either session shell: with a stalled, TERM-ignoring scan the agent still
// starts within the budget, the timeout is the only line reported (bash's
// own killed-command diagnostic is not), the agent's stderr is untouched,
// it inherits no fd 3, and no child of the reaper survives.
func TestReapBeforeIsBoundedAndCleansUpAStalledScan(t *testing.T) {
	saved := reapLaunchBudget
	t.Cleanup(func() { reapLaunchBudget = saved })
	reapLaunchBudget = 300 * time.Millisecond
	script, err := ReapScript([]string{"yrt" + strings.ToLower(rand.Text()[:12]) + " app-server"})
	if err != nil {
		t.Fatal(err)
	}
	agent := `echo agent started; echo agent stderr >&2; if [ -e /proc/self/fd/3 ]; then echo fd 3 leaked; fi`
	for _, shell := range []string{"sh", "bash"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			path, marker := stallingScan(t)
			cmd := exec.Command(shell, "-c", reapBefore(script, agent))
			cmd.Env = append(os.Environ(), "PATH="+path)
			begin := time.Now()
			out, err := cmd.CombinedOutput()
			elapsed := time.Since(begin)
			if want := "yoloctl: agent daemon reaper timed out\nagent started\nagent stderr\n"; err != nil || string(out) != want {
				t.Fatalf("launch = %v, output %q, want %q", err, out, want)
			}
			if limit := reapLaunchBudget + 500*time.Millisecond; elapsed > limit {
				t.Fatalf("the agent started after %v, past %v", elapsed, limit)
			}
			waitFor(t, "no stalled scan to survive", func() bool { return len(processesWith(t, marker)) == 0 })
		})
	}
}

// Under the managed PTY, as the holder runs it through bash: the timeout line
// reaches the terminal, nothing else of the reaper's does, and typeahead
// written before the agent starts is the agent's first input.
func TestReapBeforeUnderAPTYKeepsTypeahead(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	saved := reapLaunchBudget
	t.Cleanup(func() { reapLaunchBudget = saved })
	reapLaunchBudget = 300 * time.Millisecond
	script, err := ReapScript([]string{"yrt" + strings.ToLower(rand.Text()[:12]) + " app-server"})
	if err != nil {
		t.Fatal(err)
	}
	path, marker := stallingScan(t)
	cmd := exec.Command("bash", "-c", reapBefore(script, `read -r line; echo "agent read: $line"`))
	cmd.Env = append(os.Environ(), "PATH="+path)
	tty, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tty.Close() }()
	if _, err := tty.Write([]byte("typed ahead\n")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&out, tty); close(done) }()
	// Bound the run: a hung prefix is killed and fails below, never hangs.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		t.Fatal("the prefixed agent did not finish within 5 s")
	}
	// The copy ends at the terminal's last writer; past a grace, closing the
	// master ends it. Either way the reader is joined before out is read.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = tty.Close()
		<-done
	}
	got := strings.ReplaceAll(out.String(), "\r\n", "\n")
	if !strings.Contains(got, "yoloctl: agent daemon reaper timed out\n") || !strings.Contains(got, "agent read: typed ahead\n") ||
		strings.Contains(got, "Killed") || strings.Contains(got, reapDelimiter) {
		t.Fatalf("terminal output %q", got)
	}
	waitFor(t, "no stalled scan to survive", func() bool { return len(processesWith(t, marker)) == 0 })
}

// processesWith lists the PIDs whose command line contains word.
func processesWith(t *testing.T, word string) []string {
	t.Helper()
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var pids []string
	for _, d := range dirs {
		if raw, err := os.ReadFile("/proc/" + d.Name() + "/cmdline"); err == nil && strings.Contains(string(raw), word) {
			pids = append(pids, d.Name())
		}
	}
	return pids
}
