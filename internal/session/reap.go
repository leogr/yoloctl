package session

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"golang.org/x/crypto/ssh"
)

// The daemon reaper. Some agents start a detached background server that
// outlives the agent's exit and that the next launch would reuse, with the
// previous launch's environment and without the ancestry its egress is
// attributed to. A managed launch is the lifetime of the daemons its profile
// declares: the holder stops them before the agent starts, as a prefix of the
// agent's own session command (nothing is added to the agent's argv or
// environment), and again, best effort, after the agent exits.
//
// A process matches when it runs as the sandbox user and its argv starts with
// a declared prefix, the first word compared to the executable's base name.
// Other processes, a user's box exec shell among them, are never signaled.
// The environment of another exec's processes is unreadable across the
// sandbox's Landlock domains, so the match cannot tell launches apart: a
// declared daemon a user started from box exec is stopped too. Children the
// daemon started are matched only if they match a prefix themselves.

// Reaper polling, in tenths of a second: a first SIGTERM (a graceful drain),
// then a second one to survivors (Codex's forced shutdown, which may leave
// its socket behind, as SIGKILL does), then SIGKILL. A survivor is reported
// and the launch proceeds: an exception to the lifetime rule, not its
// enforcement.
var (
	reapTermWait  = 20
	reapForceWait = 10
	reapKillWait  = 10
)

// reapLaunchBudget bounds the reaper before the agent starts, its /proc scan
// included: at the budget coreutils timeout sends SIGKILL to the reaper's
// whole process group, so no child of it (a stalled tr, a sleep) outlives the
// bound, and the agent starts either way.
// reapExitBudget bounds the reaper's session after the agent exits, channel
// opening included.
var (
	reapLaunchBudget = 5 * time.Second
	reapExitBudget   = 10 * time.Second
)

// reapDelimiter ends the reaper's quoted here-document. No script line is
// ever equal to it: daemon words appear only inside the quoted case pattern
// of a longer line.
const reapDelimiter = "YR_EOF"

// reapScript stops the matching processes. Its only output is a line on fd 3
// naming the processes that survived SIGKILL; reaperCommand points fd 3 at
// the session's stderr and discards every shell's own messages.
const reapScript = `yr_nl='
'
yr_me=$(id -u)
yr_match() {
	yr_u=
	while read -r yr_k yr_v yr_x; do
		if [ "$yr_k" = Uid: ]; then yr_u=$yr_v; break; fi
	done 2>/dev/null <"/proc/$1/status"
	[ -n "$yr_u" ] && [ "$yr_u" = "$yr_me" ] || return 1
	yr_c=$(tr '\000' '\n' 2>/dev/null <"/proc/$1/cmdline") || return 1
	yr_a=${yr_c%%"$yr_nl"*}
	case $yr_c in *"$yr_nl"*) yr_r=${yr_c#*"$yr_nl"} ;; *) yr_r= ;; esac
	case "${yr_a##*/}$yr_nl$yr_r$yr_nl" in
	@PATTERNS@) return 0 ;;
	esac
	return 1
}
yr_live() {
	yr_s=
	for yr_p in $yr_l; do yr_match "$yr_p" && yr_s="$yr_s $yr_p"; done
	yr_l=$yr_s
	[ -n "$yr_l" ]
}
yr_wait() {
	yr_n=$1
	while yr_live; do
		[ "$yr_n" -gt 0 ] || return 1
		sleep 0.1
		yr_n=$((yr_n - 1))
	done
}
yr_l=
for yr_d in /proc/[0-9]*; do
	yr_p=${yr_d#/proc/}
	[ "$yr_p" = "$$" ] || ! yr_match "$yr_p" || yr_l="$yr_l $yr_p"
done
if yr_live; then
	kill -TERM $yr_l 2>/dev/null
	if ! yr_wait @TERM@; then
		kill -TERM $yr_l 2>/dev/null
		if ! yr_wait @FORCE@; then
			kill -KILL $yr_l 2>/dev/null
			yr_wait @KILL@ || echo "yoloctl: agent daemon processes$yr_l survived SIGKILL" >&3
		fi
	fi
fi
`

// ReapScript returns the POSIX sh program stopping the declared daemons, or
// "" when none are declared. Each entry is validated as a profile's
// session.daemons entry before its words are embedded.
func ReapScript(daemons []string) (string, error) {
	if len(daemons) == 0 {
		return "", nil
	}
	patterns := make([]string, 0, len(daemons))
	for _, d := range daemons {
		words, err := agentprofile.DaemonArgv(d)
		if err != nil {
			return "", fmt.Errorf("session: %w", err)
		}
		patterns = append(patterns, `"`+strings.Join(words, "${yr_nl}")+`${yr_nl}"*`)
	}
	return strings.NewReplacer(
		"@PATTERNS@", strings.Join(patterns, "|"),
		"@TERM@", fmt.Sprint(reapTermWait),
		"@FORCE@", fmt.Sprint(reapForceWait),
		"@KILL@", fmt.Sprint(reapKillWait),
	).Replace(reapScript), nil
}

// reaperCommand runs script with sh from a quoted here-document, so the
// shell's input is the script itself and nothing in it is expanded early. A
// group discards the diagnostics of both shells: the reaper's own (a child
// reported killed) and the session shell's, which bash prints with the whole
// command when timeout kills itself with the group. The reaper's report
// reaches stderr through fd 3. The group's redirections end with it, so the
// command after it (the agent) inherits neither fd 3 nor a changed stderr.
func reaperCommand(prefix, script string) string {
	return "{ " + prefix + "sh -s 2>/dev/null <<'" + reapDelimiter + "'\n" + script + reapDelimiter + "\n} 3>&2 2>/dev/null\n"
}

// reapBefore prefixes the agent's session command with the reaper under
// timeout. The reaper reads only its here-document, so typeahead stays queued
// for the agent. Without --foreground, timeout runs it in a process group of
// its own and kills the whole group at the budget; the reaper reads no
// terminal input. A timed-out reaper is reported on stderr and the agent
// starts.
func reapBefore(script, command string) string {
	if script == "" {
		return command
	}
	return reaperCommand("timeout -s KILL "+strconv.FormatFloat(reapLaunchBudget.Seconds(), 'f', -1, 64)+" ", script) +
		"case $? in 124|137) echo 'yoloctl: agent daemon reaper timed out' >&2 ;; esac\n" + command
}

// reapAfterExit runs the reaper in a session of its own once the agent has
// exited, best effort and within reapExitBudget. Failures are logged.
func reapAfterExit(ctx context.Context, client *ssh.Client, script string, logf func(string, ...any)) {
	if script == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, reapExitBudget)
	defer cancel()
	s, err := newSandboxSession(ctx, client, make(chan struct{}, 1))
	if err != nil {
		logf("stop agent daemons after exit: %v", err)
		return
	}
	defer func() { _ = s.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	var out bytes.Buffer
	s.Stdout, s.Stderr = &out, &out
	err = s.Run(reaperCommand("", script))
	if msg := strings.TrimSpace(out.String()); msg != "" {
		logf("%s", msg)
	}
	if err != nil {
		logf("stop agent daemons after exit: %v", err)
	}
}
