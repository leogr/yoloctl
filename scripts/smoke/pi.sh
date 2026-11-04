#!/usr/bin/env bash
# Isolated Pi image, policy and account smoke. No provider credentials are
# needed for the default path. SMOKE_PI_OPENAI_API_KEY opts into a real API/tool
# request; it is delivered as an account secret and never printed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
BOX=smoke-pi
ACCOUNT=smoke-pi

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

smoke_info "importing a synthetic Pi login from a disposable file"
fixture_home="${SMOKE_ROOT}/pi-host"
mkdir -p "${fixture_home}/.pi/agent"
printf '%s\n' '{"openai":{"type":"api_key","key":"synthetic-import"}}' >"${fixture_home}/.pi/agent/auth.json"
chmod 0600 "${fixture_home}/.pi/agent/auth.json"
"$YOLOCTL" account auth import "$ACCOUNT" pi --space "$SMOKE_SPACE" --file "${fixture_home}/.pi/agent/auth.json"
printf '%s' 'synthetic-first' | "$YOLOCTL" account secret set "$ACCOUNT" OPENAI_API_KEY --space "$SMOKE_SPACE"

smoke_info "building Pi latest and checking auth delivery and search tools"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent pi --account "$ACCOUNT" \
	--workdir-mode ephemeral --features yolo.space.operator --no-attach --no-autoupdate
smoke_check_image_identity "$BOX"
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }
in_pi() { in_box env PI_CODING_AGENT_DIR=/home/sandbox/.pi/agent PI_OFFLINE=1 PI_TELEMETRY=0 pi "$@"; }
in_pi --version
in_box rg --version
in_box fdfind --version
in_pi auth check --provider openai >"${SMOKE_ROOT}/pi-auth-check.txt"
in_box node -e '
const fs = require("node:fs");
const assert = require("node:assert/strict");
assert.equal(process.env.OPENAI_API_KEY, "synthetic-first");
assert.equal(JSON.parse(fs.readFileSync("/home/sandbox/.pi/agent/auth.json")).openai.key, "synthetic-import");
'
diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE")"
[[ "$diff_output" == "PLAN: no changes" ]] || smoke_fail "Pi box has drift: $diff_output"

smoke_info "checking real Pi shell execution, operator skill discovery and saved sessions against a local fixture"
in_box node --input-type=module <"$(dirname "${BASH_SOURCE[0]}")/pi-runtime.mjs"

smoke_info "checking allowed Node HTTPS and denied curl egress"
hosts=(api.anthropic.com platform.claude.com api.openai.com auth.openai.com chatgpt.com generativelanguage.googleapis.com openrouter.ai)
for host in "${hosts[@]}"; do
	smoke_info "probing HTTPS ${host}"
	status="$(in_box node -e '
const https = require("node:https");
const req = https.get("https://" + process.argv[1] + "/", res => {
  res.resume();
  console.log(res.statusCode);
});
req.setTimeout(15000, () => req.destroy(new Error("timeout")));
req.on("error", error => {
  console.error("HTTPS " + process.argv[1] + ": " + (error.code || "request failed") + ": " + error.message);
  process.exitCode = 1;
});
' "$host")"
	smoke_check_https_status "$host" "$status"
done
if in_box curl -sS --max-time 5 -o /dev/null https://generativelanguage.googleapis.com/ 2>/dev/null; then
	smoke_fail "curl borrowed Pi's Node egress grant"
fi
for _attempt in {1..10}; do
	log_output="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
	if grep -q "binary '/usr/bin/curl' not allowed in policy 'pi'" <<<"$log_output"; then
		break
	fi
	sleep 1
done
grep -q "binary '/usr/bin/curl' not allowed in policy 'pi'" <<<"$log_output" ||
	smoke_fail "missing attributed Pi egress denial"
smoke_require_allowed_egress "$log_output" pi /usr/bin/node "${hosts[@]}"

smoke_info "checking import refusal and create-time secret replacement"
if "$YOLOCTL" account auth import "$ACCOUNT" pi --space "$SMOKE_SPACE" --file "${fixture_home}/.pi/agent/auth.json" \
	>"${SMOKE_ROOT}/pi-import-denied.log" 2>&1; then
	smoke_fail "native import overwrote an active account"
fi
grep -qi 'running' "${SMOKE_ROOT}/pi-import-denied.log" ||
	smoke_fail "import failed for a reason other than an active consumer"
printf '%s' 'synthetic-replacement' | "$YOLOCTL" account secret set "$ACCOUNT" OPENAI_API_KEY --space "$SMOKE_SPACE"
in_box node -e '
const fs = require("node:fs");
fs.writeFileSync("/home/sandbox/.pi/agent/auth.json", JSON.stringify({openai:{type:"api_key",key:"synthetic-agent-owned"}}));
fs.mkdirSync("/home/sandbox/.pi/history", {recursive:true});
fs.writeFileSync("/home/sandbox/.pi/history/yoloctl-smoke", "persistent-history");
'
rm "${fixture_home}/.pi/agent/auth.json"
"$YOLOCTL" box restart "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
in_box node -e 'if (process.env.OPENAI_API_KEY !== "synthetic-first") process.exit(1)'
"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
in_box node -e 'if (process.env.OPENAI_API_KEY !== "synthetic-replacement") process.exit(1)'
assert_native_state() {
	in_box node -e '
const fs = require("node:fs");
const assert = require("node:assert/strict");
assert.equal(JSON.parse(fs.readFileSync("/home/sandbox/.pi/agent/auth.json")).openai.key, "synthetic-agent-owned");
assert.equal(fs.readFileSync("/home/sandbox/.pi/history/yoloctl-smoke", "utf8"), "persistent-history");
assert.ok(fs.readFileSync("/home/sandbox/.pi/agent/sessions/yoloctl-smoke.jsonl", "utf8").includes("pi-response-ok"));
'
}
assert_native_state

smoke_info "checking Pi state across isolated VM replacement"
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
assert_native_state
in_box node --input-type=module <"$(dirname "${BASH_SOURCE[0]}")/pi-runtime.mjs"

if [[ -n "${SMOKE_PI_OPENAI_API_KEY:-}" ]]; then
	smoke_info "running the opted-in OpenAI API and shell-tool request"
	"$YOLOCTL" account secret set "$ACCOUNT" OPENAI_API_KEY --space "$SMOKE_SPACE" --from-env=SMOKE_PI_OPENAI_API_KEY
	"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
	# Stored credentials take precedence over environment keys in Pi. Remove only
	# this smoke account's synthetic OpenAI credential before checking injection.
	in_box node -e '
const fs = require("node:fs");
const path = "/home/sandbox/.pi/agent/auth.json";
const auth = JSON.parse(fs.readFileSync(path)); delete auth.openai;
fs.writeFileSync(path, JSON.stringify(auth), {mode: 0o600});
'
	: "${SMOKE_PI_MODEL:?set SMOKE_PI_MODEL to an OpenAI model available to your test key}"
	in_pi --provider openai --model "$SMOKE_PI_MODEL" --no-extensions --tools bash --mode json \
		-p 'Use bash to execute: printf pi-live-ok > /workdir/pi-live-smoke. Then stop.' \
		>"${SMOKE_ROOT}/pi-response.json"
	in_box grep -qx 'pi-live-ok' /workdir/pi-live-smoke
else
	smoke_info "SKIP: real provider request (set SMOKE_PI_OPENAI_API_KEY and SMOKE_PI_MODEL); OAuth and interactive TUI need manual validation"
fi
smoke_info "PASS: Pi image, endpoint policy, secret delivery, local tool/skill fixture and native-state persistence"
