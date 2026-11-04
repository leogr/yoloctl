#!/usr/bin/env bash
# Isolated npm development environment and confined registry smoke.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

BOX=smoke-npm
cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		if [[ "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 && "${SMOKE_PASSED:-0}" != 1 ]]; then
			printf 'Retaining failed fixture at %s\n' "$SMOKE_ROOT" >&2
			return
		fi
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_ensure_space
smoke_destroy_box "$BOX"
smoke_info "npm: JavaScript environment with confined registry access"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent none \
	--features npm --workdir-mode ephemeral --no-attach --no-autoupdate
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }

smoke_info "npm: runtime, user configuration, install, lockfile, scripts and npx"
in_box sh -se <<'SH'
test "$(id -u)" != 0
node --version
npm --version
npx --version
test "$(npm config get prefix)" = /home/sandbox/.npm-global
test "$(npm config get cache)" = /home/sandbox/.cache/npm
test -w /home/sandbox/.npmrc
test -w /home/sandbox/.npm-global
test -w /home/sandbox/.cache
test -w /home/sandbox/.cache/npm

cd /workdir
node - <<'JS'
const fs = require('node:fs');
fs.writeFileSync('package.json', JSON.stringify({
  name: 'yoloctl-npm-smoke', version: '1.0.0', private: true,
  scripts: {build: 'node build.cjs', test: 'node test.cjs'}
}));
fs.writeFileSync('build.cjs', `
const fs = require('node:fs');
const semver = require('semver');
fs.mkdirSync('dist', {recursive:true});
fs.writeFileSync('dist/result.json', JSON.stringify({version: semver.valid('1.2.3')}));
`);
fs.writeFileSync('test.cjs', `
const assert = require('node:assert/strict');
assert.equal(require('./dist/result.json').version, '1.2.3');
`);
JS
# Pin this small dependency as a fixture, not the development environment.
npm install --save-exact --no-audit --no-fund --fetch-retries=0 --fetch-timeout=15000 semver@7.7.2
test -f package-lock.json
node -e 'const f = require("node:fs"); f.copyFileSync("package-lock.json", "expected-lock.json")'
rm -rf node_modules
npm ci --offline --no-audit --no-fund
cmp package-lock.json expected-lock.json
npm run build
npm test
test "$(npx --offline --yes semver 1.2.3)" = 1.2.3

# No local dependency or global installation may satisfy this npx request.
npx_dir=$(mktemp -d /tmp/yoloctl-npx.XXXXXX)
cd "$npx_dir"
test "$(npx --yes --fetch-retries=0 --fetch-timeout=15000 --package=semver@7.7.2 semver 2.3.4)" = 2.3.4
cd /workdir
rm -rf "$npx_dir"

npm install -g --offline --no-audit --no-fund semver@7.7.2
test "$(command -v semver)" = /home/sandbox/.npm-global/bin/semver
test "$(semver 3.4.5)" = 3.4.5
node - <<'JS'
const fs = require('node:fs');
const assert = require('node:assert/strict');
for (const path of ['/home/sandbox/.npmrc', '/home/sandbox/.npm-global', '/home/sandbox/.cache', '/home/sandbox/.cache/npm']) {
  assert.equal(fs.statSync(path).uid, process.getuid(), path);
  assert.equal(fs.statSync(path).gid, process.getgid(), path);
}
JS
SH

smoke_info "npm: curl cannot borrow the registry grant"
if in_box curl -fsS --max-time 5 -o /dev/null https://registry.npmjs.org/semver 2>/dev/null; then
	smoke_fail "curl borrowed npm registry egress"
fi
logs="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1)"
grep -q "binary '/usr/bin/curl' not allowed in policy 'npm'" <<<"$logs" ||
	smoke_fail "missing attributed curl denial"

diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE" 2>&1)"
[[ "$diff_output" == "PLAN: no changes" ]] || {
	printf '%s\n' "$diff_output" >&2
	smoke_fail "npm environment has unexpected reconcile drift"
}
SMOKE_PASSED=1
smoke_info "PASS: npm install/ci/run/test, npx, global commands and confined registry access"
