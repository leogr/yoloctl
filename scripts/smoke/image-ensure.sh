#!/usr/bin/env bash
# Live-VM smoke for the on-demand base + feature-composed image path.
#
# The Claude and Codex features deliberately remain @latest. --no-autoupdate only
# suppresses the separate post-create registry refresh so the second create
# proves the cached composed image can be selected without rebuilding.

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

BOX="smoke-image-ensure"
BOX_CACHED="smoke-image-cached"
ACCOUNT="smoke-image"
CACHE_ACCOUNT="smoke-image-cache"
BASE_IMAGE="yoloctl/agentbox:ubuntu-24.04"
FIRST_CREATE_LOG="${SMOKE_ROOT}/image-first-create.log"
SECOND_CREATE_LOG="${SMOKE_ROOT}/image-second-create.log"
AUTOUPDATE_LOG="${SMOKE_ROOT}/image-autoupdate.log"
REBUILD_LOG="${SMOKE_ROOT}/image-rebuild.log"
REFRESH_LOG="${SMOKE_ROOT}/image-base-refresh.log"
CLAUDE_PACKAGE_JSON="/home/sandbox/.npm-global/lib/node_modules/@anthropic-ai/claude-code/package.json"
NPM_MAINTENANCE_CACHE="/home/sandbox/.npm-global/.cache/npm"
CACHE_TOKEN="$(python3 -c 'import secrets; print(secrets.token_hex(16))')"

# Check actual unpacked layer usage, not just the generated recipe. A late
# recursive chown used to duplicate the entire npm tree in this final layer.
check_image_ownership_layer() {
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
		nerdctl image history --no-trunc --human=false --format '{{json .}}' "$runtime_image" \
		>"${SMOKE_ROOT}/image-history.jsonl"
	python3 - "${SMOKE_ROOT}/image-history.jsonl" <<'PY'
import json
import sys
with open(sys.argv[1]) as source:
    history = [json.loads(line) for line in source if line.strip()]
repair = [item for item in history if "find /home/sandbox -xdev" in item["CreatedBy"]]
assert len(repair) == 1, f"expected one home repair layer, got {len(repair)}"
size = int(repair[0]["Size"])
assert 0 <= size < 1024 * 1024, f"home repair duplicated package data: {size} bytes"
print(f"home repair layer: {size} bytes")
PY
}

# A changed ARG reruns the probe without --no-cache: that flag would clear the
# very mounts being inspected. Markers distinguish retained downloads from a
# cache silently reset and repopulated by the refresh. No account data is used.
check_package_caches() {
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- sh -ceu '
d=$(mktemp -d /tmp/yoloctl-package-cache-smoke.XXXXXX)
trap '\''rm -rf "$d"'\'' EXIT
cat > "$d/Dockerfile"
nerdctl build --build-arg "SMOKE_PHASE=$1" --build-arg "SMOKE_TOKEN=$2" \
    -t yoloctl-smoke/package-cache-probe "$d"
' sh "$1" "$CACHE_TOKEN" <<EOF
FROM $runtime_image
ARG SMOKE_PHASE
ARG SMOKE_TOKEN
RUN --mount=type=cache,id=yoloctl-apt-archives-v1,target=/var/cache/yoloctl/apt,sharing=locked \
    --mount=type=cache,id=yoloctl-apt-lists-v1,target=/var/lib/apt/lists,sharing=locked \
    test -n "\$(find /var/lib/apt/lists -maxdepth 1 -name '*InRelease' -print -quit)" \
    && test -n "\$(find /var/cache/yoloctl/apt -maxdepth 1 -name 'python3.13_*.deb' -print -quit)" \
    && if [ "\$SMOKE_PHASE" = before ]; then printf '%s' "\$SMOKE_TOKEN" > /var/cache/yoloctl/apt/smoke-retained; \
       else test "\$(cat /var/cache/yoloctl/apt/smoke-retained)" = "\$SMOKE_TOKEN" && rm /var/cache/yoloctl/apt/smoke-retained; fi
RUN if [ -d /var/cache/yoloctl/apt ]; then test -z "\$(find /var/cache/yoloctl/apt -type f -print -quit)"; fi
RUN --mount=type=cache,id=yoloctl-apt-lists-v1,target=/var/lib/apt/lists,sharing=locked \
    if [ "\$SMOKE_PHASE" = before ]; then printf '%s' "\$SMOKE_TOKEN" > /var/lib/apt/lists/smoke-retained; \
    else test "\$(cat /var/lib/apt/lists/smoke-retained)" = "\$SMOKE_TOKEN" && rm /var/lib/apt/lists/smoke-retained; fi
RUN if [ -d /var/lib/apt/lists ]; then test -z "\$(find /var/lib/apt/lists -type f -print -quit)"; fi
RUN --mount=type=cache,id=yoloctl-npm-v1,target=/var/cache/yoloctl/npm,sharing=locked,mode=0777 \
    test -d /var/cache/yoloctl/npm/0/_cacache \
    && if [ "\$SMOKE_PHASE" = before ]; then printf '%s' "\$SMOKE_TOKEN" > /var/cache/yoloctl/npm/smoke-retained; \
       else test "\$(cat /var/cache/yoloctl/npm/smoke-retained)" = "\$SMOKE_TOKEN" && rm /var/cache/yoloctl/npm/smoke-retained; fi
RUN if [ -d /var/cache/yoloctl/npm ]; then test -z "\$(find /var/cache/yoloctl/npm -type f -print -quit)"; fi
USER sandbox
RUN --mount=type=cache,id=yoloctl-npm-v1,target=/var/cache/yoloctl/npm,sharing=locked,mode=0777 \
    mkdir -p /var/cache/yoloctl/npm/\$(id -u) && touch /var/cache/yoloctl/npm/\$(id -u)/nonroot-probe
EOF
}

