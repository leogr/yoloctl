#!/usr/bin/env bash
# VM-required end-to-end smoke for account-secret delivery:
#
#   1. `account auth import NAME claude` extracts ~/.claude.json `primaryApiKey`
#      into the account's ANTHROPIC_API_KEY secret.
#   2. A claude box selecting the account receives the key in its ENVIRONMENT
#      (the OPENSHELL_USER_ENVIRONMENT injection the supervisor performs into
#      SSH child processes — verified in v0.0.72 ssh.rs): no file, no key on
#      the command line.
#   3. RECREATE pickup (env injection is container-create-time): replacing the
#      secret changes nothing on `box restart`; `box recreate` delivers the
#      new value.
#   4. ONE-TIME native import: Codex auth.json is copied into the account leaf,
#      then an agent-written replacement survives box restart unchanged.
#
# Requires:
#   - a built ./bin/yoloctl (the `smoke-account-auth` Makefile target depends on `build`)
#   - SMOKE_ANTHROPIC_API_KEY set to a (test) Anthropic API-key VALUE — the
#     smoke only asserts delivery, never calls the API, so any value works
#   - a working selected VM backend + network egress (image builds pull packages)
#
# Isolated + self-cleaning: uses a dedicated space/account/boxes and tears them
# down on exit (success OR failure). Never touches your default space or real
# accounts. It does NOT read or modify your real ~/.claude.json (it points HOME
# at a throwaway dir holding only the test key for the import step).
#
# Note: provisioning a VM + building the claude AND codex images takes minutes,
# and the builds depend on upstream package mirrors — a transient network
# hiccup can fail `box create`; just re-run.

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
SPACE="$SMOKE_SPACE"
ACCOUNT="smoke"
BOX="smoke-claude"
CODEX_BOX="smoke-codex"
WORKDIR="$SMOKE_WORKDIR"
FAKE_HOME="${SMOKE_ROOT}/fake-home"
mkdir -p "$FAKE_HOME"
REAL_XDG="$XDG_CONFIG_HOME" # retain isolated configuration during HOME override

info() { printf '>>> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

if [ -z "${SMOKE_ANTHROPIC_API_KEY:-}" ]; then
	echo "SKIP: set SMOKE_ANTHROPIC_API_KEY=<test key> to run this smoke (it provisions a VM + builds images)"
	exit 0
fi

box_env_has() { # BOX VALUE-REGEX — presence check inside the box, value never printed
	"$YOLOCTL" box exec --space "$SPACE" "$1" -- env | grep -q "$2"
}

cleanup() {
	set +e
	"$YOLOCTL" box destroy "$BOX" --space "$SPACE" --force >/dev/null 2>&1
	"$YOLOCTL" box destroy "$CODEX_BOX" --space "$SPACE" --force >/dev/null 2>&1
	"$YOLOCTL" space destroy "$SPACE" --force >/dev/null 2>&1
	smoke_remove_owned_root
}
trap cleanup EXIT

info "1/6 space create with required initial account"
smoke_create_space "$SPACE" --workdir "$WORKDIR" --account "$ACCOUNT" || fail "space create"

info "2/6 account auth import $ACCOUNT claude (extract primaryApiKey into the secret)"
printf '{"primaryApiKey":"%s","hasCompletedOnboarding":true}\n' "$SMOKE_ANTHROPIC_API_KEY" >"$FAKE_HOME/.claude.json"
# HOME points at the throwaway dir so import reads the test ~/.claude.json;
# XDG_CONFIG_HOME stays on the real config so the account just created is found.
HOME="$FAKE_HOME" XDG_CONFIG_HOME="$REAL_XDG" "$YOLOCTL" account auth import "$ACCOUNT" --space "$SPACE" claude \
	|| fail "account auth import (primaryApiKey extraction)"
"$YOLOCTL" account show "$ACCOUNT" --space "$SPACE" | grep -q 'ANTHROPIC_API_KEY' \
	|| fail "ANTHROPIC_API_KEY secret missing after import"

info "4/6 box create $BOX (builds the claude image, runs it) + assert env injection"
"$YOLOCTL" box create --no-attach --space "$SPACE" --account "$ACCOUNT" --agent claude "$BOX" \
	|| fail "box create"
# box exec is an SSH child; v0.0.72's supervisor injects OPENSHELL_USER_ENVIRONMENT
# into SSH children, so the key must appear here. -q: presence only.
box_env_has "$BOX" "^ANTHROPIC_API_KEY=${SMOKE_ANTHROPIC_API_KEY}\$" \
	|| fail "ANTHROPIC_API_KEY not in the agent environment — injection did not reach the agent"
info "    ANTHROPIC_API_KEY delivered via env injection (no file, no command line)"

info "5/6 explicit replacement: restart keeps the old env, recreate delivers the new"
REPLACEMENT="${SMOKE_ANTHROPIC_API_KEY}-replacement"
printf '%s' "$REPLACEMENT" | "$YOLOCTL" account secret set "$ACCOUNT" --space "$SPACE" ANTHROPIC_API_KEY \
	|| fail "account secret set (replacement)"
"$YOLOCTL" box restart --no-attach "$BOX" --space "$SPACE" --no-autoupdate || fail "box restart"
box_env_has "$BOX" "^ANTHROPIC_API_KEY=${SMOKE_ANTHROPIC_API_KEY}\$" \
	|| fail "env injection changed on a plain restart (it is container-create-time)"
"$YOLOCTL" box recreate --no-attach "$BOX" --space "$SPACE" --force --no-autoupdate || fail "box recreate"
box_env_has "$BOX" "^ANTHROPIC_API_KEY=${REPLACEMENT}\$" \
	|| fail "box recreate did not deliver the replacement env-injected value"

info "6/6 one-time codex import: native state remains agent-owned across lifecycle"
"$YOLOCTL" box stop "$BOX" --space "$SPACE"
CODEX_LEAF="/home/sandbox/.codex"
CODEX_IMPORT="${REAL_XDG}/codex-smoke-auth.json"
printf '%s\n' '{"smoke":"imported-once"}' >"$CODEX_IMPORT"
"$YOLOCTL" account auth import "$ACCOUNT" --space "$SPACE" codex --file "$CODEX_IMPORT" \
	|| fail "account auth import codex"
"$YOLOCTL" account auth status "$ACCOUNT" --space "$SPACE" codex \
	|| fail "account auth status codex"
"$YOLOCTL" box create --no-attach --space "$SPACE" --account "$ACCOUNT" --agent codex "$CODEX_BOX" \
	|| fail "codex box create"
"$YOLOCTL" box exec "$CODEX_BOX" --space "$SPACE" -- grep -q '"smoke":"imported-once"' "${CODEX_LEAF}/auth.json" \
	|| fail "codex auth.json was not copied into native state"
"$YOLOCTL" box exec "$CODEX_BOX" --space "$SPACE" -- sh -c 'printf "%s\n" "{\"smoke\":\"agent-owned\"}" > /home/sandbox/.codex/auth.json'
"$YOLOCTL" box restart --no-attach "$CODEX_BOX" --space "$SPACE" --no-autoupdate || fail "codex box restart"
"$YOLOCTL" box exec "$CODEX_BOX" --space "$SPACE" -- grep -q '"smoke":"agent-owned"' "${CODEX_LEAF}/auth.json" \
	|| fail "box restart rewrote agent-owned native state"

echo "OK: account auth e2e passed (create-time env injection + one-time native import)"
