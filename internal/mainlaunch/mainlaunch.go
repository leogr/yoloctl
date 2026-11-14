// Package mainlaunch holds the fixed texts and the record of the M-b launch
// hand-over (stop-box contract 3, sections 2 and 3): the box's main process
// is a fixed launcher that waits on a FIFO in the box user's home; one SSH
// exec of the fixed hand-over command writes the session's launch into it;
// the launcher then becomes the session's command. Nothing here carries a
// variable's value: the record holds TERM, a per-launch token, the command
// and the names of the hand-over exec's environment.
package mainlaunch

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// Hand-over exit statuses. Any other answer (another status, a failed
// channel, a connection that ended first) means "not known".
const (
	// HandoverDelivered: the record was written into the launcher's FIFO.
	HandoverDelivered = 0
	// HandoverNoMkfifo: the image has no mkfifo; nothing was written.
	HandoverNoMkfifo = 81
	// HandoverNoFIFO: no launcher is waiting (the launch was already taken,
	// or the main process is not the launcher); nothing was written.
	HandoverNoFIFO = 82
	// HandoverNameHasToken: an environment name contains the token; nothing
	// was written (never resent).
	HandoverNameHasToken = 83
)

// Launcher exit statuses: each is a main-process exit, so the box stops.
const (
	LauncherMkdir       = 90
	LauncherMkfifo      = 91
	LauncherRead        = 92
	LauncherBadRecord   = 93
	LauncherMissingName = 94
)

// NoLoginShellEnv is the upstream SSH env request that makes the server run
// the hand-over command with a non-login shell (pinned OpenShell ac7f615c,
// openshell-supervisor-process ssh.rs NO_LOGIN_SHELL_ENV).
const NoLoginShellEnv = "OPENSHELL_NO_LOGIN_SHELL"

// LauncherDir is the directory, in the box user's home, of the FIFO.
const LauncherDir = ".yoloctl-launch"

// NewToken returns a random per-launch token that occurs nowhere in
// command; it picks again until it does not. The command is never changed.
func NewToken(command []byte) (string, error) {
	for range 64 {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		token := hex.EncodeToString(raw)
		if !bytes.Contains(command, []byte(token)) {
			return token, nil
		}
	}
	return "", errors.New("no launch token absent from the command")
}

// Record is the holder's part of the record (parts 1 to 4): the TERM line,
// the token line, the command byte for byte (embedded and trailing newlines
// kept), a newline and the token on a line of its own. The hand-over
// command appends the name list and the token as the last line.
func Record(term, token string, command []byte) ([]byte, error) {
	if term == "" || bytes.ContainsAny([]byte(term), "\n\x00") {
		return nil, errors.New("invalid TERM for the launch record")
	}
	if len(token) != 32 || bytes.Contains(command, []byte(token)) {
		return nil, errors.New("invalid launch token")
	}
	var b bytes.Buffer
	b.WriteString("TERM=" + term + "\n" + token + "\n")
	b.Write(command)
	b.WriteString("\n" + token + "\n")
	return b.Bytes(), nil
}

// names reads one process's initial environment names inside the box, one
// per line: each NUL-separated entry becomes one line (a byte 0x0a in an
// entry becomes 0x01 first, so a value cannot fake an entry), the name is the
// text before the first "=". A name containing a control character (below
// 0x20, or 0x7f; a line break is one) is set aside: it is on neither side,
// not listed, not removed, not reported missing (the documented limit: this
// line form cannot tell a newline name from a 0x01 name). Exact for every
// other name. Values only pass through tr and sed and are never kept.
const namesFn = `__yoloctl_mb_names() {
	tr '\n\000' '\001\n' < "/proc/$1/environ" | sed 's/=.*//' | while IFS= read -r __yoloctl_mb_n; do
		case $__yoloctl_mb_n in ''|*[[:cntrl:]]*) ;; *) printf '%s\n' "$__yoloctl_mb_n" ;; esac
	done
}
`

