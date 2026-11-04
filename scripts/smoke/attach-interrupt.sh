#!/usr/bin/env bash
# Live-VM gate: an interrupted attach client never pauses the space.
#
# In an isolated space, attachments that start a session are interrupted by
# the two incident triggers: the client is terminated (SIGTERM, SIGKILL) and
# its output consumer exits (the client dies of SIGPIPE). The receipt trace from
# `space operation` proves where each interruption landed: a kill is sent only
# once the attach's pending receipt shows authorized setup (`effect_phase:
# started`, setup not complete); a closed pipe breaks on the session-exit
# notice that setup prints after authorization, or on the lock-wait notice
# before it, while a declined `box recreate` prompt fed from a FIFO holds the
# space lock for exactly as long as the driver decides. One SIGTERM there must
# end the real CLI client with the detached status (75); every client wait is
# bounded. After each case another box
# mutation must succeed without any acknowledgment, no receipt may be
# uncertain, at most one session may have started, and a finite reattach must
# join that session without starting one. The summary and each case's
# finalized receipts are kept outside the fixture root, in ${SMOKE_ROOT}.attach-interrupt/.
#
# ATTACH_INTERRUPT_EXPECT=fence runs the same cases against a baseline binary:
# each owned-setup interruption must then fence; the fixture acknowledges it
# (inside the isolated space only) and records that separately.
#
# ATTACH_INTERRUPT_CASES selects fence (the default, the cases above),
# after-handoff or all. The after-handoff cases send one SIGTERM, SIGHUP or
# SIGINT to a `box attach` that already joined a running session, without a
# TTY and under a PTY: the client must exit within 5 s, the session must keep
# running and no receipt may stay pending or uncertain. They are recorded in
# after-handoff.tsv beside the summary.

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
SMOKE_SPACE="${SMOKE_SPACE:-smoke-attach-int}"
smoke_init

EXPECT="${ATTACH_INTERRUPT_EXPECT:-no-fence}"
[[ "$EXPECT" == no-fence || "$EXPECT" == fence ]] || smoke_fail "ATTACH_INTERRUPT_EXPECT must be no-fence or fence"
CASES="${ATTACH_INTERRUPT_CASES:-fence}"
[[ "$CASES" == fence || "$CASES" == after-handoff || "$CASES" == all ]] ||
	smoke_fail "ATTACH_INTERRUPT_CASES must be fence, after-handoff or all"
[[ "$EXPECT" == no-fence || "$CASES" == fence ]] || smoke_fail "ATTACH_INTERRUPT_EXPECT=fence runs only the fence cases"
TARGET=smoke-int-target
OTHER=smoke-int-other
OUT="${SMOKE_ROOT}/attach-interrupt"
mkdir -p "$OUT"
# Evidence survives the fixture's removal; never mix it with an older run.
REPORT="${SMOKE_ROOT}.attach-interrupt"
mkdir "$REPORT" 2>/dev/null || smoke_fail "report directory ${REPORT} exists; move it aside first"
SUMMARY="${REPORT}/summary.tsv"
CLIENT_WAIT=60 # seconds a signalled client may take to exit
printf 'case\ttrigger\tclient_exit\tphase_at_interrupt\treceipt_state\tclient_detached\tsessions_started\tmutation\treattach\n' >"$SUMMARY"
CLIENT_PID=""
PTY_HELPER_PID=""
HOLDER_PID=""
HANDOFF_SUMMARY="${REPORT}/after-handoff.tsv"
PROBE=0

