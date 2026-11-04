#!/usr/bin/env bash
# Isolated Gemini image, policy and account smoke. No provider credentials are
# needed for the default path. SMOKE_GEMINI_API_KEY opts into a real API/tool
# request; it is delivered as an account secret and never printed.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
BOX=smoke-gemini
ACCOUNT=smoke-gemini

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

smoke_info "importing a synthetic Gemini login from a disposable file"
fixture_home="${SMOKE_ROOT}/gemini-host"
mkdir -p "${fixture_home}/.gemini"
printf '%s\n' '{"refresh_token":"synthetic-import"}' >"${fixture_home}/.gemini/oauth_creds.json"
chmod 0600 "${fixture_home}/.gemini/oauth_creds.json"
"$YOLOCTL" account auth import "$ACCOUNT" gemini --space "$SMOKE_SPACE" --file "${fixture_home}/.gemini/oauth_creds.json"
printf '%s' 'synthetic-first' | "$YOLOCTL" account secret set "$ACCOUNT" GEMINI_API_KEY --space "$SMOKE_SPACE"

smoke_info "building Gemini latest and checking image-owned configuration"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent gemini --account "$ACCOUNT" \
	--workdir-mode ephemeral --no-attach --no-autoupdate
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }
in_box gemini --version
in_box node -e '
const fs = require("node:fs");
const assert = require("node:assert/strict");
assert.deepEqual(JSON.parse(fs.readFileSync("/etc/gemini-cli/settings.json")), {
  general: {enableAutoUpdate: false, enableAutoUpdateNotification: false}
});
assert.deepEqual(JSON.parse(fs.readFileSync("/etc/gemini-cli/system-defaults.json")), {
  privacy: {usageStatisticsEnabled: false},
  tools: {shell: {enableInteractiveShell: false}}
});
assert.equal(process.env.GEMINI_API_KEY, "synthetic-first");
assert.equal(JSON.parse(fs.readFileSync("/home/sandbox/.gemini/oauth_creds.json")).refresh_token, "synthetic-import");
'
diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE")"
[[ "$diff_output" == "PLAN: no changes" ]] || smoke_fail "Gemini box has drift: $diff_output"

smoke_info "checking allowed Node HTTPS and denied curl egress"
hosts=(generativelanguage.googleapis.com cloudcode-pa.googleapis.com oauth2.googleapis.com www.googleapis.com)
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
	smoke_fail "curl borrowed Gemini's Node egress grant"
fi
for _attempt in {1..10}; do
	log_output="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
	if grep -q "binary '/usr/bin/curl' not allowed in policy 'gemini'" <<<"$log_output"; then
		break
	fi
	sleep 1
done
grep -q "binary '/usr/bin/curl' not allowed in policy 'gemini'" <<<"$log_output" ||
	smoke_fail "missing attributed Gemini egress denial"
smoke_require_allowed_egress "$log_output" gemini /usr/bin/node "${hosts[@]}"

smoke_info "checking import refusal and create-time secret replacement"
if "$YOLOCTL" account auth import "$ACCOUNT" gemini --space "$SMOKE_SPACE" --file "${fixture_home}/.gemini/oauth_creds.json" \
	>"${SMOKE_ROOT}/gemini-import-denied.log" 2>&1; then
	smoke_fail "native import overwrote an active account"
fi
grep -qi 'running' "${SMOKE_ROOT}/gemini-import-denied.log" ||
	smoke_fail "import failed for a reason other than an active consumer"
printf '%s' 'synthetic-replacement' | "$YOLOCTL" account secret set "$ACCOUNT" GEMINI_API_KEY --space "$SMOKE_SPACE"
in_box node -e '
const fs = require("node:fs");
fs.writeFileSync("/home/sandbox/.gemini/oauth_creds.json", JSON.stringify({refresh_token:"synthetic-agent-owned"}));
fs.mkdirSync("/home/sandbox/.gemini/history", {recursive:true});
fs.writeFileSync("/home/sandbox/.gemini/history/yoloctl-smoke", "persistent-history");
'
rm "${fixture_home}/.gemini/oauth_creds.json"
"$YOLOCTL" box restart "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
in_box node -e 'if (process.env.GEMINI_API_KEY !== "synthetic-first") process.exit(1)'
"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
in_box node -e 'if (process.env.GEMINI_API_KEY !== "synthetic-replacement") process.exit(1)'
assert_native_state() {
	in_box node -e '
const fs = require("node:fs");
const assert = require("node:assert/strict");
assert.equal(JSON.parse(fs.readFileSync("/home/sandbox/.gemini/oauth_creds.json")).refresh_token, "synthetic-agent-owned");
assert.equal(fs.readFileSync("/home/sandbox/.gemini/history/yoloctl-smoke", "utf8"), "persistent-history");
'
}
assert_native_state

smoke_info "checking Gemini state across isolated VM replacement"
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
assert_native_state

if [[ -n "${SMOKE_GEMINI_API_KEY:-}" ]]; then
	smoke_info "running the opted-in Gemini API and shell-tool request"
	"$YOLOCTL" account secret set "$ACCOUNT" GEMINI_API_KEY --space "$SMOKE_SPACE" --from-env=SMOKE_GEMINI_API_KEY
	"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
	# Run the real CLI through OpenShell. The image default disables shell PTYs;
	# this request must successfully execute a shell tool without that grant.
	in_box env NO_BROWSER=true GEMINI_SANDBOX=false gemini --approval-mode yolo \
		--prompt 'Use run_shell_command to execute: printf gemini-shell-ok > /workdir/gemini-shell-smoke. Then stop.' \
		--output-format json >"${SMOKE_ROOT}/gemini-response.json"
	in_box grep -qx 'gemini-shell-ok' /workdir/gemini-shell-smoke
else
	smoke_info "SKIP: real API/tool request (set SMOKE_GEMINI_API_KEY); Google login and interactive TUI require manual validation"
fi
smoke_info "PASS: Gemini image, endpoint policy, secret delivery and synthetic native-state persistence"
