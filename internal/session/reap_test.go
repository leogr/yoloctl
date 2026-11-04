package session

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

// Daemon words reach the reaper's shell text, so only plain words pass.
func TestReapScriptRefusesUnsafeDaemons(t *testing.T) {
	if script, err := ReapScript(nil); script != "" || err != nil {
		t.Fatalf("no daemons = %q, %v; want no reaper", script, err)
	}
	for _, bad := range []string{"", "codex  app-server", " codex", "codex;rm", "codex $(id)", "/usr/bin/codex app-server",
		"-codex", "codex 'x'", "a b c d e f g h i"} {
		if _, err := ReapScript([]string{"codex app-server", bad}); err == nil {
			t.Errorf("daemon %q was accepted", bad)
		}
	}
	script, err := ReapScript([]string{"codex app-server", "other"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"codex${yr_nl}app-server${yr_nl}"*|"other${yr_nl}"*) return 0 ;;`; !strings.Contains(script, want) {
		t.Fatalf("script lacks the patterns %s:\n%s", want, script)
	}
}

// The reaper runs before the agent's exec in its own group with no input,
// and no reaper leaves the command unchanged.
func TestReapBeforePrefixesTheAgentCommand(t *testing.T) {
	if got := reapBefore("", "exec 'agent'"); got != "exec 'agent'" {
		t.Fatalf("without daemons the command changed: %q", got)
	}
	script, _ := ReapScript([]string{"codex app-server"})
	got := reapBefore(script, "exec 'agent'")
	head := "{ timeout -s KILL 5 sh -s 2>/dev/null <<'" + reapDelimiter + "'\n"
	if !strings.HasPrefix(got, head+script+reapDelimiter+"\n} 3>&2 2>/dev/null\n") || !strings.HasSuffix(got, "\nexec 'agent'") {
		t.Fatalf("command = %q", got)
	}
	// The here-document ends only at its delimiter.
	for _, l := range strings.Split(script, "\n") {
		if l == reapDelimiter {
			t.Fatalf("a script line ends the here-document early: %q", l)
		}
	}
}

func TestHolderUnitArgsCarryTheDeclaredDaemons(t *testing.T) {
	args, err := holderUnitArgs(UnitSpec{Agentbox: "cx", Argv: []string{"codex"}, Daemons: []string{"codex app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, []string{VMBinaryPath, "session-hold", "cx", "--reap-daemon", "codex app-server", "--", "codex"}) {
		t.Fatalf("args = %q", args)
	}
	if _, err := holderUnitArgs(UnitSpec{Agentbox: "cx", Argv: []string{"codex"}, Daemons: []string{"codex;id"}}); err == nil {
		t.Fatal("an unsafe daemon reached the holder's command line")
	}
}

// The holder stops the declared daemons as the prefix of the agent's command
// and again in a session of its own after the agent exits, before the
// tombstone records the exit.
func TestHolderReapsDeclaredDaemonsAroundTheAgent(t *testing.T) {
	opts, _ := testPaths(t)
	opts.Daemons = []string{"codex app-server"}
	script, err := ReapScript(opts.Daemons)
	if err != nil {
		t.Fatal(err)
	}
	fs := startFakeSandbox(t, opts.SSHSocket)
	tombstoneBefore := make(chan bool, 1)
	fs.later = func() {
		_, err := os.Stat(opts.TombstonePath)
		tombstoneBefore <- err == nil
	}
	res := runHolderAsync(context.Background(), opts)
	<-fs.started
	fs.exit(0)
	if r := <-res; r.err != nil || r.status != 0 {
		t.Fatalf("RunHolder = %d, %v", r.status, r.err)
	}
	fs.mu.Lock()
	execs := slices.Clone(fs.execs)
	fs.mu.Unlock()
	if len(execs) != 2 || execs[0] != reapBefore(script, `'agent' '--flag' 'it'\''s'`) || execs[1] != reaperCommand("", script) {
		t.Fatalf("execs = %q", execs)
	}
	if <-tombstoneBefore {
		t.Fatal("the tombstone was written before the daemons were stopped")
	}
	if _, err := readSessionFile[Tombstone](opts.TombstonePath); err != nil {
		t.Fatalf("no tombstone after the reap: %v", err)
	}
}

// A holder stop leaves the agent and its daemons running, and a lost
// connection proves no exit: neither reaps after the launch.
func TestHolderReapsOnlyAfterAnObservedExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*fakeSandbox, context.CancelFunc)
	}{
		{"holder stop", func(_ *fakeSandbox, cancel context.CancelFunc) { cancel() }},
		{"connection lost", func(fs *fakeSandbox, _ context.CancelFunc) { fs.closeStatusless() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := testPaths(t)
			opts.Daemons = []string{"codex app-server"}
			fs := startFakeSandbox(t, opts.SSHSocket)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			res := runHolderAsync(ctx, opts)
			<-fs.started
			tc.end(fs, cancel)
			<-res
			fs.mu.Lock()
			defer fs.mu.Unlock()
			if fs.sessions != 1 {
				t.Fatalf("%d sessions opened, want only the agent's: %q", fs.sessions, fs.execs)
			}
		})
	}
}

func TestHolderRefusesAnUnsafeDaemon(t *testing.T) {
	opts, _ := testPaths(t)
	opts.Daemons = []string{"codex;id"}
	if _, err := RunHolder(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("RunHolder = %v; want a refused daemon before any connection", err)
	}
}