cleanup() {
	set +e
	if [[ -n "$HOLDER_PID" ]]; then
		# EOF declines the holder's prompt; never kill it (recreate is not
		# effect-gated) and never leave it holding the space lock.
		exec 9>&-
		for _ in {1..150}; do
			kill -0 "$HOLDER_PID" 2>/dev/null || break
			sleep 0.2
		done
		if kill -0 "$HOLDER_PID" 2>/dev/null; then
			# Destroying behind a held space lock would block; keep everything.
			printf 'Lock holder %s still runs; retaining the fixture at %s\n' "$HOLDER_PID" "$SMOKE_ROOT" >&2
			find "$OUT" -maxdepth 1 -type f -exec cp -- {} "$REPORT"/ \; 2>/dev/null
			return
		fi
		wait "$HOLDER_PID" 2>/dev/null
	fi
	if [[ -n "$CLIENT_PID" ]] && kill -0 "$CLIENT_PID" 2>/dev/null; then
		kill -KILL "$CLIENT_PID" 2>/dev/null
		wait "$CLIENT_PID" 2>/dev/null
	fi
	# The PTY harness (not a yoloctl client) reaps its client once it is gone.
	if [[ -n "$PTY_HELPER_PID" ]]; then
		for _ in {1..50}; do
			kill -0 "$PTY_HELPER_PID" 2>/dev/null || break
			sleep 0.2
		done
		kill "$PTY_HELPER_PID" 2>/dev/null
		wait "$PTY_HELPER_PID" 2>/dev/null
	fi
	# Regular files only: opening the holder's FIFO would block forever.
	find "$OUT" -maxdepth 1 -type f -exec cp -- {} "$REPORT"/ \; 2>/dev/null
	printf 'Attach-interrupt report: %s\n' "$REPORT" >&2
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		if [[ "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 && "${SMOKE_PASSED:-0}" != 1 ]]; then
			printf 'Retaining failed fixture at %s\n' "$SMOKE_ROOT" >&2
			return
		fi
		smoke_destroy_box "$TARGET"
		smoke_destroy_box "$OTHER"
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_ensure_space

# The box sees the canonical host workdir mirrored under /workdir.
HOST_WORKDIR="$(cd "$SMOKE_WORKDIR" && pwd -P)"
GUEST_WORKDIR="/workdir${HOST_WORKDIR}"
STARTS="${HOST_WORKDIR}/starts"
STOP="${HOST_WORKDIR}/stop"
rm -f "$STARTS" "$STOP"

smoke_info "countable agentless session workload; a second box for mutations"
# Each session start appends one line; touching $STOP ends the session so
# the holder records a tombstone that the next attach reports during setup.
"$YOLOCTL" box create "$TARGET" --space "$SMOKE_SPACE" --agent none --workdir "$HOST_WORKDIR" \
	--no-attach --no-autoupdate \
	--session-command sh --session-command -c \
	--session-command 'echo started >>"$0/starts"; echo session-ready; while [ ! -e "$0/stop" ]; do sleep 0.2; done; rm -f "$0/stop"' \
	--session-command "$GUEST_WORKDIR"
"$YOLOCTL" box create "$OTHER" --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral --no-attach --no-autoupdate

receipts() {
	"$YOLOCTL" space operation "$SMOKE_SPACE"
}

starts() {
	if [[ -f "$STARTS" ]]; then wc -l <"$STARTS" | tr -d ' '; else echo 0; fi
}

session_state() {
	"$YOLOCTL" box session "$TARGET" --space "$SMOKE_SPACE" --json |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["session"]["state"])'
}

# receipt_field IDS_FILE FIELD... prints fields of the single new box.attach
# receipt not listed in IDS_FILE, or nothing while it does not exist yet.
new_attach_receipt() {
	receipts | python3 -c '
import json, sys
known = set(open(sys.argv[1]).read().split())
new = [r for r in json.load(sys.stdin) or [] if r["method"] == "box.attach" and r["id"] not in known]
if len(new) > 1:
    sys.exit("more than one new attach receipt")
if new:
    r = new[0]
    print(r["id"], r["state"], r.get("effect_phase", "-"), str(r.get("setup_complete", False)).lower(),
          str(r.get("client_detached", False)).lower())
' "$1"
}

# other_container prints OTHER's container ID from the space storage view.
other_container() {
	"$YOLOCTL" space storage "$SMOKE_SPACE" --json | python3 -c '
import json, sys
ids = [b["container"] for b in json.load(sys.stdin).get("boxes") or [] if b["name"] == sys.argv[1]]
print(ids[0] if len(ids) == 1 else "")
' "$OTHER"
}

uncertain_count() {
	receipts | python3 -c 'import json,sys; print(sum(r["state"] == "uncertain" for r in json.load(sys.stdin) or []))'
}

# Declaration mutations of another box: refused while the space is fenced.
mutate_other() {
	PROBE=$((PROBE + 1))
	local probe="smoke-int-probe-${PROBE}"
	"$YOLOCTL" box create "$probe" --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral \
		--manifest-only --no-attach >"${OUT}/mutation-${PROBE}.log" 2>&1 &&
		"$YOLOCTL" box destroy "$probe" --space "$SMOKE_SPACE" --force >>"${OUT}/mutation-${PROBE}.log" 2>&1
}

# wait_client PID: a client that outlives CLIENT_WAIT fails the gate (the EXIT
# trap then ends it); it never hangs the driver.
wait_client() {
	local pid="$1" _
	for _ in $(seq $((CLIENT_WAIT * 5))); do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.2
	done
	kill -0 "$pid" 2>/dev/null && smoke_fail "client ${pid} still runs ${CLIENT_WAIT}s after its signal"
	wait "$pid"
}

prepare_running_without_session() {
	if [[ "$(session_state)" == running ]]; then
		touch "$STOP"
		for _ in {1..150}; do
			[[ "$(session_state)" == running ]] || break
			sleep 0.2
		done
	fi
	[[ "$(session_state)" == not-running ]] || smoke_fail "the target session did not end"
	"$YOLOCTL" box start "$TARGET" --space "$SMOKE_SPACE" --no-attach --no-autoupdate >/dev/null
	[[ "$(session_state)" == not-running ]] || smoke_fail "box start --no-attach started a session"
}

prepare_stopped() {
	"$YOLOCTL" box stop "$TARGET" --space "$SMOKE_SPACE" >/dev/null
	rm -f "$STOP"
}

# run_case NAME TRIGGER PREPARE
run_case() {
	local name="$1" trigger="$2" prepare="$3"
	local ids="${OUT}/${name}.ids" before after status=0 phase=- line id state effect setup detached
	smoke_info "case ${name}: ${trigger} during an attach that starts a session (${prepare})"
	"$prepare"
	receipts | python3 -c 'import json,sys; print("\n".join(r["id"] for r in json.load(sys.stdin) or []))' >"$ids"
	before="$(starts)"
	case "$trigger" in
	SIGTERM | SIGKILL)
		"$YOLOCTL" box attach "$TARGET" --space "$SMOKE_SPACE" --no-autoupdate \
			</dev/null >"${OUT}/${name}.out" 2>"${OUT}/${name}.err" &
		CLIENT_PID=$!
		# Interrupt only once the receipt proves authorized, unfinished setup.
		for _ in {1..600}; do
			line="$(new_attach_receipt "$ids")"
			read -r id state effect setup detached <<<"${line:-- - - - -}"
			# A baseline attach has no effect phase; pending setup is its window.
			if [[ "$state" == pending && "$setup" == false && ("$effect" == started || "$EXPECT" == fence && "$effect" == -) ]]; then
				phase=owned-setup
				break
			fi
			if [[ "$setup" == true || "$state" != pending && "$state" != - ]]; then
				phase=after-handoff
				break
			fi
			kill -0 "$CLIENT_PID" 2>/dev/null || break
		done
		kill "-${trigger#SIG}" "$CLIENT_PID" 2>/dev/null || true
		wait_client "$CLIENT_PID" || status=$?
		CLIENT_PID=""
		;;
	closed-pipe | closed-pipe-waiting)
		local holder="" fifo="${OUT}/${name}.holder.in" other_id=""
		if [[ "$trigger" == closed-pipe-waiting ]]; then
			# Another operation holds the space lock for as long as the driver
			# decides: `box recreate` without --force asks for confirmation
			# inside its exclusive space lock and reads the answer from this
			# FIFO. The attach then waits on the lock, prints its lock-wait
			# notice before authorization after 1 s, and its pipe breaks there.
			# The answer is "n", so the holder changes nothing. The holder is
			# driver-controlled; there is no timing assumption.
			other_id="$(other_container)"
			[[ -n "$other_id" ]] || smoke_fail "${name}: no container identity for ${OTHER}"
			mkfifo "$fifo"
			exec 9<>"$fifo"
			"$YOLOCTL" box recreate "$OTHER" --space "$SMOKE_SPACE" --no-attach --no-autoupdate \
				<"$fifo" 9>&- >"${OUT}/${name}.holder.log" 2>&1 &
			holder=$!
			HOLDER_PID=$holder
			for _ in {1..300}; do
				grep -q 'continue? \[y/N\]' "${OUT}/${name}.holder.log" && break
				kill -0 "$holder" 2>/dev/null || smoke_fail "${name}: the lock holder ended before its prompt (see ${OUT}/${name}.holder.log)"
				sleep 0.1
			done
			grep -q 'continue? \[y/N\]' "${OUT}/${name}.holder.log" ||
				smoke_fail "${name}: the lock holder never reached its prompt (see ${OUT}/${name}.holder.log)"
		fi
		# A real consumer that exits at once; the client's first write fails.
		# The client's nonzero status must not trip errexit before it is kept.
		set +o pipefail
		{
			rc=0
			timeout --signal=KILL 180 "$YOLOCTL" box attach "$TARGET" --space "$SMOKE_SPACE" --no-autoupdate \
				</dev/null 9>&- 2>&1 || rc=$?
			echo "$rc" >"${OUT}/${name}.status"
		} | true
		set -o pipefail
		status="$(cat "${OUT}/${name}.status")"
		[[ "$status" != 137 ]] || smoke_fail "${name}: the closed-pipe client never wrote and hit the guard timeout"
		if [[ -n "$holder" ]]; then
			# The holder must have outlived the client, or the case proves nothing.
			kill -0 "$holder" 2>/dev/null || smoke_fail "${name}: the lock holder released early"
			receipts | python3 -c 'import json,sys; sys.exit(not any(r["method"] == "box.recreate" and r["state"] == "pending" for r in json.load(sys.stdin) or []))' ||
				smoke_fail "${name}: the lock holder released early (no pending box.recreate receipt)"
			echo n >&9
			exec 9>&-
			wait_client "$holder" || smoke_fail "${name}: the lock-holding recreate failed (see ${OUT}/${name}.holder.log)"
			HOLDER_PID=""
			rm -f -- "$fifo"
			grep -q aborted "${OUT}/${name}.holder.log" || smoke_fail "${name}: the lock holder did not decline"
			[[ "$(other_container)" == "$other_id" ]] || smoke_fail "${name}: the declined recreate replaced ${OTHER}'s container"
		fi
		;;
	esac
	# The interrupted request settles on its own; nothing is retried.
	for _ in {1..600}; do
		line="$(new_attach_receipt "$ids")"
		read -r id state effect setup detached <<<"${line:-- - - - -}"
		[[ "$state" == - || "$state" == pending ]] || break
		sleep 0.2
	done
	[[ "$state" != - && "$state" != pending ]] || smoke_fail "${name}: the attach receipt never settled"
	if [[ "$trigger" == closed-pipe* && "$EXPECT" == no-fence ]]; then
		# Where the pipe broke follows from the settled receipt.
		if [[ "$effect" == waiting ]]; then
			phase=waiting
		elif [[ "$detached" == true ]]; then
			phase=owned-setup
		else
			phase=after-handoff
		fi
	fi
	after="$(starts)"
	local started=$((after - before)) mutation=ok reattach=skipped
	receipts >"${REPORT}/${name}.receipts.json"
	if [[ "$EXPECT" == fence ]]; then
		# Baseline: record the outcome; a fence is acknowledged inside this
		# isolated fixture only, so later cases can run.
		if [[ "$state" == uncertain ]]; then
			if mutate_other; then smoke_fail "${name}: a fenced baseline accepted a mutation"; fi
			mutation=refused-then-acknowledged-in-fixture
			"$YOLOCTL" space operation "$SMOKE_SPACE" --acknowledge-all >"${OUT}/${name}.acknowledge.log"
		else
			mutate_other || smoke_fail "${name}: baseline refused a mutation without an uncertain receipt"
		fi
	else
		[[ "$state" == complete || "$state" == setup-complete ]] || smoke_fail "${name}: receipt ${id} is ${state}"
		[[ "$(uncertain_count)" == 0 ]] || smoke_fail "${name}: an uncertain receipt fences the space"
		mutate_other || smoke_fail "${name}: another box mutation was refused without acknowledgment (see ${OUT}/mutation-${PROBE}.log)"
	fi
	[[ "$started" -le 1 ]] || smoke_fail "${name}: ${started} sessions started for one attach"
	if [[ "$phase" == owned-setup && "$EXPECT" == no-fence ]]; then
		[[ "$effect" == started && "$setup" == true && "$detached" == true && "$started" == 1 ]] ||
			smoke_fail "${name}: owned setup did not finish without its client (effect=${effect} setup=${setup} started=${started})"
		[[ "$(session_state)" == running ]] || smoke_fail "${name}: the started session is not running"
		if [[ "$trigger" == SIGTERM ]]; then
			# One signal ends the client explicitly, with how to continue.
			[[ "$status" == 75 ]] || smoke_fail "${name}: SIGTERM client exit ${status}, want the detached status 75"
			grep -q "keeps starting the session" "${OUT}/${name}.err" ||
				smoke_fail "${name}: the detached client did not explain itself (see ${OUT}/${name}.err)"
		fi
	fi
	if [[ "$phase" == waiting && "$EXPECT" == no-fence ]]; then
		[[ "$effect" == waiting && "$started" == 0 ]] ||
			smoke_fail "${name}: a pre-authorization interruption changed state (effect=${effect} started=${started})"
	fi
	# A finite reattach joins the session; output goes to a file, never a pipe.
	if [[ "$(session_state)" == running ]]; then
		before="$(starts)"
		"$YOLOCTL" box use "$TARGET" screen --space "$SMOKE_SPACE" --timeout 60s \
			>"${OUT}/${name}.reattach.json" 2>"${OUT}/${name}.reattach.err" ||
			smoke_fail "${name}: reattach failed (see ${OUT}/${name}.reattach.err)"
		[[ "$(starts)" == "$before" ]] || smoke_fail "${name}: reattach started another session"
		grep -q session-ready "${OUT}/${name}.reattach.json" ||
			smoke_fail "${name}: the reattached screen lacks the session's session-ready line"
		reattach=joined
	fi
	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$trigger" "$status" "$phase" "$state" "$detached" \
		"$started" "$mutation" "$reattach" >>"$SUMMARY"
	smoke_info "case ${name}: client exit ${status}, ${phase}, receipt ${state}, client_detached ${detached}, ${started} session(s), mutation ${mutation}, reattach ${reattach}"
}

