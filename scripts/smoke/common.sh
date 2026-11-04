#!/usr/bin/env bash
# Shared setup for VM-required smoke tests.
#
# Every smoke run sets an owned YOLOCTL_HOME, ignoring inherited overrides. That gives it a
# dedicated yoloctl Lima home and prevents it from seeing or mutating the
# caller's spaces, accounts, boxes, or context.

smoke_info() {
	printf '>>> %s\n' "$*"
}

smoke_fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

# Unauthenticated provider roots can return redirects, challenges, rate limits
# or 421. This checks reachability, not API health/authentication. Also require
# the sandbox's attributed ALLOWED event; a proxy's HTTP error is not proof of
# an allowed connection. Keep upstream/proxy failures actionable.
smoke_check_https_status() {
	local host="$1" status="$2"
	smoke_info "HTTPS ${host}: HTTP ${status}"
	[[ "$status" != 407 ]] || smoke_fail "HTTPS ${host}: proxy authentication required (HTTP 407)"
	case "$status" in
		[234][0-9][0-9]) ;;
		*) smoke_fail "HTTPS ${host}: unexpected HTTP status ${status}" ;;
	esac
}

smoke_require_allowed_egress() {
	local logs="$1" policy="$2" binary="$3" host
	shift 3
	for host in "$@"; do
		if ! printf '%s\n' "$logs" | grep -F 'OCSF NET:OPEN ' |
			grep -F "ALLOWED ${binary}(" |
			grep -F -- "-> ${host}:443 [policy:${policy} " >/dev/null; then
			smoke_fail "missing attributed ${policy} egress allowance for ${binary} -> ${host}:443"
		fi
	done
}

# Inspect the IMAGE, bypassing the entrypoint: inspecting the live box would
# also pass after an expensive first-start remap. This disposable, networkless
# setup probe has no account/project mounts and runs no agent workload.
smoke_check_image_identity() {
	local box="$1" runtime_image
	runtime_image="$("$YOLOCTL" box inspect "$box" --space "$SMOKE_SPACE" --json |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["manifest"]["runtime_image"])')"
	[[ "$runtime_image" == yoloctl/agentbox-composed:* ]] || smoke_fail "expected composed image for $box"
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- \
		nerdctl run --rm --network none --entrypoint /bin/sh "$runtime_image" -ceu '
expected="$1:$2"
actual="$(id -u sandbox):$(id -g sandbox)"
test "$actual" = "$expected" || { echo "image sandbox identity: $actual, expected $expected" >&2; exit 1; }
test "$(stat -c %u:%g /home/sandbox)" = "$expected"
wrong_owner=$(find /home/sandbox/.npm-global \( ! -uid "$1" -o ! -gid "$2" \) -print -quit)
test -z "$wrong_owner" || { echo "npm prefix ownership differs from image identity: $wrong_owner" >&2; exit 1; }
printf "prepared image identity: %s\n" "$actual"
' image-identity "$(id -u)" "$(id -g)"
}

smoke_require_backend() {
	case "$1" in
	lima)
		command -v limactl >/dev/null 2>&1 || smoke_fail "limactl is required for the Lima smoke tests"
		command -v ssh >/dev/null 2>&1 || smoke_fail "ssh is required by Lima"
		;;
	firecracker)
		[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 && -r /dev/kvm && -w /dev/kvm ]] || smoke_fail "Firecracker smoke requires Linux amd64 with usable /dev/kvm"
		if [[ -n "${SMOKE_VM_IMAGE:-}" ]]; then
			[[ -f "${SMOKE_VM_IMAGE}/image.json" ]] || smoke_fail "SMOKE_VM_IMAGE is not a prepared Firecracker image"
		fi
		;;
	*) smoke_fail "unknown smoke backend: $1" ;;
	esac
}