cleanup() {
	local status=$?
	set +e
	if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
		printf 'Smoke failed; the isolated VM may still be running.\n' >&2
		printf 'VM disks, configuration and logs retained at %s\n' "$SMOKE_ROOT" >&2
		printf 'Do not move this directory; copy individual logs instead.\n' >&2
		printf 'To stop the test VM: YOLOCTL_HOME=%q XDG_CONFIG_HOME=%q XDG_CACHE_HOME=%q %q space stop %q\n' \
			"$YOLOCTL_HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$YOLOCTL" "$SMOKE_SPACE" >&2
		return
	fi
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != "1" ]]; then
		smoke_destroy_box "$BOX"
		smoke_destroy_box "$BOX_CACHED"
		smoke_destroy_account "$ACCOUNT"
		smoke_destroy_account "$CACHE_ACCOUNT"
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_destroy_box "$BOX"
smoke_destroy_box "$BOX_CACHED"
smoke_destroy_account "$ACCOUNT"
smoke_destroy_account "$CACHE_ACCOUNT"
smoke_ensure_space

smoke_info "removing yoloctl base/composed images from the isolated VM"
while IFS= read -r image_ref; do
	case "$image_ref" in
		yoloctl/agentbox-composed:*)
			"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
				nerdctl image rm -f "$image_ref" >/dev/null 2>&1 || true
			;;
	esac
done < <("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl images --format '{{.Repository}}:{{.Tag}}')
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl image rm -f "$BASE_IMAGE" >/dev/null 2>&1 || true

if "$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl image inspect "$BASE_IMAGE" >/dev/null 2>&1; then
	smoke_fail "precondition failed: $BASE_IMAGE is still cached"
fi

smoke_info "creating a Claude + Codex + Go box from an empty yoloctl image cache"
"$YOLOCTL" account create "$ACCOUNT" --space "$SMOKE_SPACE"
"$YOLOCTL" box create --no-attach "$BOX" \
	--space "$SMOKE_SPACE" \
	--agent claude \
	--account "$ACCOUNT" \
	--features go,codex \
	--workdir-mode ephemeral \
	--no-autoupdate 2>&1 | tee "$FIRST_CREATE_LOG"
first_create_output="$(<"$FIRST_CREATE_LOG")"
# Native creation reports each controller image job's stage before its backend
# runs ("Building image <id>", "Pulling image <id>" or "Retaining local image
# <id>", internal/controller/image_jobs.go); the older "not present in space;
# building" notice belongs to the non-native path. The absent-before /
# present-after image checks below also prove the build.
if [[ "$first_create_output" != *"Building image "* &&
	"$first_create_output" != *"not present in space; building"* ]]; then
	smoke_fail "fresh create did not report an on-demand image build"
fi

inspect_json="$("$YOLOCTL" box inspect "$BOX" --space "$SMOKE_SPACE" --json)"
runtime_image="$(
	printf '%s\n' "$inspect_json" |
		awk -F'"' '/"runtime_image"[[:space:]]*:/ { print $4; exit }'
)"
case "$runtime_image" in
	yoloctl/agentbox-composed:*) ;;
	*) smoke_fail "expected a composed runtime image, got '${runtime_image:-<empty>}'" ;;
esac

"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl image inspect "$BASE_IMAGE" >/dev/null ||
	smoke_fail "$BASE_IMAGE was not built"
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl image inspect "$runtime_image" >/dev/null ||
	smoke_fail "$runtime_image was not built"
smoke_check_image_identity "$BOX"
check_image_ownership_layer
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- go version
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- claude --version
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- codex --version
smoke_info "asserting Codex's bubblewrap prerequisite is available on the confined PATH"
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- sh -ceu 'command -v bwrap; bwrap --version'