# The shared PTY harness reaps its child even while a descendant holds the
# PTY open; its own timeout stays above every bound of this driver.
PTY_RUN="$(dirname "${BASH_SOURCE[0]}")/pty-run.py"

now_ms() {
	python3 -I -c 'import time; print(time.time_ns() // 1000000)'
}

# ensure_running_session joins the target's session, starting it if needed.
ensure_running_session() {
	if [[ "$(session_state)" != running ]]; then
		rm -f "$STOP"
		PROBE=$((PROBE + 1))
		"$YOLOCTL" box use "$TARGET" screen --space "$SMOKE_SPACE" --timeout 60s \
			>"${OUT}/ensure-${PROBE}.json" 2>&1 || smoke_fail "could not start the target session (see ${OUT}/ensure-${PROBE}.json)"
	fi
	[[ "$(session_state)" == running ]] || smoke_fail "the target session is not running"
}

# run_attached_case NAME SIGNAL MODE: one signal to an attach after handoff.
run_attached_case() {
	local name="$1" sig="$2" mode="$3"
	local ids="${OUT}/${name}.ids" out="${OUT}/${name}.out" pidfile="${OUT}/${name}.pid"
	local status=0 before line id state effect setup detached sent elapsed_ms pending _
	smoke_info "case ${name}: one ${sig} to an attach that joined a running session (${mode})"
	ensure_running_session
	receipts | python3 -c 'import json,sys; print("\n".join(r["id"] for r in json.load(sys.stdin) or []))' >"$ids"
	before="$(starts)"
	if [[ "$mode" == pty ]]; then
		python3 "$PTY_RUN" --output "$out" --pid-file "$pidfile" --timeout 600 -- \
			"$YOLOCTL" box attach "$TARGET" --space "$SMOKE_SPACE" --no-autoupdate \
			</dev/null 9>&- >"${OUT}/${name}.pty.log" 2>"${OUT}/${name}.err" &
		PTY_HELPER_PID=$!
		for _ in {1..100}; do
			[[ -s "$pidfile" ]] && break
			sleep 0.1
		done
		[[ -s "$pidfile" ]] || smoke_fail "${name}: the PTY helper never started its client"
		CLIENT_PID="$(cat "$pidfile")"
	else
		"$YOLOCTL" box attach "$TARGET" --space "$SMOKE_SPACE" --no-autoupdate \
			</dev/null 9>&- >"$out" 2>"${OUT}/${name}.err" &
		CLIENT_PID=$!
	fi
	# Signal only after handoff: setup is complete and the attachment printed.
	for _ in {1..300}; do
		line="$(new_attach_receipt "$ids")"
		read -r id state effect setup detached <<<"${line:-- - - - -}"
		[[ "$setup" == true && -s "$out" ]] && break
		kill -0 "$CLIENT_PID" 2>/dev/null || smoke_fail "${name}: the client ended before its attachment started (see ${OUT}/${name}.err)"
		sleep 0.2
	done
	[[ "$setup" == true && -s "$out" ]] || smoke_fail "${name}: the attachment never started (setup=${setup})"
	sent="$(now_ms)"
	kill "-${sig#SIG}" "$CLIENT_PID" || smoke_fail "${name}: the client ended before its ${sig}"
	for _ in {1..50}; do
		kill -0 "$CLIENT_PID" 2>/dev/null || break
		sleep 0.1
	done
	elapsed_ms=$(($(now_ms) - sent))
	kill -0 "$CLIENT_PID" 2>/dev/null && smoke_fail "${name}: the client still runs ${elapsed_ms} ms after one ${sig}"
	if [[ "$mode" == pty ]]; then
		wait_client "$PTY_HELPER_PID" || status=$?
		PTY_HELPER_PID=""
	else
		wait "$CLIENT_PID" || status=$?
	fi
	CLIENT_PID=""
	[[ "$status" != 75 ]] || smoke_fail "${name}: an attached client reported the before-handoff status 75"
	# The attachment's receipt settles on its own; nothing stays pending.
	for _ in {1..300}; do
		line="$(new_attach_receipt "$ids")"
		read -r id state effect setup detached <<<"${line:-- - - - -}"
		[[ "$state" == pending ]] || break
		sleep 0.2
	done
	[[ "$state" == complete || "$state" == setup-complete ]] || smoke_fail "${name}: receipt ${id} is ${state}"
	receipts >"${REPORT}/${name}.receipts.json"
	pending="$(receipts | python3 -c 'import json,sys; print(sum(r["state"] in ("pending", "uncertain", "command-uncertain") for r in json.load(sys.stdin) or []))')"
	[[ "$pending" == 0 ]] || smoke_fail "${name}: ${pending} receipt(s) pending or uncertain"
	[[ "$(session_state)" == running ]] || smoke_fail "${name}: the session ended with its attach client"
	[[ "$(starts)" == "$before" ]] || smoke_fail "${name}: the attach started another session"
	mutate_other || smoke_fail "${name}: another box mutation was refused (see ${OUT}/mutation-${PROBE}.log)"
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$sig" "$mode" "$status" "$elapsed_ms" "$state" >>"$HANDOFF_SUMMARY"
	smoke_info "case ${name}: client exit ${status} after ${elapsed_ms} ms, receipt ${state}, session running"
}

