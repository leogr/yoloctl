#!/usr/bin/env bash
# Live-VM smoke for the lifecycle auto-attach tail:
# create/copy/start/restart/recreate attach to the box's session by default
# (generic-shell `bash -l` here), the session's exit status becomes the
# command's exit code, an externally terminated attach detaches (exit 0 +
# space-qualified hint) while the session survives, and --no-attach opts out.
# The same PTY harness then checks `box exec`'s PTY rule: a terminal on stdin
# and stdout gets a remote PTY, a redirected stdout does not.
# Finally attach-mixed.py drives a tab, managed attach and passthrough together
# with audited input/query replies, output load, resizes and an unread viewer.
#
# A Python PTY harness works on Linux and macOS, propagates the child's exit
# status, and records the exact yoloctl PID for the detach test.

# Emit the fixture's actual keystrokes for both the live smoke and its local
# regression. Importing this file defines these helpers without touching Lima.
smoke_replay_setup_input() {
	# A default title/colored prompt can exceed the entire 128-byte ring.
	# Put readiness in a short final prompt and construct the marker at runtime
	# so polling cannot mistake readline's command echo for completed setup.
	printf '%s\r' 'export YOLOCTL_SMOKE_SESSION=survived; PROMPT_COMMAND=; printf -v PS1 "REPLAY_%s> " MARKER; printf "\033[>3u\033[2J\033[HSTATE_HEADER\033[10;1H"; printf "%0512d\n" 0'
}

smoke_replay_resume_input() {
	printf '%s\r' 'test "$YOLOCTL_SMOKE_SESSION" = survived || exit 9; printf "\033[<uREATTACH_%s\n" OK; exit 0'
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then return 0; fi

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

PTY_RUN="$(dirname "${BASH_SOURCE[0]}")/pty-run.py"

BOX1="smoke-attach-1"
BOX2="smoke-attach-2"
BOX3="smoke-attach-3"
OUT="${SMOKE_ROOT}/attach-out"
mkdir -p "$OUT"

DETACH_HARNESS_PID=""
DETACH_YOLOCTL_PID=""

cleanup() {
	set +e
	# A failure mid-detach-phase must not leave the background PTY harness
	# (and its attached yoloctl) running.
	if [[ -n "$DETACH_YOLOCTL_PID" ]] && kill -0 "$DETACH_YOLOCTL_PID" 2>/dev/null; then
		kill "$DETACH_YOLOCTL_PID" 2>/dev/null
	fi
	if [[ -n "$DETACH_HARNESS_PID" ]] && kill -0 "$DETACH_HARNESS_PID" 2>/dev/null; then
		kill "$DETACH_HARNESS_PID" 2>/dev/null
		wait "$DETACH_HARNESS_PID" 2>/dev/null
	fi
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != "1" ]]; then
		smoke_destroy_box "$BOX1"
		smoke_destroy_box "$BOX2"
		smoke_destroy_box "$BOX3"
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_destroy_box "$BOX1"
smoke_destroy_box "$BOX2"
smoke_destroy_box "$BOX3"
smoke_ensure_space

# pty_run PHASE STDIN_LINE CMD... — run a yoloctl command under a real PTY,
# feeding STDIN_LINE to the attached session and capturing all output to
# $OUT/PHASE.txt. Propagates the yoloctl exit status and flushes output for
# polling during the detach phase. CR is Enter; LF is Ctrl+J and becomes
# CSI 106;5u after the fixture enables Kitty keyboard disambiguation.
pty_run() {
	local phase="$1" input="$2"
	shift 2
	printf '%s\r' "$input" | python3 "$PTY_RUN" --output "${OUT}/${phase}.txt" --ready-text "sandbox@" -- "$@"
}

expect_attached() {
	grep -q "sandbox@" "${OUT}/$1.txt" \
		|| smoke_fail "$1: expected the managed shell prompt"
}

smoke_info "1/10 box create auto-attaches into bash -l and exits cleanly"
pty_run create "exit 0" "$YOLOCTL" box create "$BOX1" --space "$SMOKE_SPACE" \
	--workdir-mode ephemeral --no-autoupdate || smoke_fail "create+attach exited $?"
expect_attached create

# The VM-side binary must run on the guest even when the host CLI is an
# Intel executable running under Rosetta. Verify the confined identity too.
identity=$("$YOLOCTL" box exec "$BOX1" --space "$SMOKE_SPACE" -- sh -c 'printf "%s:%s" "$(id -u)" "$(id -g)"')
[[ "$identity" == "$(id -u):$(id -g)" ]] || smoke_fail "sandbox identity $identity does not match host"

