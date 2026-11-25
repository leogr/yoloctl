package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/followview"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/spf13/cobra"
)

func followSeams(t *testing.T) {
	t.Helper()
	oldGetwd := followGetwd
	oldHost, oldInteractive, oldRun, oldDefault, oldGrants, oldCommand := followHostAllowed, followInteractive, followRun, followDefault, followGrants, followCommand
	t.Cleanup(func() {
		followGetwd = oldGetwd
		followHostAllowed, followInteractive, followRun, followDefault, followGrants, followCommand = oldHost, oldInteractive, oldRun, oldDefault, oldGrants, oldCommand
	})
	followHostAllowed = func() error { return nil }
	followInteractive = func(*cobra.Command) bool { return true }
	t.Setenv("YOLOCTL_HOME", t.TempDir())
}
func executeFollow(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(t.Context())
}
func TestFollowPinsRelativeVMNameAndSpaceAtLaunch(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	var selected followview.Options
	ctx, err := config.LoadContext()
	if err != nil {
		t.Fatal(err)
	}
	ctx.Space = "first"
	if err := ctx.Save(); err != nil {
		t.Fatal(err)
	}
	followDefault = func(_ context.Context, space string) (string, error) {
		if space != "first" {
			t.Fatal("wrong launch space", space)
		}
		return root, nil
	}
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	for _, name := range []string{"notes.md", "~notes.md"} {
		if err := executeFollow(t, name); err != nil {
			t.Fatal(err)
		}
		if selected.Path != filepath.Join(root, name) || selected.Workdir != root || selected.Space != "first" || selected.Format != followview.Markdown {
			t.Fatal("target not pinned", selected)
		}
	}
	ctx.Space = "second"
	if err := ctx.Save(); err != nil {
		t.Fatal(err)
	}
	var readSpace string
	followGrants = func(_ context.Context, space string) ([]string, error) {
		readSpace = space
		return nil, errors.New("grant removed")
	}
	if _, err := selected.Reader.Read(t.Context(), followview.Query{}); err == nil || readSpace != "first" {
		t.Fatal("changed context redirected the view", readSpace, err)
	}
}
func TestHostFollowNeverResolvesASpaceOrVM(t *testing.T) {
	followSeams(t)
	var selected followview.Options
	followDefault = func(context.Context, string) (string, error) { t.Fatal("host read default workdir"); return "", nil }
	followGrants = func(context.Context, string) ([]string, error) { t.Fatal("host read grants"); return nil, nil }
	followCommand = func(context.Context, string, ...string) *exec.Cmd { t.Fatal("host opened VM transport"); return nil }
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("host"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executeFollow(t, "--host", path); err != nil {
		t.Fatal(err)
	}
	if selected.Space != "" || selected.Path != path {
		t.Fatal(selected)
	}
	s, err := selected.Reader.Read(t.Context(), followview.Query{})
	if err != nil || string(s.Data) != "host" {
		t.Fatal(s, err)
	}
	if !readOnlyStartup([]string{"follow", "--host", path}) {
		t.Fatal("follow initialized VM home before host mode")
	}
	if selected.Mermaid == nil {
		t.Fatal("markdown.mermaid defaults on, but follow draws no diagrams")
	}
	tuiPath, err := layout.TUIPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tuiPath, []byte("markdown:\n  mermaid: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := executeFollow(t, "--host", path); err != nil {
		t.Fatal(err)
	}
	if selected.Mermaid != nil {
		t.Fatal("markdown.mermaid: false still draws diagrams")
	}
}
func TestFollowValidationPrecedesReaderAndHostEffects(t *testing.T) {
	followSeams(t)
	followHostAllowed = func() error { t.Fatal("invalid argv reached host/controller selection"); return nil }
	for _, args := range [][]string{{"--host", "--space", "s", "file"}, {"--format", "html", "file"}, {}, {"one", "two"}, {""}} {
		if err := executeFollow(t, args...); err == nil {
			t.Fatal("invalid arguments accepted", args)
		}
	}
}
func TestFollowCompletionUsesVMListAndNeverHostFallback(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guest-not-really-host"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	followGrants = func(context.Context, string) ([]string, error) { return []string{root}, nil }
	followCommand = func(ctx context.Context, space string, args ...string) *exec.Cmd {
		if args[0] != "python3" || len(args) != 3 || args[1] != "-c" {
			t.Fatal("not a fixed script request", args)
		}
		return exec.CommandContext(ctx, "python3", "-c", `print('{"entries":["guest-only"]}')`)
	}
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	if err := cmd.Flags().Set("space", "fixed"); err != nil {
		t.Fatal(err)
	}
	out, directive := cmd.ValidArgsFunction(cmd, nil, root+"/g")
	if len(out) != 1 || out[0] != root+"/guest-only" || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("not VM-based completion", out, directive)
	}
	followGrants = func(context.Context, string) ([]string, error) { return nil, errors.New("VM stopped") }
	out, directive = cmd.ValidArgsFunction(cmd, nil, root+"/g")
	if len(out) != 0 || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("unavailable VM fell back to host", out, directive)
	}
}
func TestFollowCompletionKeepsGrantRootsWithoutWorkdirAndHostIsExplicit(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	followGrants = func(context.Context, string) ([]string, error) { return []string{root}, nil }
	followDefault = func(context.Context, string) (string, error) {
		return "", errors.New("settings unavailable")
	}
	followCommand = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("unavailable workdir reached the guest reader")
		return nil
	}
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	out, directive := cmd.ValidArgsFunction(cmd, nil, "")
	if len(out) != 1 || !strings.HasPrefix(out[0], root+"/") || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal(out, directive)
	}
	if err := os.WriteFile(filepath.Join(root, "host-only"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("host", "true"); err != nil {
		t.Fatal(err)
	}
	out, directive = cmd.ValidArgsFunction(cmd, nil, root+"/host-")
	if len(out) != 1 || out[0] != root+"/host-only" || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal(out, directive)
	}
}
func TestFollowCompletionOpensWorkdirRelativePath(t *testing.T) {
	followSeams(t)
	tree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	grant := filepath.Join(tree, "leogr")
	workdir := filepath.Join(grant, "workdir")
	path := filepath.Join(workdir, "yoloctl", ".yolo-team", "PROGRESS.md")
	outside := filepath.Join(tree, "outside")
	for _, dir := range []string{filepath.Dir(path), outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte("# Progress\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, "shortcut")); err != nil {
		t.Fatal(err)
	}
	followDefault = func(_ context.Context, space string) (string, error) {
		if space != "fixed" {
			t.Fatalf("default workdir: unexpected space %q", space)
		}
		return workdir, nil
	}
	followGrants = func(_ context.Context, space string) ([]string, error) {
		if space != "fixed" {
			t.Fatalf("grants: unexpected space %q", space)
		}
		return []string{grant}, nil
	}
	followCommand = func(ctx context.Context, space string, args ...string) *exec.Cmd {
		if space != "fixed" {
			t.Fatalf("guest reader: unexpected space %q", space)
		}
		return exec.CommandContext(ctx, args[0], args[1:]...)
	}

	cases := []struct {
		token string
		want  []string
		dir   bool
	}{
		{"", []string{"yoloctl/", cobra.CompletionWithDesc(grant+"/", "space grant")}, true},
		{"y", []string{"yoloctl/"}, true},
		{"./y", []string{"./yoloctl/"}, true},
		{"yoloctl/", nil, false},
		{"yoloctl/.", []string{"yoloctl/.yolo-team/"}, true},
		{"yoloctl/.yolo-team/", []string{"yoloctl/.yolo-team/PROGRESS.md"}, false},
		{"../../outside/", nil, false},
		{"shortcut/", nil, false},
	}
	var completed string
	for _, tc := range cases {
		var out bytes.Buffer
		root := &cobra.Command{Use: "yoloctl"}
		root.AddCommand(newFollowCmd())
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"__complete", "follow", "--space", "fixed", tc.token})
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		got, directive := lines[:len(lines)-1], lines[len(lines)-1]
		wantDirective := cobra.ShellCompDirectiveNoFileComp
		if tc.dir {
			wantDirective |= cobra.ShellCompDirectiveNoSpace
		}
		if !slices.Equal(got, tc.want) || directive != ":"+strconv.Itoa(int(wantDirective)) {
			t.Errorf("complete %q: got %q %s, want %q :%d", tc.token, got, directive, tc.want, wantDirective)
		}
		if tc.token == "yoloctl/.yolo-team/" && len(got) == 1 {
			completed = got[0]
		}
	}
	if completed == "" {
		t.Fatal("missing final relative candidate")
	}
	launched := false
	followRun = func(ctx context.Context, opts followview.Options) error {
		launched = true
		if opts.Path != path || opts.Workdir != workdir || opts.Space != "fixed" {
			t.Fatalf("wrong completion target: %+v", opts)
		}
		snapshot, err := opts.Reader.Read(ctx, followview.Query{})
		if err != nil {
			return err
		}
		if string(snapshot.Data) != "# Progress\n" {
			t.Fatalf("wrong file content: %q", snapshot.Data)
		}
		return nil
	}
	if err := executeFollow(t, "--space", "fixed", completed); err != nil || !launched {
		t.Fatalf("completed path did not open: launched=%t, err=%v", launched, err)
	}
}