if [[ "$CASES" != after-handoff ]]; then
	run_case term-running SIGTERM prepare_running_without_session
	run_case kill-stopped SIGKILL prepare_stopped
	run_case term-stopped SIGTERM prepare_stopped
	run_case pipe-running closed-pipe prepare_running_without_session
	run_case pipe-waiting closed-pipe-waiting prepare_running_without_session
fi
if [[ "$CASES" != fence ]]; then
	printf 'case\tsignal\tmode\tclient_exit\texit_ms\treceipt_state\n' >"$HANDOFF_SUMMARY"
	for mode in no-tty pty; do
		for sig in SIGTERM SIGHUP SIGINT; do
			run_attached_case "attached-${sig#SIG}-${mode}" "$sig" "$mode"
		done
	done
fi

# Each interruption must have landed where its case claims.
python3 - "$SUMMARY" "$EXPECT" <<'PY'
import csv, sys
rows = list(csv.DictReader(open(sys.argv[1]), delimiter="\t"))
want = {"SIGTERM": "owned-setup", "SIGKILL": "owned-setup", "closed-pipe": "owned-setup", "closed-pipe-waiting": "waiting"}
if sys.argv[2] == "fence":
    if not any(row["receipt_state"] == "uncertain" for row in rows):
        sys.exit("baseline: no interrupted attach fenced the space")
    sys.exit(0)
for row in rows:
    if row["phase_at_interrupt"] != want[row["trigger"]]:
        sys.exit(f"{row['case']}: the interruption landed {row['phase_at_interrupt']}, not {want[row['trigger']]}; rerun the gate")
PY
[[ "$EXPECT" == fence || "$(uncertain_count)" == 0 ]] || smoke_fail "uncertain receipts remain"
SMOKE_PASSED=1
smoke_info "PASS: interrupted attach clients (${EXPECT}, ${CASES}); report ${REPORT}"
