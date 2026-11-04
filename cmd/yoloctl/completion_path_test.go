package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// pathTestTree builds a small fixture: <tmp>/{alpha/, beta/sub/, gamma.txt,
// .hidden/}. Returns the tmp root.
func pathTestTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"alpha", "beta/sub", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "gamma.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write gamma.txt: %v", err)
	}
	return dir
}

func TestListPathCandidatesDirs(t *testing.T) {
	dir := pathTestTree(t)

	got, directive := listPathCandidates(dir+"/", true, nil)
	want := []cobra.Completion{dir + "/alpha/", dir + "/beta/"}
	if !slices.Equal(got, want) {
		t.Errorf("dirs = %v, want %v", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoSpace|cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoSpace|NoFileComp", directive)
	}
}

func TestListPathCandidatesFilesModeIncludesFiles(t *testing.T) {
	dir := pathTestTree(t)

	got, directive := listPathCandidates(dir+"/", false, nil)
	want := []cobra.Completion{dir + "/alpha/", dir + "/beta/", dir + "/gamma.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	// The response-wide directive can't terminate files AND continue dirs in
	// one mixed reply; directory traversal wins (see listPathCandidates).
	// Prefix-narrowed all-file replies drop NoSpace (test below), and the
	// palette handles mixed lists per-candidate via the trailing "/".
	if directive != cobra.ShellCompDirectiveNoSpace|cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("mixed-list directive = %v, want NoSpace|NoFileComp", directive)
	}
}

// An all-file candidate list must NOT set NoSpace: the token is complete and
// the shell should terminate it as usual.
func TestListPathCandidatesFileOnlyListDropsNoSpace(t *testing.T) {
	dir := pathTestTree(t)

	got, directive := listPathCandidates(dir+"/gam", false, nil)
	if want := []cobra.Completion{dir + "/gamma.txt"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp only", directive)
	}
}

func TestListPathCandidatesPrefixFilter(t *testing.T) {
	dir := pathTestTree(t)

	got, _ := listPathCandidates(dir+"/al", true, nil)
	if want := []cobra.Completion{dir + "/alpha/"}; !slices.Equal(got, want) {
		t.Errorf("prefix filter = %v, want %v", got, want)
	}
}

func TestListPathCandidatesHiddenEntries(t *testing.T) {
	dir := pathTestTree(t)

	if got, _ := listPathCandidates(dir+"/", true, nil); slices.Contains(got, dir+"/.hidden/") {
		t.Errorf("unrequested hidden dir offered: %v", got)
	}
	got, _ := listPathCandidates(dir+"/.", true, nil)
	if want := []cobra.Completion{dir + "/.hidden/"}; !slices.Equal(got, want) {
		t.Errorf("dot prefix = %v, want %v", got, want)
	}
}

// Constrained mode: only paths under a root survive, plus directories on the
// ancestor path toward a root (so the user can type from / down to a deep
// grant). Roots equal to a candidate carry their description.
func TestListPathCandidatesConstrainedToRoots(t *testing.T) {
	dir := pathTestTree(t)
	roots := []rootCandidate{{path: filepath.Join(dir, "beta", "sub"), desc: "default workdir"}}

	// alpha/ is unrelated to the root; beta/ is an ancestor of it.
	got, _ := listPathCandidates(dir+"/", true, roots)
	if want := []cobra.Completion{dir + "/beta/"}; !slices.Equal(got, want) {
		t.Errorf("constrained = %v, want %v", got, want)
	}

	// Inside beta/, sub/ IS the root: kept, annotated.
	got, _ = listPathCandidates(dir+"/beta/", true, roots)
	if want := []cobra.Completion{dir + "/beta/sub/\tdefault workdir"}; !slices.Equal(got, want) {
		t.Errorf("at root = %v, want %v", got, want)
	}
}

func TestListPathCandidatesSeedsRootsOnEmptyInput(t *testing.T) {
	roots := []rootCandidate{
		{path: "/tmp/grant-b", desc: "mount grant (rw)"},
		{path: "/tmp/grant-a", desc: "default workdir"},
		{path: "/tmp/grant-a", desc: "mount grant (rw)"}, // duplicate path: first desc wins
	}

	got, directive := listPathCandidates("", true, roots)
	want := []cobra.Completion{
		"/tmp/grant-a/\tdefault workdir",
		"/tmp/grant-b/\tmount grant (rw)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("seeds = %v, want %v", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoSpace|cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoSpace|NoFileComp", directive)
	}
}

