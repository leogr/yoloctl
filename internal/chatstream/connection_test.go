package chatstream

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// launcher runs the embedded client launcher with a fake AIRC that reports
// its argv, the inherited config and that config's link count.
func launcher(t *testing.T, published, nick string, prelude ...string) (stdout, stderr string, err error) {
	return launcherMode(t, published, nick, false, prelude...)
}

func launcherMode(t *testing.T, published, nick string, takeOver bool, prelude ...string) (stdout, stderr string, err error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the launcher's inherited /proc/self/fd descriptor is Linux-only")
	}
	python, lookErr := exec.LookPath("python3")
	if lookErr != nil {
		t.Skip("python3 is unavailable")
	}
	fake := filepath.Join(t.TempDir(), "airc")
	script := "#!/bin/sh\nprintf 'argv=%s\\n' \"$*\"\nprintf 'links=%s\\n' \"$(stat -L -c %h \"$2\")\"\ncat \"$2\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	argv := ClientArgv(python, fake, published, nick, takeOver)
	argv[3] = strings.Join(prelude, "\n") + "\n" + argv[3]
	cmd := exec.Command(python, argv[1:]...)
	cmd.Stdout, cmd.Stderr = &out, &errs
	err = cmd.Run()
	return out.String(), errs.String(), err
}

