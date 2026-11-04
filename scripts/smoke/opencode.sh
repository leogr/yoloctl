#!/usr/bin/env bash
# Isolated OpenCode image, policy and account smoke. No provider credentials are
# needed for the default path. SMOKE_OPENCODE_OPENAI_API_KEY opts into a real API/tool
# request; it is delivered as an account secret and never printed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
BOX=smoke-opencode
ACCOUNT=smoke-opencode

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

smoke_info "importing a synthetic OpenCode login from a disposable file"
fixture_home="${SMOKE_ROOT}/opencode-host"
mkdir -p "${fixture_home}/.local/share/opencode"
printf '%s\n' '{"openai":{"type":"api","key":"synthetic-import"}}' >"${fixture_home}/.local/share/opencode/auth.json"
chmod 0600 "${fixture_home}/.local/share/opencode/auth.json"
"$YOLOCTL" account auth import "$ACCOUNT" opencode --space "$SMOKE_SPACE" --file "${fixture_home}/.local/share/opencode/auth.json"
printf '%s' 'synthetic-first' | "$YOLOCTL" account secret set "$ACCOUNT" OPENAI_API_KEY --space "$SMOKE_SPACE"

smoke_info "building OpenCode latest and checking auth delivery and search tools"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent opencode --account "$ACCOUNT" \
	--workdir-mode ephemeral --features yolo.space.operator --no-attach --no-autoupdate
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }
in_opencode() {
 in_box env XDG_DATA_HOME=/home/sandbox/.local/share \
  XDG_CONFIG_HOME=/home/sandbox/.local/share/opencode/config \
  XDG_CACHE_HOME=/home/sandbox/.local/share/opencode/cache \
  XDG_STATE_HOME=/home/sandbox/.local/share/opencode/state \
  NPM_CONFIG_CACHE=/home/sandbox/.local/share/opencode/cache/npm \
  OPENCODE_DISABLE_AUTOUPDATE=true OPENCODE_DISABLE_MODELS_FETCH=true \
  OPENCODE_DISABLE_LSP_DOWNLOAD=true OPENCODE_CONFIG_CONTENT='{"share":"disabled"}' \
  NO_PROXY=localhost,127.0.0.1,::1 opencode "$@"
}
in_opencode --version
in_box rg --version
in_opencode auth list >"${SMOKE_ROOT}/opencode-auth-check.txt"
in_box node -e '
const fs = require("node:fs");
const assert = require("node:assert/strict");
assert.equal(process.env.OPENAI_API_KEY, "synthetic-first");
assert.equal(JSON.parse(fs.readFileSync("/home/sandbox/.local/share/opencode/auth.json")).openai.key, "synthetic-import");
'
diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE")"
[[ "$diff_output" == "PLAN: no changes" ]] || smoke_fail "OpenCode box has drift: $diff_output"

smoke_info "checking real OpenCode plugin installation/loading, shell execution, operator skills and saved sessions"
in_box node --input-type=module <"$(dirname "${BASH_SOURCE[0]}")/opencode-runtime.mjs"

smoke_info "checking native executable attribution and HTTPS endpoints"
in_box env BUN_BE_BUN=1 opencode -e '
const fs = require("node:fs");
if (fs.realpathSync(process.execPath) !== "/home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe") process.exit(1);
'
hosts=(opencode.ai api.anthropic.com api.openai.com auth.openai.com chatgpt.com generativelanguage.googleapis.com openrouter.ai)
for host in "${hosts[@]}"; do
 smoke_info "probing HTTPS ${host}"
 status="$(in_box env BUN_BE_BUN=1 opencode -e '
const res = await fetch("https://" + process.argv[1] + "/", {signal: AbortSignal.timeout(15000), redirect: "manual"});
console.log(res.status);
await res.body?.cancel();
' "$host")"
 smoke_check_https_status "$host" "$status"
done
in_box env BUN_BE_BUN=1 opencode -e '
const res = await fetch("https://registry.npmjs.org/@opencode-ai%2Fplugin", {signal: AbortSignal.timeout(15000)});
if (!res.ok || (await res.json()).name !== "@opencode-ai/plugin") process.exit(1);
'
for host in generativelanguage.googleapis.com registry.npmjs.org; do
if in_box curl -sS --max-time 5 -o /dev/null "https://$host/" 2>/dev/null; then
 smoke_fail "curl borrowed OpenCode egress"
fi
if in_box node -e '
const https = require("node:https");
const req = https.get("https://" + process.argv[1] + "/", res => {res.resume()});
req.setTimeout(5000, () => req.destroy(new Error("timeout")));
req.on("error", () => {process.exitCode = 1});
' "$host" 2>/dev/null; then
 smoke_fail "Node borrowed OpenCode egress"