func TestListPathCandidatesSeedsFilesystemRootWithoutDoubleSlash(t *testing.T) {
	got, directive := listPathCandidates("", true, []rootCandidate{{path: "/", desc: "mount grant (rw)"}})
	if want := []cobra.Completion{"/\tmount grant (rw)"}; !slices.Equal(got, want) {
		t.Fatalf("root seed = %v, want %v", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoSpace|cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v, want NoSpace|NoFileComp", directive)
	}
}

// The emitted candidates must extend the typed token byte-for-byte: a `~`
// prefix is expanded to read the directory but preserved in the values, or
// bash/zsh/fish (and the palette) silently drop every candidate.
func TestListPathCandidatesPreservesTildePrefix(t *testing.T) {
	home := pathTestTree(t)
	t.Setenv("HOME", home)

	got, _ := listPathCandidates("~/al", true, nil)
	if want := []cobra.Completion{"~/alpha/"}; !slices.Equal(got, want) {
		t.Errorf("tilde prefix = %v, want %v", got, want)
	}

	got, directive := listPathCandidates("~", true, nil)
	if want := []cobra.Completion{"~/"}; !slices.Equal(got, want) {
		t.Errorf("bare tilde = %v, want %v", got, want)
	}
	if directive&cobra.ShellCompDirectiveNoSpace == 0 {
		t.Errorf("bare tilde directive %v misses NoSpace", directive)
	}
}

// In constrained mode a bare "~" is a directory step like any other: offered
// only when the home dir relates to a grant root, suppressed otherwise —
// it must not punch a hole in the grant filter.
func TestListPathCandidatesBareTildeConstrained(t *testing.T) {
	home := pathTestTree(t)
	t.Setenv("HOME", home)

	related := []rootCandidate{{path: filepath.Join(home, "alpha"), desc: "default workdir"}}
	if got, _ := listPathCandidates("~", true, related); !slices.Equal(got, []cobra.Completion{"~/"}) {
		t.Errorf("related home = %v, want [~/]", got)
	}

	unrelated := []rootCandidate{{path: filepath.Join(t.TempDir(), "elsewhere"), desc: "default workdir"}}
	got, directive := listPathCandidates("~", true, unrelated)
	if len(got) != 0 {
		t.Errorf("unrelated home = %v, want none", got)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("unrelated home directive = %v, want NoFileComp", directive)
	}
}

func TestListPathCandidatesPreservesRelativePrefix(t *testing.T) {
	dir := pathTestTree(t)
	t.Chdir(dir)

	got, _ := listPathCandidates("be", true, nil)
	if want := []cobra.Completion{"beta/"}; !slices.Equal(got, want) {
		t.Errorf("relative prefix = %v, want %v", got, want)
	}
}

// Symlinks complete by what they point at: a dir symlink is a directory
// candidate, a dangling one is dropped.
func TestListPathCandidatesSymlinks(t *testing.T) {
	dir := pathTestTree(t)
	if err := os.Symlink(filepath.Join(dir, "alpha"), filepath.Join(dir, "linked")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling")); err != nil {
		t.Skipf("symlink: %v", err)
	}

	got, _ := listPathCandidates(dir+"/", true, nil)
	if !slices.Contains(got, dir+"/linked/") {
		t.Errorf("dir symlink not offered as directory: %v", got)
	}
	if slices.ContainsFunc(got, func(c cobra.Completion) bool { return strings.Contains(c, "dangling") }) {
		t.Errorf("dangling symlink offered: %v", got)
	}
}

// Unreadable directories yield an empty list — never an error on a
// keystroke — and keep NoFileComp so bash's `-o default` can't fall back to
// unrestricted file completion.
func TestListPathCandidatesUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits don't bind root")
	}
	dir := pathTestTree(t)
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got, directive := listPathCandidates(locked+"/", true, nil)
	if len(got) != 0 {
		t.Errorf("candidates = %v, want empty", got)
	}
	if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Errorf("directive = %v, want NoFileComp set", directive)
	}
}

// TAB, LF, and CR are record delimiters in cobra's completion protocol. Host
// files containing them cannot be represented safely and are omitted; an
// ordinary colon-prefixed filename remains valid.
func TestListPathCandidatesSkipsProtocolDelimiterFilenames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{":auth.json", "tab\tname", "line\nname", "return\rname"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}

	got, directive := listPathCandidates(dir+"/", false, nil)
	if want := []cobra.Completion{dir + "/:auth.json"}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %q, want %q", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v, want NoFileComp", directive)
	}
}
