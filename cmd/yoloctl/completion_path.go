package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Host-path completion engine. Shell directives (ShellCompDirectiveFilterDirs
// or default file completion) can't express what the path completers need: a
// candidate set confined to a space's mount grants spans multiple roots
// (FilterDirs supports exactly one), and the TUI palette consumes candidate
// lines only — a directive with no candidates completes nothing there. So
// candidates are generated here, with one os.ReadDir per keystroke, and the
// shell and the palette see the same list.
//
// Two rules are load-bearing:
//
//   - A candidate must extend the typed token byte-for-byte. bash filters
//     replies with `compgen -W`, zsh/fish and the palette prefix-match too,
//     so a cleaned or ~-expanded candidate is silently dropped. `~` and
//     relative prefixes are expanded for the directory read only; emitted
//     values keep the user's literal prefix.
//   - Replies must carry ShellCompDirectiveNoFileComp even when empty: the
//     generated bash script registers `complete -o default`, so a bare empty
//     reply falls back to the shell's own unrestricted file completion —
//     resurrecting exactly the ungranted paths the constrained completers
//     exist to suppress.
//
// Everything is advisory and best-effort (unreadable directories yield an
// empty list, symlinks are not resolved): real path validation stays with
// create/reconcile.

// rootCandidate is one directory a constrained completion is allowed under —
// a space mount grant or the space's default workdir. desc annotates the
// seeded root in shells with description support and in the palette.
type rootCandidate struct {
	path string // absolute, cleaned host path
	desc string
}

// listPathCandidates completes toComplete against the host filesystem.
// dirsOnly keeps only directories (workdir completion); otherwise files are
// offered too. A non-empty roots confines results to
// paths under the given roots — or on the ancestor path toward one, so the
// user can type from / down to a deep grant — and seeds the roots themselves
// for an empty token, since every valid value lies under one of them.
func listPathCandidates(toComplete string, dirsOnly bool, roots []rootCandidate) ([]cobra.Completion, cobra.ShellCompDirective) {
	return listPathCandidatesMatching(toComplete, dirsOnly, roots, nil)
}

// listPathCandidatesMatching is listPathCandidates with an optional relation
// predicate for constraints richer than a union of roots. In particular,
// workdir completion uses it to honor the effective (longest-match) grant's
// writability while still allowing directory steps toward a deeper writable
// grant. A non-nil relates marks the result as constrained even when it has no
// eligible roots, so an all-read-only space never degrades to unconstrained
// host completion.
func listPathCandidatesMatching(
	toComplete string,
	dirsOnly bool,
	roots []rootCandidate,
	relates func(path string, isDir bool) bool,
) ([]cobra.Completion, cobra.ShellCompDirective) {
	constrained := len(roots) > 0 || relates != nil
	if constrained && toComplete == "" {
		seeded := seedRootCandidates(roots)
		directive := cobra.ShellCompDirectiveNoFileComp
		if len(seeded) > 0 {
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		return seeded, directive
	}
	if toComplete == "~" { // bare "~": continue into the home dir on the next keystroke
		if constrained {
			// Constrained mode: "~/" is only a useful step when the home dir
			// relates to a grant root like any other directory candidate.
			home, err := os.UserHomeDir()
			if err != nil || !pathCandidateRelates(filepath.Clean(home), true, roots, relates) {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
		return []cobra.Completion{"~/"}, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
	}

	literalParent, base := splitCompletionToken(toComplete)
	lookupDir := literalParent
	if lookupDir == "" {
		lookupDir = "."
	}
	lookupDir, err := expandUserPath(lookupDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	entries, err := os.ReadDir(lookupDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	descByPath := make(map[string]string, len(roots))
	for _, r := range roots {
		if _, ok := descByPath[r.path]; !ok {
			descByPath[r.path] = r.desc
		}
	}

	var out []cobra.Completion
	sawDir := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue // hidden entries only when explicitly asked for
		}
		full := filepath.Join(lookupDir, name)
		isDir := entry.IsDir()
		if !isDir && entry.Type()&os.ModeSymlink != 0 {
			fi, err := os.Stat(full)
			if err != nil {
				continue // dangling symlink: nothing completable behind it
			}
			isDir = fi.IsDir()
		}
		if dirsOnly && !isDir {
			continue
		}
		// Only directories can be a step on the way TOWARD a root.
		if constrained && !pathCandidateRelates(full, isDir, roots, relates) {
			continue
		}
		cand := literalParent + name
		if isDir {
			cand += "/"
		}
		// Cobra's line protocol reserves TAB for descriptions and LF/CR for
		// record framing. Such host filenames remain usable when typed manually,
		// but cannot be represented safely as completion candidates.
		if strings.ContainsAny(cand, "\t\r\n") {
			continue
		}
		if isDir {
			sawDir = true
		}
		if desc, ok := descByPath[full]; ok {
			cand = cobra.CompletionWithDesc(cand, desc)
		}
		out = append(out, cand)
	}
	sort.Strings(out)

	// NoSpace whenever a directory prefix is on offer, so completion can
	// continue inside it. The directive is response-wide — cobra can't
	// terminate files and continue dirs within one reply — so a MIXED list
	// necessarily shortchanges one side; directories win because traversal
	// is iterative while accepting a file is terminal (one manual space).
	// In practice shells insert a full candidate only once prefix filtering
	// (already applied above) narrows the reply, and a narrowed all-file
	// reply drops NoSpace here — only menu-style selection across a still-
	// mixed list pays the manual space. The palette is per-candidate via
	// the trailing "/" (completionTerminator) and is unaffected.
	directive := cobra.ShellCompDirectiveNoFileComp
	if sawDir {
		directive |= cobra.ShellCompDirectiveNoSpace
	}
	return out, directive
}

// seedRootCandidates offers the grant roots themselves for an empty token,
// each with a trailing "/" so accepting one continues completion inside it.
func seedRootCandidates(roots []rootCandidate) []cobra.Completion {
	seen := make(map[string]bool, len(roots))
	out := make([]cobra.Completion, 0, len(roots))
	for _, r := range roots {
		value := pathWithTrailingSeparator(r.path)
		if seen[r.path] || strings.ContainsAny(value, "\t\r\n") {
			continue
		}
		seen[r.path] = true
		out = append(out, cobra.CompletionWithDesc(value, r.desc))
	}
	sort.Strings(out)
	return out
}

// pathWithTrailingSeparator marks a directory candidate without turning the
// filesystem root into "//".
func pathWithTrailingSeparator(path string) string {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}
	return path + string(os.PathSeparator)
}

// splitCompletionToken splits the typed token at its last "/": the literal
// parent (kept verbatim, trailing slash included, as the emitted-candidate
// prefix) and the basename being completed.
func splitCompletionToken(s string) (parent, base string) {
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return "", s
	}
	return s[:i+1], s[i+1:]
}

// pathRelatesToRoots reports whether p (absolute, cleaned) is under one of
// the roots, or — for directories — an ancestor of one (a step on the typed
// path toward a grant).
func pathRelatesToRoots(p string, isDir bool, roots []rootCandidate) bool {
	for _, r := range roots {
		if pathWithinRoot(p, r.path) {
			return true
		}
		if isDir && pathWithinRoot(r.path, p) {
			return true
		}
	}
	return false
}

func pathCandidateRelates(
	p string,
	isDir bool,
	roots []rootCandidate,
	relates func(path string, isDir bool) bool,
) bool {
	if relates != nil {
		return relates(p, isDir)
	}
	return pathRelatesToRoots(p, isDir, roots)
}

// pathWithinRoot reports whether p equals root or lies beneath it. Both are
// absolute and cleaned (expandUserPath output / loadSpaceGrantRoots).
func pathWithinRoot(p, root string) bool {
	if root == string(os.PathSeparator) || p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// projectLiteralPaths maps canonical absolute paths back into the spelling of
// the token being completed. Shells and the palette discard candidates that
// do not extend that token byte-for-byte, so a declared /home/me/src value
// must be returned as ~/src when the user started with "~/", and similarly
// for ./, ../, and doubled separators. Empty input intentionally keeps the
// canonical absolute values as the useful top-level listing.
func projectLiteralPaths(paths []string, toComplete string) []cobra.Completion {
	if toComplete == "" {
		seen := make(map[string]bool, len(paths))
		out := make([]cobra.Completion, 0, len(paths))
		for _, path := range paths {
			if path == "" || strings.ContainsAny(path, "\t\r\n") || seen[path] {
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
		sort.Strings(out)
		return out
	}

	literalParent, _ := splitCompletionToken(toComplete)
	if toComplete == "~" {
		literalParent = "~/"
	}
	lookupParent := literalParent
	if lookupParent == "" {
		lookupParent = "."
	}
	lookupParent, err := expandUserPath(lookupParent)
	if err != nil {
		return nil
	}

	seen := make(map[string]bool, len(paths))
	out := make([]cobra.Completion, 0, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(lookupParent, path)
		if err != nil || rel == "." {
			continue
		}
		cand := literalParent + rel
		if !strings.HasPrefix(cand, toComplete) || strings.ContainsAny(cand, "\t\r\n") || seen[cand] {
			continue
		}
		seen[cand] = true
		out = append(out, cand)
	}
	sort.Strings(out)
	return out
}