fi
done
for _attempt in {1..10}; do
 log_output="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
 if grep -q "binary '/usr/bin/curl' not allowed in policy 'opencode'" <<<"$log_output" &&
    grep -q "binary '/usr/bin/node' not allowed in policy 'opencode'" <<<"$log_output"; then
  break
 fi
 sleep 1
done
for binary in curl node; do
 grep -q "binary '/usr/bin/$binary' not allowed in policy 'opencode'" <<<"$log_output" ||
  smoke_fail "missing attributed OpenCode egress denial for $binary"
done
smoke_require_allowed_egress "$log_output" opencode \
 /home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe "${hosts[@]}" registry.npmjs.org

smoke_info "checking import refusal and create-time secret replacement"
if "$YOLOCTL" account auth import "$ACCOUNT" opencode --space "$SMOKE_SPACE" --file "${fixture_home}/.local/share/opencode/auth.json" \
	>"${SMOKE_ROOT}/opencode-import-denied.log" 2>&1; then
	smoke_fail "native import overwrote an active account"
fi
grep -qi 'running' "${SMOKE_ROOT}/opencode-import-denied.log" ||
	smoke_fail "import failed for a reason other than an active consumer"
printf '%s' 'synthetic-replacement' | "$YOLOCTL" account secret set "$ACCOUNT" OPENAI_API_KEY --space "$SMOKE_SPACE"
in_box node -e '
const fs = require("node:fs");
fs.writeFileSync("/home/sandbox/.local/share/opencode/auth.json", JSON.stringify({openai:{type:"api",key:"synthetic-agent-owned"}}));
fs.mkdirSync("/home/sandbox/.local/share/opencode/history", {recursive:true});
fs.writeFileSync("/home/sandbox/.local/share/opencode/history/yoloctl-smoke", "persistent-history");
'
rm "${fixture_home}/.local/share/opencode/auth.json"
"$YOLOCTL" box restart "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
in_box node -e 'if (process.env.OPENAI_API_KEY !== "synthetic-first") process.exit(1)'
"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
in_box node -e 'if (process.env.OPENAI_API_KEY !== "synthetic-replacement") process.exit(1)'
assert_native_state() {
	in_box node -e '
const fs = require("node:fs");
const assert = require("node:assert/strict");
assert.equal(JSON.parse(fs.readFileSync("/home/sandbox/.local/share/opencode/auth.json")).openai.key, "synthetic-agent-owned");
assert.equal(fs.readFileSync("/home/sandbox/.local/share/opencode/history/yoloctl-smoke", "utf8"), "persistent-history");
assert.ok(fs.statSync("/home/sandbox/.local/share/opencode/opencode.db").size > 0);
assert.ok(fs.readFileSync("/home/sandbox/.local/share/opencode/yoloctl-smoke-session", "utf8").startsWith("ses_"));
'
}
assert_native_state

smoke_info "checking OpenCode state across isolated VM replacement"
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
assert_native_state
in_box node --input-type=module <"$(dirname "${BASH_SOURCE[0]}")/opencode-runtime.mjs"

if [[ -n "${SMOKE_OPENCODE_OPENAI_API_KEY:-}" ]]; then
	smoke_info "running the opted-in OpenAI API and shell-tool request"
	"$YOLOCTL" account secret set "$ACCOUNT" OPENAI_API_KEY --space "$SMOKE_SPACE" --from-env=SMOKE_OPENCODE_OPENAI_API_KEY
	"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
	# Remove this smoke account's synthetic stored credential before checking injection.
	in_box node -e '
const fs = require("node:fs");
const path = "/home/sandbox/.local/share/opencode/auth.json";
const auth = JSON.parse(fs.readFileSync(path)); delete auth.openai;
fs.writeFileSync(path, JSON.stringify(auth), {mode: 0o600});
'
	: "${SMOKE_OPENCODE_MODEL:?set SMOKE_OPENCODE_MODEL to an OpenAI model available to your test key}"
	in_opencode run --auto --format json --model "openai/$SMOKE_OPENCODE_MODEL" 'Use bash to execute: printf opencode-live-ok > /workdir/opencode-live-smoke. Then stop.' \
		>"${SMOKE_ROOT}/opencode-response.json"
	in_box grep -qx 'opencode-live-ok' /workdir/opencode-live-smoke
else
	smoke_info "SKIP: real provider request (set SMOKE_OPENCODE_OPENAI_API_KEY and SMOKE_OPENCODE_MODEL); OAuth and interactive TUI need manual validation"
fi
smoke_info "PASS: OpenCode image, endpoint policy, npm plugin SDK and hook, secret delivery, local tool/skill fixture and native-state persistence"
