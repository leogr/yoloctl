package boxendpoint

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestArgsRoundTrip(t *testing.T) {
	args := Args("/workdir/home/me/repo", "/run/openshell/ssh.sock")
	if args[0] != Binary || args[1] != Command {
		t.Fatalf("args = %q", args)
	}
	got, err := ParseArgs(args[2:])
	if err != nil {
		t.Fatal(err)
	}
	want := Options{Protocol: ProtocolVersion, Workdir: "/workdir/home/me/repo", SocketPath: "/run/openshell/ssh.sock"}
	if got != want {
		t.Fatalf("ParseArgs = %+v, want %+v", got, want)
	}
}

func TestParseArgsIsStrict(t *testing.T) {
	ok := []string{"--protocol", ProtocolVersion, "--workdir", "/workdir", "--ssh-socket-path", "/run/openshell/ssh.sock"}
	for name, args := range map[string][]string{
		"unknown flag":     append(slices.Clone(ok), "--mode", "process"),
		"duplicate flag":   append(slices.Clone(ok), "--workdir", "/workdir"),
		"incomplete":       append(slices.Clone(ok), "--workdir"),
		"missing protocol": ok[2:],
		"outside workdir":  {"--protocol", ProtocolVersion, "--workdir", "/tmp", "--ssh-socket-path", "/run/openshell/ssh.sock"},
		"unclean workdir":  {"--protocol", ProtocolVersion, "--workdir", "/workdir/a/../b", "--ssh-socket-path", "/run/openshell/ssh.sock"},
		"relative socket":  {"--protocol", ProtocolVersion, "--workdir", "/workdir", "--ssh-socket-path", "ssh.sock"},
		"missing socket":   ok[:4],
	} {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("%s: ParseArgs(%q) accepted", name, args)
		}
	}
	if _, err := ParseArgs(ok); err != nil {
		t.Fatal(err)
	}
}

// A replaced binary never serves a launch recorded for another protocol, but
// inspection can still read that launch's workdir.
func TestOtherProtocolIsParsedButNotServed(t *testing.T) {
	args := []string{"--protocol", "0", "--workdir", "/workdir/x", "--ssh-socket-path", "/run/openshell/ssh.sock"}
	launch, err := ParseLaunch(args)
	if err != nil || launch.Protocol != "0" || launch.Workdir != "/workdir/x" {
		t.Fatalf("ParseLaunch = %+v, %v", launch, err)
	}
	if _, err := ParseArgs(args); err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Fatalf("ParseArgs error = %v", err)
	}
}

func TestChildEnvironmentMatchesConfinedLaunch(t *testing.T) {
	env, err := ChildEnvironment("/home/sandbox", "sandbox", "/usr/bin:/bin", `{"ANTHROPIC_API_KEY":"secret","OPENSHELL_SANDBOX":"1","OPENSHELL_X":"y","HOME":"/override","YOLOCTL_CHAT_ROOM":"r"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ANTHROPIC_API_KEY=secret", "HOME=/override", "PATH=/usr/bin:/bin", "SHELL=/bin/bash", "USER=sandbox", "YOLOCTL_CHAT_ROOM=r"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
	for _, kv := range env {
		if strings.Contains(kv, UserEnvironmentVariable) || strings.HasPrefix(kv, "OPENSHELL_") {
			t.Fatalf("supervisor variable forwarded: %q", kv)
		}
	}
	if got := WithTerm(env, "dumb"); got[len(got)-1] != "TERM=dumb" || len(got) != len(env)+1 {
		t.Fatalf("WithTerm = %q", got)
	}
	injected, err := ChildEnvironment("/home/sandbox", "sandbox", "", `{"TERM":"vt100"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := WithTerm(injected, "xterm"); !slices.Contains(got, "TERM=vt100") || slices.Contains(got, "TERM=xterm") {
		t.Fatalf("injected TERM must win like OpenShell: %q", got)
	}
	if !slices.Contains(injected, "PATH=/usr/local/bin:/usr/bin:/bin") {
		t.Fatalf("default PATH missing: %q", injected)
	}
}

func TestChildEnvironmentRejectsMalformedChannel(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `{"A":1}`, `{"A=B":"x"}`, `{"":"x"}`, `{"A":"x\u0000y"}`, `{`, strings.Repeat(" ", maxUserEnvironment+1)} {
		if _, err := ChildEnvironment("/home/sandbox", "sandbox", "", raw); err == nil {
			t.Errorf("accepted %.40q", raw)
		} else if strings.Contains(err.Error(), "x") && strings.Contains(raw, "x\\u0000y") {
			t.Errorf("error leaks a value: %v", err)
		}
	}
}

func TestParseModes(t *testing.T) {
	encode := func(modes ...terminalMode) string {
		var b []byte
		for _, m := range modes {
			b = append(b, ssh.Marshal(struct {
				Op  byte
				Val uint32
			}{m.Opcode, m.Value})...)
		}
		return string(b)
	}
	raw := encode(terminalMode{ssh.ECHO, 0}, terminalMode{ssh.VINTR, 3}) + "\x00" + encode(terminalMode{ssh.ICANON, 0})
	got, err := parseModes(raw)
	if err != nil || !reflect.DeepEqual(got, []terminalMode{{ssh.ECHO, 0}, {ssh.VINTR, 3}}) {
		t.Fatalf("parseModes = %v, %v", got, err)
	}
	if got, err := parseModes(encode(terminalMode{ssh.ECHO, 1}) + "\xa0garbage"); err != nil || len(got) != 1 {
		t.Fatalf("opcode >= 160 must end parsing: %v, %v", got, err)
	}
	if _, err := parseModes(string([]byte{ssh.ECHO, 0, 0})); err == nil {
		t.Fatal("truncated mode accepted")
	}
	if _, err := parseModes(strings.Repeat(encode(terminalMode{ssh.ECHO, 1}), maxModeEntries+1)); err == nil {
		t.Fatal("unbounded modes accepted")
	}
}

func TestShellArgvMatchesConfinedLaunch(t *testing.T) {
	for _, tc := range []struct {
		command    string
		hasCommand bool
		pty        bool
		want       []string
	}{
		{"echo hi", true, false, []string{"/bin/bash", "-lc", "echo hi"}},
		{"echo hi", true, true, []string{"/bin/bash", "-lc", "echo hi"}},
		{"", false, true, []string{"/bin/bash", "-i"}},
		{"", false, false, []string{"/bin/bash"}},
	} {
		if got := shellArgv(tc.command, tc.hasCommand, tc.pty); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("shellArgv(%q, %v, %v) = %q", tc.command, tc.hasCommand, tc.pty, got)
		}
	}
}

func TestTerminalBounds(t *testing.T) {
	if !validTerm("xterm-256color") || !validTerm("") || validTerm("bad term") || validTerm("a=b") || validTerm(strings.Repeat("x", maxTermBytes+1)) {
		t.Fatal("terminal name validation")
	}
	c, r, x, y := clampWinsize(0, 70000, 1<<20, 0)
	if c != 1 || r != 0xffff || x != 0xffff || y != 0 {
		t.Fatalf("clamp = %d %d %d %d", c, r, x, y)
	}
}