smoke_info "2/10 box copy auto-attaches"
pty_run copy "exit 0" "$YOLOCTL" box copy "$BOX1" "$BOX2" --space "$SMOKE_SPACE" \
	--no-autoupdate || smoke_fail "copy+attach exited $?"
expect_attached copy

smoke_info "3/10 box start auto-attaches after a stop"
"$YOLOCTL" box stop "$BOX2" --space "$SMOKE_SPACE"
pty_run start "exit 0" "$YOLOCTL" box start "$BOX2" --space "$SMOKE_SPACE" \
	--no-autoupdate || smoke_fail "start+attach exited $?"
expect_attached start

smoke_info "4/10 box restart auto-attaches"
pty_run restart "exit 0" "$YOLOCTL" box restart "$BOX2" --space "$SMOKE_SPACE" \
	--no-autoupdate || smoke_fail "restart+attach exited $?"
expect_attached restart

smoke_info "5/10 box recreate auto-attaches and propagates the session exit status"
rc=0
pty_run recreate "exit 7" "$YOLOCTL" box recreate "$BOX2" --space "$SMOKE_SPACE" \
	--force --no-autoupdate || rc=$?
[[ "$rc" == "7" ]] || smoke_fail "recreate+attach exit status = $rc, want the session's 7"
expect_attached recreate

for attach_mode in managed passthrough; do
attach_flags=()
if [[ "$attach_mode" == passthrough ]]; then attach_flags=(--passthrough); fi
smoke_info "6/10 ${attach_mode}: externally terminated attach detaches: exit 0, hint, session survives"
# Keep this fixture's ring small so keyboard negotiation is certainly evicted.
# A fresh holder starts below; tuning never changes an already living holder.
printf 'session:\n  ring_buffer_bytes: 128\n' >"${SMOKE_CONFIG_ROOT}/tui.yaml"
: >"${OUT}/detach.txt"
detach_pidfile="${OUT}/detach.pid"
: >"$detach_pidfile"
smoke_replay_setup_input | \
	python3 "$PTY_RUN" --output "${OUT}/detach.txt" --pid-file "$detach_pidfile" --ready-text "sandbox@" -- \
	"$YOLOCTL" box start "$BOX1" --space "$SMOKE_SPACE" --no-autoupdate "${attach_flags[@]}" &
harness_pid=$!
DETACH_HARNESS_PID="$harness_pid"
yoloctl_pid=""
for _ in {1..120}; do
	if grep -q "sandbox@" "${OUT}/detach.txt" 2>/dev/null \
		&& [[ -s "$detach_pidfile" ]]; then
		IFS= read -r yoloctl_pid <"$detach_pidfile" || true
		if [[ "$yoloctl_pid" =~ ^[0-9]+$ ]] && kill -0 "$yoloctl_pid" 2>/dev/null; then
			DETACH_YOLOCTL_PID="$yoloctl_pid"
			break
		fi
		yoloctl_pid=""
	fi
	sleep 2
done
[[ -n "$yoloctl_pid" ]] || smoke_fail "detach: never saw the attach begin"
for _ in {1..30}; do
	grep -q 'REPLAY_MARKER' "${OUT}/detach.txt" && break
	sleep 1
done
grep -q 'REPLAY_MARKER' "${OUT}/detach.txt" || smoke_fail "detach: shell did not produce the replay marker"
kill -TERM "$yoloctl_pid"
rc=0
wait "$harness_pid" || rc=$?
DETACH_HARNESS_PID=""
DETACH_YOLOCTL_PID=""
[[ "$rc" == "0" ]] || smoke_fail "detach: expected exit 0, got $rc"
grep -q "reattach: yoloctl box attach ${BOX1} --space ${SMOKE_SPACE}" "${OUT}/detach.txt" \
	|| smoke_fail "detach: missing the space-qualified reattach hint"
# Capture first: grep -q on a pipe can make the CLI exit with SIGPIPE after
# matching the session line, which pipefail would misreport as session loss.
"$YOLOCTL" box show "$BOX1" --space "$SMOKE_SPACE" >"${OUT}/detached-state.txt"
grep -q "running (join it" "${OUT}/detached-state.txt" \
	|| smoke_fail "detach: session did not survive the detach"

smoke_info "7/10 ${attach_mode}: reattach replays output and joins the same shell"
smoke_replay_resume_input | \
	python3 "$PTY_RUN" --output "${OUT}/reattach.txt" --timeout 60 --ready-text REPLAY_MARKER -- \
	"$YOLOCTL" box attach "$BOX1" --space "$SMOKE_SPACE" --no-autoupdate "${attach_flags[@]}" \
	|| smoke_fail "reattach did not resume the same shell"