smoke_info "creating a second box and asserting the composed image is selected from cache"
"$YOLOCTL" account create "$CACHE_ACCOUNT" --space "$SMOKE_SPACE"
"$YOLOCTL" box create --no-attach "$BOX_CACHED" \
	--space "$SMOKE_SPACE" \
	--agent claude \
	--account "$CACHE_ACCOUNT" \
	--features go,codex \
	--workdir-mode ephemeral \
	--no-autoupdate 2>&1 | tee "$SECOND_CREATE_LOG"
second_create_output="$(<"$SECOND_CREATE_LOG")"
if [[ "$second_create_output" == *"Building image "* ||
	"$second_create_output" == *"Pulling image "* ||
	"$second_create_output" == *"not present in space; building"* ||
	"$second_create_output" == *"rebuilding composed image"* ]]; then
	smoke_fail "the second compatible box unexpectedly rebuilt an image"
fi

second_runtime="$(
	"$YOLOCTL" box inspect "$BOX_CACHED" --space "$SMOKE_SPACE" --json |
		awk -F'"' '/"runtime_image"[[:space:]]*:/ { print $4; exit }'
)"
[[ "$second_runtime" == "$runtime_image" ]] ||
	smoke_fail "runtime image differs across compatible boxes: $runtime_image -> $second_runtime"

smoke_info "base refresh and composed rebuild retain downloads without baking caches into images"
check_package_caches before
"$YOLOCTL" image refresh --space "$SMOKE_SPACE" 2>&1 | tee "$REFRESH_LOG"
"$YOLOCTL" box reconcile "$BOX" --space "$SMOKE_SPACE" --rebuild 2>&1 | tee "$REBUILD_LOG"
smoke_check_image_identity "$BOX"
check_image_ownership_layer
check_package_caches after

smoke_info "poisoning the default npm cache and forcing lifecycle autoupdate drift"
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl exec --user 0 "$BOX" rm -rf /home/sandbox/.npm
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
	nerdctl exec --user 0 "$BOX" mkdir -p /home/sandbox/.npm
poison_owner="$(
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
		nerdctl exec --user 0 "$BOX" stat -c %u /home/sandbox/.npm
)"
[[ "$poison_owner" == "0" ]] ||
	smoke_fail "poisoned ~/.npm owner = $poison_owner, want root (0)"

"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- \
	node -e 'const fs = require("fs"); const p = process.argv[1]; const v = JSON.parse(fs.readFileSync(p)); v.version = "0.0.0-yoloctl-smoke"; fs.writeFileSync(p, JSON.stringify(v));' \
	"$CLAUDE_PACKAGE_JSON"

"$YOLOCTL" box stop "$BOX" --space "$SMOKE_SPACE"
"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach 2>&1 | tee "$AUTOUPDATE_LOG"
autoupdate_output="$(<"$AUTOUPDATE_LOG")"
if [[ "$autoupdate_output" != *"autoupdate: @anthropic-ai/claude-code 0.0.0-yoloctl-smoke"* ]]; then
	smoke_fail "default start did not report the forced Claude autoupdate"
fi

installed_version="$(
	"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- \
		node -p 'require(process.argv[1]).version' "$CLAUDE_PACKAGE_JSON"
)"
[[ "$installed_version" != "0.0.0-yoloctl-smoke" ]] ||
	smoke_fail "autoupdate left the forced stale package version in place"

cache_owner="$(
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
		nerdctl exec --user 0 "$BOX" stat -c %u:%g "$NPM_MAINTENANCE_CACHE"
)" || smoke_fail "stat $NPM_MAINTENANCE_CACHE failed — autoupdate did not create the maintenance cache?"
# GID 20 can resolve to dialout even when sandbox uses that same numeric GID.
sandbox_owner="$(
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
		nerdctl exec --user 0 "$BOX" sh -c 'printf "%s:%s\n" "$(id -u sandbox)" "$(id -g sandbox)"'
)" || smoke_fail "resolving sandbox identity failed"
[[ "$cache_owner" == "$sandbox_owner" ]] ||
	smoke_fail "maintenance cache owner = $cache_owner, want sandbox identity $sandbox_owner"
poison_owner_after="$(
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
		nerdctl exec --user 0 "$BOX" stat -c %u /home/sandbox/.npm
)" || smoke_fail "stat poisoned ~/.npm failed — autoupdate removed it?"
[[ "$poison_owner_after" == "0" ]] ||
	smoke_fail "autoupdate unexpectedly repaired or replaced poisoned ~/.npm (owner $poison_owner_after)"

smoke_info "PASS: fresh build, cached selection, thin ownership layers, apt/npm build caches and isolated autoupdate"