func TestFollowRejectsRelativeVMPathWithoutAbsoluteWorkdir(t *testing.T) {
	followSeams(t)
	grant := t.TempDir()
	followGrants = func(context.Context, string) ([]string, error) { return []string{grant}, nil }
	followCommand = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("invalid workdir reached the guest reader")
		return nil
	}
	followRun = func(context.Context, followview.Options) error {
		t.Fatal("invalid workdir launched a view")
		return nil
	}
	for _, base := range []string{"", "relative/workdir"} {
		followDefault = func(context.Context, string) (string, error) { return base, nil }
		cmd := newFollowCmd()
		cmd.SetContext(t.Context())
		out, directive := cmd.ValidArgsFunction(cmd, nil, "notes")
		if len(out) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("invalid workdir %q offered completion: %v %v", base, out, directive)
		}
		if err := executeFollow(t, "notes.md"); err == nil || !strings.Contains(err.Error(), "default workdir") {
			t.Errorf("relative follow with workdir %q: %v", base, err)
		}
	}
}

func TestFollowIsHostOnlyAndItsFlagsHaveCompleters(t *testing.T) {
	followSeams(t)
	root := newClientRootCmd()
	cmd, _, err := root.Find([]string{"follow"})
	if err != nil || cmd.Name() != "follow" {
		t.Fatal(err)
	}
	for _, flag := range []string{"space", "format"} {
		if _, ok := cmd.GetFlagCompletionFunc(flag); !ok {
			t.Fatal("missing flag completer", flag)
		}
	}
	worker := newRootCmd()
	if cmd, _, _ := worker.Find([]string{"follow"}); cmd != worker {
		t.Fatal("native worker exposed follow")
	}
	followHostAllowed = func() error { return errors.New("not a host") }
	followRun = func(context.Context, followview.Options) error { t.Fatal("host refusal bypassed"); return nil }
	if err := executeFollow(t, "--host", "file"); err == nil || !strings.Contains(err.Error(), "not a host") {
		t.Fatal(err)
	}
	// Help is available without a VM or interactive terminal.
	cmd = newFollowCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil || !strings.Contains(buf.String(), "1 MiB") {
		t.Fatal(err, buf.String())
	}
}