grep -q 'REPLAY_MARKER' "${OUT}/reattach.txt" || smoke_fail "reattach: missing replay"
grep -q 'REATTACH_OK' "${OUT}/reattach.txt" || smoke_fail "reattach: lost shell state"
python3 - "${OUT}/reattach.txt" "$attach_mode" <<'PY'
import pathlib, sys
output = pathlib.Path(sys.argv[1]).read_bytes()
if sys.argv[2] == "passthrough":
    marker = output.index(b"REPLAY_MARKER")
    prefix = output[:marker]
    assert b"\x1b[>3u" not in prefix, "startup keyboard control was not evicted"
    assert b"\x1b[=3u" in prefix, "reattach lost the evicted keyboard negotiation"
    assert b"\x1b[<u" in output[marker:], "agent did not unwind its restored keyboard state"
else:
    assert b"STATE_HEADER" in output, "native snapshot lost the screen header outside the byte ring"

PY
done
# Later phases use the default ring budget again.
rm "${SMOKE_CONFIG_ROOT}/tui.yaml"

smoke_info "8/10 --no-attach skips the attach even on a PTY"
rc=0
pty_run noattach "" "$YOLOCTL" box create "$BOX3" --space "$SMOKE_SPACE" \
	--workdir-mode ephemeral --no-autoupdate --no-attach || rc=$?
[[ "$rc" == "0" ]] || smoke_fail "--no-attach create exited $rc"
if grep -q "notice: attaching to box" "${OUT}/noattach.txt"; then
	smoke_fail "--no-attach still attached"
fi
"$YOLOCTL" box show "$BOX3" --space "$SMOKE_SPACE" >"${OUT}/noattach-state.txt"
grep -q "none (start one" "${OUT}/noattach-state.txt" \
	|| smoke_fail "--no-attach: unexpected session state"

smoke_info "9/10 box exec asks for a PTY only with a terminal on stdin and stdout"
# Both runs have a terminal on stdin. The first redirects stdout the way
# `out=$(...)` does: no PTY, so the bytes are exact ("\n", never "\r\n").
exec_probe='test -t 1 && echo pty-yes || echo pty-no'
exec_capture="${OUT}/exec-capture.bin"
pty_run exec-capture "" bash -c '"$1" box exec "$2" --space "$3" -- sh -c "$4" >"$5"' _ \
	"$YOLOCTL" "$BOX3" "$SMOKE_SPACE" "$exec_probe" "$exec_capture" \
	|| smoke_fail "box exec with a redirected stdout exited $?"
cmp -s "$exec_capture" <(printf 'pty-no\n') || {
	od -c "$exec_capture" >&2
	smoke_fail "box exec output redirected from a terminal is not byte-exact"
}
pty_run exec-terminal "" "$YOLOCTL" box exec "$BOX3" --space "$SMOKE_SPACE" -- \
	sh -c "$exec_probe" || smoke_fail "box exec on a terminal exited $?"
grep -q "pty-yes" "${OUT}/exec-terminal.txt" \
	|| smoke_fail "box exec on a terminal did not get a PTY"
for phase in exec-capture exec-terminal; do
	if grep -q '^Error:' "${OUT}/${phase}.txt"; then
		smoke_fail "$phase: successful exec printed an internal transport error"
	fi
done
# Exact stderr matters as much as stdout. SSH proxy cleanup must not append
# cancellation errors to an otherwise successful nonterminal command.
"$YOLOCTL" box exec "$BOX3" --space "$SMOKE_SPACE" -- \
	sh -c 'printf stdout-tail; printf stderr-tail >&2' \
	>"${OUT}/exec-stdout.bin" 2>"${OUT}/exec-stderr.bin"
cmp -s "${OUT}/exec-stdout.bin" <(printf stdout-tail) || smoke_fail "exec stdout changed"
cmp -s "${OUT}/exec-stderr.bin" <(printf stderr-tail) || {
	cat "${OUT}/exec-stderr.bin" >&2
	smoke_fail "exec stderr changed"
}

smoke_info "10/10 simultaneous TUI, managed and passthrough viewers under load"
python3 "$(dirname "${BASH_SOURCE[0]}")/attach-mixed.py" \
	--yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --box "$BOX3" --output "$OUT/mixed"

smoke_info "collaborative box-use with a passthrough viewer and two agent frontends"
python3 "$(dirname "${BASH_SOURCE[0]}")/box-use.py" \
	--yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --box "$BOX3" --output "$OUT/box-use"

smoke_info "PASS: lifecycle auto-attach, box exec PTY rule, mixed viewers and box-use"
