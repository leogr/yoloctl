#!/usr/bin/env bash
# Isolated native installer/policy/account smoke. Optional OAuth file enables
# real personal-account acceptance; credentials are never printed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
BOX=smoke-agy
ACCOUNT=smoke-agy
cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT
smoke_ensure_space
smoke_destroy_box "$BOX"
smoke_destroy_account "$ACCOUNT"
"$YOLOCTL" account create "$ACCOUNT" --space "$SMOKE_SPACE"
fixture="${SMOKE_ROOT}/agy-oauth-token"
printf '%s\n' '{"auth_method":"consumer","token":{"access_token":"synthetic-agy","token_type":"Bearer","refresh_token":"synthetic-agy","expiry":"2099-01-01T00:00:00Z"}}' >"$fixture"
chmod 0600 "$fixture"
"$YOLOCTL" account auth import "$ACCOUNT" agy --space "$SMOKE_SPACE" --file "$fixture"
printf '%s' 'synthetic-key' | "$YOLOCTL" account secret set "$ACCOUNT" GEMINI_API_KEY --space "$SMOKE_SPACE"

smoke_info "building latest native agy and inspecting managed version metadata"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent agy --account "$ACCOUNT" \
	--features yolo.space.operator --workdir-mode ephemeral --no-attach --no-autoupdate
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }
version="$(in_box agy --version)"
in_box python3 -c '
import json, os, pathlib, sys
p = pathlib.Path("/home/sandbox/.local/share/yoloctl/agy/agy.json")
assert json.loads(p.read_text())["version"] == sys.argv[1]
assert os.environ["GEMINI_API_KEY"] == "synthetic-key"
' "$version"
"$YOLOCTL" agent status --space "$SMOKE_SPACE" --no-check-latest --json >"${SMOKE_ROOT}/agy-status.json"
python3 - "$version" "${SMOKE_ROOT}/agy-status.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[2]))
assert any(r["agent"] == "agy" and r["installed_version"] == sys.argv[1] for r in rows)
PY

smoke_info "checking launcher defaults and read-only operator guidance before native startup"
# Observe the installed launcher's setup at its exec boundary. The native CLI
# owns settings.json afterward: 1.2.17 removes enableTelemetry and omits a false
# enableTerminalSandbox when saving. Retained JSON keys are not a runtime contract.
in_box python3 -I -c '
import json, os, pathlib, runpy, sys
from unittest.mock import patch
with patch("os.execv") as launch, patch.object(sys, "argv", ["agy", "--sandbox=false"]):
    runpy.run_path("/usr/local/lib/yoloctl/agy-launch.py")
launch.assert_called_once_with("/home/sandbox/.local/share/yoloctl/agy/agy", ["agy", "--sandbox=false"])
assert os.environ["AGY_CLI_DISABLE_AUTO_UPDATE"] == "true"
root = pathlib.Path.home() / ".gemini"
s = json.loads((root / "antigravity-cli/settings.json").read_text())
assert s["enableTelemetry"] is False and s["enableTerminalSandbox"] is False
# yolo.agent.observe-agy: a new account names the installed reporter.
reporter = "/usr/local/libexec/yoloctl/agent-statusline-agy"
assert os.access(reporter, os.X_OK)
assert s["statusLine"] == {"type": "command", "command": reporter, "stack_with_default": True}
skill = root / "antigravity-cli/skills/yolo-box-operator/SKILL.md"
assert skill.is_file()
try:
    skill.open("a").close()
except PermissionError:
    pass
else:
    raise AssertionError("operator skill is writable")
'

smoke_info "checking native OAuth traffic and denied curl attribution"
if in_box env AGY_CLI_DISABLE_AUTO_UPDATE=true timeout 30 agy --sandbox=false \
	--dangerously-skip-permissions --print 'Reply OK' --print-timeout=5s \
	>"${SMOKE_ROOT}/agy-synthetic.log" 2>&1; then
	smoke_fail "synthetic credentials unexpectedly authenticated"
fi
logs="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
grep -q 'ALLOWED.*/home/sandbox/.local/share/yoloctl/agy/agy.*daily-cloudcode-pa.googleapis.com' <<<"$logs" ||
	smoke_fail "native API egress was not allowed; inspect isolated box logs"
if in_box curl -sS --max-time 5 -o /dev/null https://daily-cloudcode-pa.googleapis.com/ 2>/dev/null; then
	smoke_fail "curl borrowed native agy egress"
fi
logs="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
grep -q "binary '/usr/bin/curl' not allowed in policy 'agy'" <<<"$logs" ||
	smoke_fail "missing attributed curl denial"
in_box python3 -c '
import pathlib
root = pathlib.Path.home() / ".gemini"
(root / "config/smoke-marker").write_text("persist")
'
[[ "$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE")" == "PLAN: no changes" ]] || smoke_fail "agy drift"
if "$YOLOCTL" account auth import "$ACCOUNT" agy --space "$SMOKE_SPACE" --file "$fixture" \
	>"${SMOKE_ROOT}/agy-import-denied.log" 2>&1; then
	smoke_fail "import overwrote an active account"
fi
grep -qi running "${SMOKE_ROOT}/agy-import-denied.log" || smoke_fail "unexpected import refusal"

smoke_info "checking native maintenance and persistent state after recreation"
"$YOLOCTL" agent update agy --agentbox "$BOX" --space "$SMOKE_SPACE" --force
in_box agy --version
"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
in_box grep -qx persist /home/sandbox/.gemini/config/smoke-marker
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
in_box grep -qx persist /home/sandbox/.gemini/config/smoke-marker

if [[ -n "${SMOKE_AGY_AUTH_FILE:-}" ]]; then
	smoke_info "checking opted-in personal OAuth and shell tools across two fresh processes"
	"$YOLOCTL" box stop "$BOX" --space "$SMOKE_SPACE"
	"$YOLOCTL" account auth import "$ACCOUNT" agy --space "$SMOKE_SPACE" --file "$SMOKE_AGY_AUTH_FILE"
	"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
	for attempt in 1 2; do
		in_box env AGY_CLI_DISABLE_AUTO_UPDATE=true timeout 180 agy --sandbox=false \
			--dangerously-skip-permissions --print 'Run a shell command that writes agy-shell-ok to /workdir/agy-shell-smoke, then stop.' \
			--print-timeout=120s --output-format=json >"${SMOKE_ROOT}/agy-response-${attempt}.json"
		in_box grep -qx agy-shell-ok /workdir/agy-shell-smoke
		in_box rm /workdir/agy-shell-smoke
	done
else
	smoke_info "SKIP: real personal OAuth and shell tools (set SMOKE_AGY_AUTH_FILE); interactive login/TUI need manual validation"
fi
smoke_info "PASS: agy installer, policy, operator skill and native-state checks"