func TestFollowCompletionAdvancesFromGrantRootIntoGuestChildren(t *testing.T) {
	followSeams(t)
	root := t.TempDir()
	// A nested grant must not turn selection of the parent root back into a
	// grant-prefix suggestion instead of listing the selected directory.
	followGrants = func(context.Context, string) ([]string, error) {
		return []string{root, filepath.Join(root, "nested")}, nil
	}
	calls := 0
	followCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, "python3", "-c", `print('{"entries":["notes.md"]}')`)
	}
	cmd := newFollowCmd()
	cmd.SetContext(t.Context())
	out, directive := cmd.ValidArgsFunction(cmd, nil, root)
	if calls != 0 || len(out) == 0 || out[0] != cobra.CompletionWithDesc(root+"/", "space grant") || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("partial root did not complete without a guest read", calls, out, directive)
	}
	out, directive = cmd.ValidArgsFunction(cmd, nil, root+"/")
	if calls != 1 || len(out) != 1 || out[0] != root+"/notes.md" || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("selected grant root did not list guest children", calls, out, directive)
	}
	followGrants = func(context.Context, string) ([]string, error) { return nil, errors.New("grant removed") }
	out, directive = cmd.ValidArgsFunction(cmd, nil, root+"/")
	if calls != 1 || len(out) != 0 || directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Fatal("removed grant reached the reader or fell back to host completion", calls, out, directive)
	}
}

func TestFollowPinsTitleBaseForAbsoluteVMPaths(t *testing.T) {
	followSeams(t)
	base := filepath.Join(t.TempDir(), "project")
	followDefault = func(_ context.Context, space string) (string, error) {
		if space != "fixed" {
			t.Fatalf("selected space %q", space)
		}
		return base, nil
	}
	var selected followview.Options
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	for _, path := range []string{filepath.Join(base, "notes.md"), filepath.Join(base, "..", "other.md")} {
		if err := executeFollow(t, "--space", "fixed", path); err != nil {
			t.Fatal(err)
		}
		if selected.Workdir != base || selected.Path != path {
			t.Fatalf("title base or pinned reader path differs: %+v", selected)
		}
	}
}