// HandoverScript is the fixed hand-over command, run by one SSH exec with
// NoLoginShellEnv set. Its standard input is Record's bytes. It answers a
// fixed status, or writes the record, its own environment's names and the
// token into the FIFO and answers 0. It prints nothing.
const HandoverScript = `__yoloctl_mb_nl='
'
` + namesFn + `__yoloctl_mb_f="$HOME/` + LauncherDir + `/fifo"
command -v mkfifo >/dev/null 2>&1 || exit 81
[ -p "$__yoloctl_mb_f" ] || exit 82
__yoloctl_mb_r=$(cat && printf .) || exit 84
__yoloctl_mb_r=${__yoloctl_mb_r%.}
__yoloctl_mb_tok=${__yoloctl_mb_r#*"$__yoloctl_mb_nl"}
__yoloctl_mb_tok=${__yoloctl_mb_tok%%"$__yoloctl_mb_nl"*}
__yoloctl_mb_names=$(__yoloctl_mb_names $$) || exit 84
case $__yoloctl_mb_names in *"$__yoloctl_mb_tok"*) exit 83 ;; esac
{
	printf '%s' "$__yoloctl_mb_r"
	[ -z "$__yoloctl_mb_names" ] || printf '%s\n' "$__yoloctl_mb_names"
	printf '%s\n' "$__yoloctl_mb_tok"
} > "$__yoloctl_mb_f" || exit 84
exit 0
`