func TestClientLauncherReplacesOnlyTheNicknameInAnAnonymousCopy(t *testing.T) {
	dir := t.TempDir()
	// The exact go-toml shape the renderer publishes; the token file is
	// deliberately absent, so the launcher never needs to read it.
	raw, err := toml.Marshal(struct {
		Unix         string   `toml:"unix"`
		ArtifactUnix string   `toml:"artifact_unix"`
		Account      string   `toml:"account"`
		Nick         string   `toml:"nick"`
		TokenFile    string   `toml:"token_file"`
		Channels     []string `toml:"channels"`
	}{"/run/yoloctl/chat/endpoint/irc.sock", "endpoint/artifacts.sock", "mhumanaccount", "leogr", "token", []string{"#space"}})
	if err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(dir, "client.toml")
	if err := os.WriteFile(published, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := launcher(t, published, "leogr-ab2cd")
	if err != nil {
		t.Fatalf("launcher failed: %v: %s", err, stderr)
	}
	head, config, _ := strings.Cut(stdout, "\n")
	links, config, _ := strings.Cut(config, "\n")
	if !strings.HasPrefix(head, "argv=--config /proc/self/fd/") || !strings.HasSuffix(head, " --jsonl 2") {
		t.Fatalf("unexpected AIRC argv: %q", head)
	}
	if links != "links=0" {
		t.Fatalf("the private config must be an unlinked anonymous file: %q", links)
	}
	var derived map[string]any
	if err := toml.Unmarshal([]byte(config), &derived); err != nil {
		t.Fatalf("derived config is not TOML: %v", err)
	}
	want := map[string]any{"unix": "/run/yoloctl/chat/endpoint/irc.sock", "artifact_unix": filepath.Join(dir, "endpoint/artifacts.sock"),
		"account": "mhumanaccount", "nick": "leogr-ab2cd", "token_file": filepath.Join(dir, "token"), "channels": []any{"#space"}}
	if len(derived) != len(want) {
		t.Fatalf("derived config fields = %v", derived)
	}
	for key, value := range want {
		if got, ok := derived[key]; !ok || !equalValue(got, value) {
			t.Fatalf("%s = %v, want %v", key, derived[key], value)
		}
	}
	if after, _ := os.ReadFile(published); !bytes.Equal(after, raw) {
		t.Fatal("the published config changed")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the launcher left files beside the published config: %v", entries)
	}
}

// Lima's container overlay may reject O_TMPFILE. Exercise that failure even
// when the test host supports it, and verify the private copy is already
// unlinked and owner-only before any configuration bytes are written.
func TestClientLauncherWithoutOTmpfile(t *testing.T) {
	const prelude = `import errno, os, stat
real_open, real_write = os.open, os.write
def open_without_tmpfile(path, flags, mode=0o777, *, dir_fd=None):
    if flags & os.O_TMPFILE == os.O_TMPFILE:
        raise OSError(errno.EOPNOTSUPP, 'O_TMPFILE unavailable')
    return real_open(path, flags, mode, dir_fd=dir_fd)
def write_private(fd, data):
    info = os.fstat(fd)
    assert info.st_nlink == 0, 'configuration has a pathname'
    assert stat.S_IMODE(info.st_mode) == 0o600, 'configuration is not private'
    return real_write(fd, data)
os.open, os.write = open_without_tmpfile, write_private
`
	dir := t.TempDir()
	published := filepath.Join(dir, "client.toml")
	const config = "unix = '/run/irc.sock'\naccount = 'mhuman'\nnick = 'leogr'\ntoken_file = 'token'\nchannels = ['#space']\n"
	if err := os.WriteFile(published, []byte(config), 0o400); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := launcher(t, published, "leogr-ab2cd", prelude)
	if err != nil || stderr != "" {
		t.Fatalf("unsupported O_TMPFILE must still launch the client: %v: %s", err, stderr)
	}
	if !strings.Contains(stdout, "\nlinks=0\n") || !strings.Contains(stdout, `nick = "leogr-ab2cd"`) {
		t.Fatalf("client did not inherit the unlinked connection configuration: %q", stdout)
	}
	if after, err := os.ReadFile(published); err != nil || string(after) != config {
		t.Fatalf("published configuration changed: %v", err)
	}
}

func equalValue(a, b any) bool {
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok || bok {
		if !aok || !bok || len(as) != len(bs) {
			return false
		}
		for i := range as {
			if as[i] != bs[i] {
				return false
			}
		}
		return true
	}
	return a == b
}

func TestClientLauncherRefusesWithoutRevealingConfig(t *testing.T) {
	const marker = "secretmarker"
	valid := "unix = '/run/irc.sock'\naccount = 'm" + marker + "'\nnick = 'leogr'\ntoken_file = 'token'\nchannels = ['#space']\n"
	for _, tc := range []struct {
		name, config, nick, link string
	}{
		{name: "unknown-field", config: valid + "tls = false\n", nick: "leogr-ab2cd"},
		{name: "missing-token-file", config: strings.Replace(valid, "token_file = 'token'\n", "", 1), nick: "leogr-ab2cd"},
		{name: "non-string", config: strings.Replace(valid, "nick = 'leogr'", "nick = 7", 1), nick: "leogr-ab2cd"},
		{name: "control-byte", config: strings.Replace(valid, "'token'", "\"tok\\u0007en\"", 1), nick: "leogr-ab2cd"},
		{name: "invalid-toml", config: valid + "= " + marker + "\n", nick: "leogr-ab2cd"},
		{name: "oversized", config: valid + "#" + strings.Repeat(marker, 8<<10) + "\n", nick: "leogr-ab2cd"},
		{name: "symlink", config: valid, nick: "leogr-ab2cd", link: "client.toml"},
		{name: "bad-nick", config: valid, nick: "1" + marker},
		{name: "space-nick", config: valid, nick: "leo gr"},
		// Only the launcher's take-over mode may enable regain.
		{name: "published-regain", config: valid + "regain = true\n", nick: "leogr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			published := filepath.Join(dir, "client.toml")
			target := published
			if tc.link != "" {
				target = filepath.Join(dir, "real.toml")
				if err := os.Symlink(target, published); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := launcher(t, published, tc.nick)
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 || stdout != "" {
				t.Fatalf("launcher must refuse before starting AIRC: err=%v stdout=%q stderr=%q", err, stdout, stderr)
			}
			if !strings.HasPrefix(stderr, "managed chat client configuration: ") || strings.Count(stderr, "\n") != 1 || strings.Contains(stderr, marker) {
				t.Fatalf("refusal must be one fixed line without configuration bytes: %q", stderr)
			}
		})
	}
}

// Regain is added only to the private copy, only in take-over mode; any other
// mode is refused before AIRC starts.
func TestClientLauncherEnablesRegainOnlyForTakeOver(t *testing.T) {
	published := filepath.Join(t.TempDir(), "client.toml")
	const config = "unix = '/run/irc.sock'\naccount = 'mhuman'\nnick = 'leogr'\ntoken_file = 'token'\nchannels = ['#space']\n"
	if err := os.WriteFile(published, []byte(config), 0o400); err != nil {
		t.Fatal(err)
	}
	for _, takeOver := range []bool{false, true} {
		stdout, stderr, err := launcherMode(t, published, "leogr", takeOver)
		if err != nil {
			t.Fatalf("take-over=%v: %v: %s", takeOver, err, stderr)
		}
		_, derived, _ := strings.Cut(stdout, "\nlinks=0\n")
		var fields map[string]any
		if err := toml.Unmarshal([]byte(derived), &fields); err != nil {
			t.Fatalf("derived config is not TOML: %v: %q", err, derived)
		}
		if regain, ok := fields["regain"]; takeOver != ok || ok && regain != true || fields["nick"] != "leogr" {
			t.Fatalf("take-over=%v derived %v", takeOver, fields)
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	argv := ClientArgv(python, "/nonexistent/airc", published, "leogr", true)
	argv[len(argv)-1] = "regain"
	out, err := exec.Command(python, argv[1:]...).CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || string(out) != "managed chat client configuration: invalid connection mode\n" {
		t.Fatalf("an unknown mode must be refused: %v %q", err, out)
	}
}