func TestHostFollowTitleBaseIsLaunchDirectory(t *testing.T) {
	followSeams(t)
	t.Chdir(t.TempDir())
	base, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	followDefault = func(context.Context, string) (string, error) { t.Fatal("host title contacted VM"); return "", nil }
	var selected followview.Options
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	if err := executeFollow(t, "--host", "docs/notes.md"); err != nil {
		t.Fatal(err)
	}
	if selected.Workdir != base || selected.Path != filepath.Join(base, "docs/notes.md") {
		t.Fatalf("host title base or path differs: %+v", selected)
	}
}

func TestFollowAbsoluteAndHomeTargetsSurviveUnavailableTitleBase(t *testing.T) {
	followSeams(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	unavailable := errors.New("settings unavailable")
	followDefault = func(context.Context, string) (string, error) { return "", unavailable }
	var selected followview.Options
	called := false
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; called = true; return nil }
	for _, tc := range []struct{ arg, path string }{
		{filepath.Join(home, "absolute.md"), filepath.Join(home, "absolute.md")},
		{"~/home.md", filepath.Join(home, "home.md")},
	} {
		called = false
		if err := executeFollow(t, "--space", "fixed", tc.arg); err != nil {
			t.Fatal(err)
		}
		if !called || selected.Path != tc.path || selected.Workdir != "" {
			t.Fatalf("absolute title fallback differs: %+v", selected)
		}
	}
	called = false
	if err := executeFollow(t, "--space", "fixed", "relative.md"); !errors.Is(err, unavailable) || called {
		t.Fatalf("relative target lost required base: %v, launched %v", err, called)
	}
}

func TestHostAbsoluteFollowSurvivesUnavailableLaunchDirectory(t *testing.T) {
	followSeams(t)
	// Unlinking cwd does not portably make Getwd fail (Darwin can still
	// resolve it). Inject the unavailable lookup, not filesystem semantics.
	lookups := 0
	followGetwd = func() (string, error) {
		lookups++
		return "", &os.PathError{Op: "getwd", Path: ".", Err: os.ErrNotExist}
	}
	var selected followview.Options
	followRun = func(_ context.Context, opts followview.Options) error { selected = opts; return nil }
	path := filepath.Join(t.TempDir(), "absolute.md")
	if err := executeFollow(t, "--host", path); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 || selected.Path != path || selected.Workdir != "" {
		t.Fatalf("host absolute fallback differs: %+v, lookups %d", selected, lookups)
	}
}

// A follow pane opens its file links through the tab channel the CLI claimed
// at start: its registration and the typed request reach that channel. Without
// the channel the link is refused, as outside the TUI.
func TestFollowOpensFileLinksThroughTheClaimedChannel(t *testing.T) {
	followSeams(t)
	dir := t.TempDir()
	// The launch workdir is the navigation root when it contains the file;
	// pin it so the expected root does not depend on where go test runs.
	followGetwd = func() (string, error) { return dir, nil }
	source, target := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	for _, path := range []string{source, target} {
		if err := os.WriteFile(path, []byte("[open b-doc](b.md)\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var result followview.ActionResultMsg
	followRun = func(_ context.Context, opts followview.Options) error {
		link, err := opts.Navigation.Context.Resolve("b.md")
		if err != nil {
			return err
		}
		result = opts.OpenFile(link)().(followview.ActionResultMsg)
		return nil
	}
	run := func(ctx context.Context) error {
		cmd := newFollowCmd()
		cmd.SetArgs([]string{"--host", source})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.ExecuteContext(ctx)
	}

	parent, child, err := termio.TabRequestPair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close(); _ = child.Close() }()
	if err := run(context.WithValue(t.Context(), tabRequestsKey{}, child)); err != nil || result.Err != nil {
		t.Fatalf("follow with the claimed channel: %v, open: %v", err, result.Err)
	}
	lines := bufio.NewReader(parent)
	var got []termio.TabRequest
	for range 2 {
		line, err := lines.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var r termio.TabRequest
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if got[0].Op != "follow-context" || got[0].FollowMode != "host" || got[0].FollowPath != source {
		t.Fatalf("registration: %+v", got[0])
	}
	if got[1] != termio.FollowRequest(filelink.Target{Mode: "host", Path: target, Root: dir}) {
		t.Fatalf("request: %+v", got[1])
	}

	result = followview.ActionResultMsg{}
	if err := run(t.Context()); err != nil || result.Err == nil || result.Err.Error() != "open local files from the yoloctl TUI" {
		t.Fatalf("follow outside the TUI: %v, open: %v", err, result.Err)
	}
}