// LauncherScript is the fixed main process of a confined box created by this
// version, run by the supervisor as "<shell> [--posix] -c LauncherScript
// <shell path>": $0 is the image shell selected in upstream's order, which the
// launch runs as the login shell (a declared SHELL stays a value).
//
// The launcher's own shell assigns no variable and changes no state: all its
// work (the FIFO with umask 077, the record, the name sets) runs in a
// subshell ($$ there is still this process's PID), which answers either
// "exit N" (a refused record or a missing name: this process exits N, nothing
// run) or "run" and a fully single-quoted script that this process evaluates:
// unset the extra identifier names, set TERM, exec the login shell, through
// "env -u NAME" for the other extra names. So every inherited variable,
// whatever its name, reaches the session byte for byte unless removed, and the
// file-creation mask is the one this process started with. It keeps the
// environment names the hand-over exec had (names only, never values; names
// with a control character are on neither side) and the command becomes the
// main process.
const LauncherScript = `command -v mkfifo >/dev/null 2>&1 || exec sleep infinity
set -- "$( (
__yoloctl_mb_nl='
'
` + namesFn + `__yoloctl_mb_q() {
	__yoloctl_mb_s=$1
	printf "'"
	while :; do
		case $__yoloctl_mb_s in
		*"'"*)
			printf '%s' "${__yoloctl_mb_s%%"'"*}'\\''"
			__yoloctl_mb_s=${__yoloctl_mb_s#*"'"} ;;
		*)
			printf '%s' "$__yoloctl_mb_s"
			break ;;
		esac
	done
	printf "' "
}
__yoloctl_mb_d="$HOME/` + LauncherDir + `"
__yoloctl_mb_f="$__yoloctl_mb_d/fifo"
umask 077
rm -f "$__yoloctl_mb_f" 2>/dev/null
rmdir "$__yoloctl_mb_d" 2>/dev/null
mkdir -m 700 "$__yoloctl_mb_d" || { echo 'exit 90'; exit; }
mkfifo -m 600 "$__yoloctl_mb_f" || { echo 'exit 91'; exit; }
__yoloctl_mb_r=$(cat "$__yoloctl_mb_f" && printf .) || { echo 'exit 92'; exit; }
__yoloctl_mb_r=${__yoloctl_mb_r%.}
rm -f "$__yoloctl_mb_f"
rmdir "$__yoloctl_mb_d"
case $__yoloctl_mb_r in TERM=*"$__yoloctl_mb_nl"*"$__yoloctl_mb_nl"*) ;; *) echo 'exit 93'; exit ;; esac
__yoloctl_mb_term=${__yoloctl_mb_r%%"$__yoloctl_mb_nl"*}
__yoloctl_mb_term=${__yoloctl_mb_term#TERM=}
__yoloctl_mb_r=${__yoloctl_mb_r#*"$__yoloctl_mb_nl"}
__yoloctl_mb_tok=${__yoloctl_mb_r%%"$__yoloctl_mb_nl"*}
__yoloctl_mb_r=${__yoloctl_mb_r#*"$__yoloctl_mb_nl"}
case $__yoloctl_mb_tok in ????????????????????????????????) ;; *) echo 'exit 93'; exit ;; esac
case $__yoloctl_mb_tok in *[!0-9a-f]*) echo 'exit 93'; exit ;; esac
case $__yoloctl_mb_r in *"$__yoloctl_mb_nl$__yoloctl_mb_tok$__yoloctl_mb_nl"*) ;; *) echo 'exit 93'; exit ;; esac
__yoloctl_mb_cmd=${__yoloctl_mb_r%%"$__yoloctl_mb_nl$__yoloctl_mb_tok$__yoloctl_mb_nl"*}
__yoloctl_mb_rest=${__yoloctl_mb_r#*"$__yoloctl_mb_nl$__yoloctl_mb_tok$__yoloctl_mb_nl"}
case $__yoloctl_mb_rest in "$__yoloctl_mb_tok$__yoloctl_mb_nl"|*"$__yoloctl_mb_nl$__yoloctl_mb_tok$__yoloctl_mb_nl") ;; *) echo 'exit 93'; exit ;; esac
__yoloctl_mb_list=${__yoloctl_mb_rest%"$__yoloctl_mb_tok$__yoloctl_mb_nl"}
case $__yoloctl_mb_cmd$__yoloctl_mb_list in *"$__yoloctl_mb_tok"*) echo 'exit 93'; exit ;; esac
__yoloctl_mb_have=$(__yoloctl_mb_names $$) || { echo 'exit 93'; exit; }
set -f
IFS=$__yoloctl_mb_nl
for __yoloctl_mb_n in $__yoloctl_mb_list; do
	case "$__yoloctl_mb_nl$__yoloctl_mb_have$__yoloctl_mb_nl" in *"$__yoloctl_mb_nl$__yoloctl_mb_n$__yoloctl_mb_nl"*) ;; *)
		printf 'yoloctl: this box lacks the session variable %s\n' "$__yoloctl_mb_n" >&2
		echo 'exit 94'
		exit ;;
	esac
done
__yoloctl_mb_unset=
set --
for __yoloctl_mb_n in $__yoloctl_mb_have; do
	case "$__yoloctl_mb_nl$__yoloctl_mb_list" in *"$__yoloctl_mb_nl$__yoloctl_mb_n$__yoloctl_mb_nl"*) continue ;; esac
	[ "$__yoloctl_mb_n" != TERM ] || continue
	case $__yoloctl_mb_n in
	[!A-Za-z_]*|*[!A-Za-z0-9_]*) set -- "$@" -u "$__yoloctl_mb_n" ;;
	*) __yoloctl_mb_unset="$__yoloctl_mb_unset $__yoloctl_mb_n" ;;
	esac
done
# An image whose env cannot remove by name keeps those variables (the
# documented limit); the session still starts.
if [ "$#" -gt 0 ] && ! env -u __yoloctl_mb_probe true >/dev/null 2>&1; then
	set --
fi
echo run
[ -z "$__yoloctl_mb_unset" ] || printf 'unset%s\n' "$__yoloctl_mb_unset"
printf 'TERM='; __yoloctl_mb_q "$__yoloctl_mb_term"; printf '\nexport TERM\nexec '
[ "$#" -eq 0 ] || __yoloctl_mb_q env
for __yoloctl_mb_a in "$@" "$0" -lc "$__yoloctl_mb_cmd"; do __yoloctl_mb_q "$__yoloctl_mb_a"; done
printf '\n'
) )"
case $1 in
'run
'*) eval "${1#run?}" ;;
'exit '9[0-4]) exit "${1#exit }" ;;
esac
exit 93
`
