#!/usr/bin/env bash
# Live-VM smoke for feature -> capability -> computed-policy enforcement.

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

BOX="smoke-features-policy"
ACCOUNT="smoke-policy"

cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != "1" ]]; then
		smoke_destroy_box "$BOX"
		smoke_destroy_account "$ACCOUNT"
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_destroy_box "$BOX"
smoke_destroy_account "$ACCOUNT"
smoke_ensure_space

smoke_info "creating a Claude box with its latest feature-composed CLI"
"$YOLOCTL" account create "$ACCOUNT" --space "$SMOKE_SPACE"
"$YOLOCTL" box create --no-attach "$BOX" \
	--space "$SMOKE_SPACE" \
	--agent claude \
	--account "$ACCOUNT" \
	--workdir-mode ephemeral \
	--no-autoupdate

smoke_info "asserting the desired and live runtime state are identical"
diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE" 2>&1)"
[[ "$diff_output" == "PLAN: no changes" ]] || {
	printf '%s\n' "$diff_output" >&2
	smoke_fail "expected box diff to report no changes"
}

smoke_info "asserting curl cannot borrow Claude's Anthropic egress grant"
if "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- \
	curl -sS --max-time 5 -o /dev/null https://api.anthropic.com/ 2>/dev/null; then
	smoke_fail "curl reached api.anthropic.com through Claude's per-binary grant"
fi

log_output=""
for _attempt in {1..10}; do
	log_output="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1 || true)"
	if grep -q "DENIED /usr/bin/curl" <<<"$log_output"; then
		break
	fi
	sleep 1
done
if ! grep -q "DENIED /usr/bin/curl" <<<"$log_output"; then
	printf '%s\n' "$log_output" | tail -20 >&2
	smoke_fail "the sandbox log did not record the curl denial"
fi
if ! grep -q "binary '/usr/bin/curl' not allowed in policy 'anthropic'" <<<"$log_output"; then
	printf '%s\n' "$log_output" | tail -20 >&2
	smoke_fail "the denial log did not attribute curl to the Anthropic policy"
fi

smoke_info "asserting PTY allocation is denied without fs.pty.allocate"
# Match the errno text so an unrelated exec failure cannot pass for a denial.
pty_denied="$("$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- \
	python3 -c 'import os; os.openpty()' </dev/null 2>&1)" &&
	smoke_fail "a box without fs.pty.allocate allocated a pseudo-terminal"
grep -q "Permission denied" <<<"$pty_denied" || {
	printf '%s\n' "$pty_denied" >&2
	smoke_fail "openpty() failed without fs.pty.allocate, but not with Permission denied"
}

smoke_info "granting fs.pty.allocate (policy drift restarts the box)"
"$YOLOCTL" box set "$BOX" --space "$SMOKE_SPACE" --capability fs.pty.allocate
diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE" 2>&1)"
[[ "$diff_output" == "PLAN: no changes" ]] || {
	printf '%s\n' "$diff_output" >&2
	smoke_fail "expected no drift after granting fs.pty.allocate"
}

# The restart itself is half of the assertion: openshell-sandbox aborts startup
# when a read_write path is a symlink (/dev/ptmx is one), so a box that answers
# `box exec` again proves the composed grant avoids that trap.
smoke_info "asserting a sandboxed process can now allocate and use a PTY"
pty_name="$("$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- \
	python3 -c 'import os; m, s = os.openpty(); os.write(s, b"ok"); print(os.ttyname(s))' </dev/null)" ||
	smoke_fail "openpty() still fails with fs.pty.allocate granted"
[[ "$pty_name" == /dev/pts/* ]] || smoke_fail "unexpected PTY slave name: $pty_name"

smoke_info "asserting box set refuses a capability no catalog defines"
bogus_output="$("$YOLOCTL" box set "$BOX" --space "$SMOKE_SPACE" \
	--capability smoke.no-such-capability 2>&1)" &&
	smoke_fail "box set accepted a capability no catalog defines"
grep -q 'unknown capability "smoke.no-such-capability"' <<<"$bogus_output" || {
	printf '%s\n' "$bogus_output" >&2
	smoke_fail "box set failed on the unknown capability, but not with the unknown-capability error"
}

# A repeated grant rewrites nothing yet still reconciles. Reaching "already in
# sync" also proves the refused set above left the manifest alone: a saved
# unknown capability would fail this reconcile.
smoke_info "asserting a repeated grant still reconciles and finds the box in sync"
repeat_output="$("$YOLOCTL" box set "$BOX" --space "$SMOKE_SPACE" \
	--capability fs.pty.allocate 2>&1)" || {
	printf '%s\n' "$repeat_output" >&2
	smoke_fail "repeating an applied box set failed"
}
grep -q "manifest already holds the requested values" <<<"$repeat_output" &&
	grep -q "already in sync" <<<"$repeat_output" || {
	printf '%s\n' "$repeat_output" >&2
	smoke_fail "a repeated box set did not report an unchanged manifest and an in-sync box"
}

smoke_info "PASS: feature policy round-trip, per-binary attribution, the PTY grant, and box set convergence work"