smoke_init() {
	local repo_root
	repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." >/dev/null && pwd)"
	YOLOCTL="${YOLOCTL:-${repo_root}/bin/yoloctl}"

	if [[ ! -x "$YOLOCTL" ]]; then
		smoke_fail "$YOLOCTL not found or not executable; run 'make build'"
	fi
	SMOKE_BACKEND="${SMOKE_BACKEND:-lima}"
	smoke_require_backend "$SMOKE_BACKEND"
	SMOKE_VM_ARGS=(--backend "$SMOKE_BACKEND")
	if [[ "$SMOKE_BACKEND" == firecracker && -n "${SMOKE_VM_IMAGE:-}" ]]; then
		SMOKE_VM_ARGS+=(--vm-image "$SMOKE_VM_IMAGE")
	fi
	export SMOKE_BACKEND

	# A run must not depend on who launched it: with a terminal on stdin,
	# commands prompt, lifecycle verbs attach, and `box exec` may allocate a
	# PTY. Commands that need input bring their own (a pipe, or `script` for a
	# real PTY).
	exec </dev/null

	SMOKE_OWNS_ROOT=0
	if [[ -z "${SMOKE_ROOT:-}" ]]; then
		# Darwin's default TMPDIR is too long for Lima's UNIX_PATH_MAX=104
		# sockets. Use a short, private fixture path on that host.
		if [[ "$(uname -s)" == Darwin ]]; then
			SMOKE_ROOT="$(mktemp -d /private/tmp/ys.XXXXXX)"
		else
			SMOKE_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/yoloctl-smoke.XXXXXX")"
		fi
		SMOKE_OWNS_ROOT=1
	else
		mkdir -p "$SMOKE_ROOT"
	fi

	SMOKE_SPACE="${SMOKE_SPACE:-smoke-live-vm}"
	SMOKE_WORKDIR="${SMOKE_WORKDIR:-${SMOKE_ROOT}/workdir}"
	XDG_CONFIG_HOME="${SMOKE_ROOT}/config"
	XDG_CACHE_HOME="${SMOKE_ROOT}/cache"
	mkdir -p "$SMOKE_WORKDIR" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME"

	export YOLOCTL SMOKE_ROOT SMOKE_OWNS_ROOT SMOKE_SPACE SMOKE_WORKDIR
	export XDG_CONFIG_HOME XDG_CACHE_HOME
	YOLOCTL_HOME="${SMOKE_ROOT}/home"
	export YOLOCTL_HOME
	SMOKE_CONFIG_ROOT="$("$YOLOCTL" version --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["config_root"])')"
	export SMOKE_CONFIG_ROOT
}

smoke_ensure_space() {
	smoke_assert_lima_globals_absent
	local manifest="${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/space.yaml"

	# Callers may disable errexit, including by using this function in an if.
	if [[ -f "$manifest" ]]; then
		smoke_info "starting isolated space ${SMOKE_SPACE}"
		"$YOLOCTL" space start "$SMOKE_SPACE" || return "$?"
	else
		smoke_info "creating isolated space ${SMOKE_SPACE}"
		smoke_create_space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default || return "$?"
	fi
	if [[ -n "${SMOKE_EXPECT_KERNEL_RELEASE:-}" ]]; then
		local release
		release="$("$YOLOCTL" space ssh "$SMOKE_SPACE" -- uname -r)" || return "$?"
		[[ "$release" == "$SMOKE_EXPECT_KERNEL_RELEASE" ]] ||
			smoke_fail "expected kernel $SMOKE_EXPECT_KERNEL_RELEASE, got $release"
	fi
	return 0
}

# The Lima stack gate owns a pristine private home. Recheck before each boot,
# including boots inside a reused smoke; other callers retain their behavior.
smoke_assert_lima_globals_absent() {
	if [[ "${SMOKE_ASSERT_NO_LIMA_GLOBAL_CONFIG:-0}" == 1 ]]; then
		local path
		for path in "${LIMA_HOME:?}/_config/default.yaml" "${LIMA_HOME}/_config/override.yaml"; do
			[[ ! -e "$path" && ! -L "$path" ]] || smoke_fail "isolated global configuration refused: $path"
		done
	fi
}

smoke_create_space() {
	"$YOLOCTL" space create "$@" "${SMOKE_VM_ARGS[@]}" --memory "${SMOKE_VM_MEMORY:-4GiB}" --cpus "${SMOKE_VM_CPUS:-4}"
}

smoke_destroy_box() {
	local box="$1"
	"$YOLOCTL" box destroy "$box" --space "$SMOKE_SPACE" --force >/dev/null 2>&1 || true
}

smoke_destroy_account() {
	local account="$1"
	"$YOLOCTL" account destroy "$account" --space "$SMOKE_SPACE" --force >/dev/null 2>&1 || true
}

smoke_destroy_space() {
	if ! "$YOLOCTL" space destroy "$SMOKE_SPACE" --force >/dev/null 2>&1; then
		SMOKE_CLEANUP_FAILED=1
		export SMOKE_CLEANUP_FAILED
		printf 'Cleanup incomplete; isolated recovery state retained at %s\n' "$SMOKE_ROOT" >&2
		return 1
	fi
}

smoke_remove_owned_root() {
	if [[ "${SMOKE_CLEANUP_FAILED:-0}" != 1 && "${SMOKE_OWNS_ROOT:-0}" == "1" && -n "${SMOKE_ROOT:-}" ]]; then
		rm -rf -- "$SMOKE_ROOT"
	fi
}
