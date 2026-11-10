// Package yoloctlcli is the TUI's subprocess client for the yoloctl
// binary for palette completion. Everything goes through os.Executable();
// the TUI never calls command internals in-process, so the
// palette and a shell complete identically with zero drift.
package yoloctlcli

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Candidate is one completion result: the value to insert and cobra's
// optional one-line description. Cobra fills Desc for free from a
// subcommand's Short and a flag's usage string; dynamic value
// completions (spaces, agentboxes, …) carry an empty Desc.
type Candidate struct {
	Value string
	Desc  string
}

// Directive is the ShellCompDirective bitmask cobra closes every
// `__complete` reply with (the ":N" line). The palette consults one bit:
// NoSpace, which path completion sets so an accepted directory prefix keeps
// the token open for further completion instead of terminating it.
type Directive int

// NoSpace reports whether cobra asked for no terminator after an accepted
// candidate.
func (d Directive) NoSpace() bool {
	return d&Directive(cobra.ShellCompDirectiveNoSpace) != 0
}

// Complete asks `yoloctl __complete words... last` for candidates.
// words are the whitespace-split palette tokens before the one being
// completed; last is the (possibly empty) token under the cursor.
// env is the caller-captured child environment; nil inherits the process env.
// Returns each candidate's value, its (cobra-supplied) description, and
// the reply's closing directive. Best-effort: errors and the completion
// latency budget (shells re-run this on every TAB; 2s is generous) yield
// nil and a zero (default) directive.
func Complete(ctx context.Context, words []string, last string, env []string) ([]Candidate, Directive) {
	self, err := os.Executable()
	if err != nil {
		return nil, 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	args := append([]string{"__complete"}, words...)
	args = append(args, last)
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, 0
	}
	return parseCompletions(out)
}

// parseCompletions turns cobra's `__complete` stdout into candidates plus
// the trailing ":N" ShellCompDirective. Cobra emits flag/subcommand
// candidates for an empty token too — kept; the palette filters by prefix
// on its side.
func parseCompletions(out []byte) ([]Candidate, Directive) {
	lines := strings.Split(string(out), "\n")
	directiveLine := -1
	var directive Directive
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if line == "" {
			continue
		}
		// Cobra reserves only the final non-empty :N line for the response
		// directive. Earlier colon-prefixed lines are valid candidate values.
		if strings.HasPrefix(line, ":") {
			if n, err := strconv.Atoi(line[1:]); err == nil {
				directive = Directive(n)
				directiveLine = i
			}
		}
		break
	}

	var cands []Candidate
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" || i == directiveLine {
			continue
		}
		value, desc, _ := strings.Cut(line, "\t")
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cands = append(cands, Candidate{Value: value, Desc: strings.TrimSpace(desc)})
	}
	return cands, directive
}
