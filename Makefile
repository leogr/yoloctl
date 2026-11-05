# yoloctl Makefile

BIN_DIR := bin
BIN     := $(BIN_DIR)/yoloctl
PKG     := ./...
MAIN    := ./cmd/yoloctl

GOFLAGS ?=
LDFLAGS ?=
# Optional Git metadata must not make local builds depend on working VCS tools.
# Opt in with `make BUILDVCS=true` when revision stamping is wanted.
BUILDVCS ?= false

# One identity per coordinated invocation, shared by all host/guest artifacts.
# Companion hashes still verify the exact bytes of each Linux executable.
BUNDLE_ID := $(shell od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
VERSION ?= dev
BUNDLE_LDFLAGS = $(LDFLAGS) -X github.com/leogr/yoloctl/internal/buildinfo.Version=$(VERSION) -X github.com/leogr/yoloctl/internal/controller.BuildID=$(BUNDLE_ID)

# Instrument the host with -race without requiring cross-platform C compilers
# for the static guest artifacts. Preserve semantic options such as build tags.
GUEST_GOFLAGS = $(filter-out -race -race=true -race=false,$(GOFLAGS))
# Distributed hosts must not acquire a build-machine libc dependency. The Go
# race detector is a developer-only exception and requires cgo by design.
HOST_CGO = $(if $(filter -race -race=true,$(GOFLAGS)),CGO_ENABLED=1,CGO_ENABLED=0)
BUILD_HOST_OS := $(shell go env GOHOSTOS)
BUILD_HOST_ARCH := $(shell go env GOHOSTARCH)

# Hash the actual companions, then link those hashes into every host target.
# Clear inherited GOFLAGS for the native helper and guest compiler; command-line
# GOFLAGS are applied explicitly so -race cannot leak back into static builds.
define build-host
	@hashes=$$(GOOS=$(BUILD_HOST_OS) GOARCH=$(BUILD_HOST_ARCH) GOFLAGS= go run -buildvcs=false ./scripts/guest-hashes.go "$(BIN_DIR)") && \
	$(HOST_CGO) $(1) go build -buildvcs=$(BUILDVCS) $(GOFLAGS) -ldflags "$(BUNDLE_LDFLAGS) $$hashes" -o $(2) $(MAIN)
endef

.DEFAULT_GOAL := build

.PHONY: all build build-guests build-linux-arm64 build-linux-amd64 build-darwin-arm64 build-darwin-amd64 test test-scripts test-rego vet vet-darwin lint docs-check check smoke-live-vm smoke-space-lifecycle smoke-features smoke-agent-state smoke-image-ensure smoke-box-attach smoke-account-auth smoke-gemini smoke-agy smoke-pi smoke-opencode clean help

all: build

build: native-embed build-guests $(BIN_DIR)
	$(call build-host,,$(BIN))

# Full yoloctl binaries used by session-hold inside Linux VMs. Keep these
# beside the host CLI when installing it; guest and host architectures can differ.
build-guests: build-linux-amd64 build-linux-arm64 build-vm-init

# Native boxes need the qualified OpenShell archives embedded in yoloctl
# (internal/nativeartifacts; git-ignored). native-qualify copies the built
# sandbox/supervisor archives from NATIVE_ARCHIVES after checking their
# hashes, builds and packs both static helpers, and writes the qualified
# selection beside them (internal/nativeartifacts/embedded/selection/, also
# git-ignored), so no committed file changes. make build runs it as needed; a
# plain go build without it refuses native boxes with "run make build".
NATIVE_INPUTS ?= third_party/openshell/release-inputs.json
.PHONY: native-qualify check-native-reproducible
native-qualify:
	GOOS=$(BUILD_HOST_OS) GOARCH=$(BUILD_HOST_ARCH) GOFLAGS= go run -buildvcs=false ./scripts/native-artifacts.go qualify "$(NATIVE_INPUTS)" "$(NATIVE_ARCHIVES)" "$(NATIVE_EVIDENCE)"

# Local OpenShell build: upstream + third_party/openshell patches, both static
# musl targets on the host (Linux or macOS) with cargo-zigbuild (README:
# Development), cached by input hash under bin/openshell/<key>/. native-embed
# (part of `make build`) builds when that entry is missing and runs
# native-qualify on it whenever the embedded key differs, so later builds reuse
# both. The embedded key (scripts/openshell-embed-key.py) joins the cache key
# with a hash of the Go helper's source closure, so a helper edit re-qualifies.
# native-qualify-local forces the qualify step.
OPENSHELL_CACHE_ENTRY = $(shell python3 scripts/openshell-build.py path)
OPENSHELL_EMBEDDED_KEY = bin/openshell/embedded.key
.PHONY: openshell-build openshell-build-check native-qualify-local native-embed
openshell-build:
	python3 scripts/openshell-build.py build

openshell-build-check:
	python3 scripts/openshell-build.py check

native-qualify-local: openshell-build
	$(MAKE) native-qualify NATIVE_ARCHIVES="$(OPENSHELL_CACHE_ENTRY)" NATIVE_INPUTS="$(OPENSHELL_CACHE_ENTRY)/release-inputs.json" NATIVE_EVIDENCE="$(OPENSHELL_CACHE_ENTRY)"
	python3 scripts/openshell-embed-key.py > $(OPENSHELL_EMBEDDED_KEY)

# No fallback archives: without a cached OpenShell build, a missing Rust/zig
# tool stops here with the install hint, before the build starts.
native-embed:
	@if [ ! -f "$(OPENSHELL_CACHE_ENTRY)/RECEIPT.json" ]; then \
		missing=; for tool in rustup zig cargo-zigbuild; do command -v $$tool >/dev/null 2>&1 || missing="$$missing $$tool"; done; \
		if [ -n "$$missing" ]; then \
			rust=$$(python3 -c 'import json; print(json.load(open("third_party/openshell/pins.json"))["rust"])'); \
			echo "make build: building OpenShell needs:$$missing" >&2; \
			echo "  install rustup (https://rustup.rs) and zig (https://ziglang.org; macOS: brew install zig), then run" >&2; \
			echo "  rustup toolchain install $$rust --target x86_64-unknown-linux-musl,aarch64-unknown-linux-musl" >&2; \
			echo "  cargo install --locked cargo-zigbuild" >&2; \
			exit 1; \
		fi; \
	fi; \
	python3 scripts/openshell-build.py build || exit 1; \
	if [ "$$(cat $(OPENSHELL_EMBEDDED_KEY) 2>/dev/null)" != "$$(python3 scripts/openshell-embed-key.py)" ] || \
		[ ! -f internal/nativeartifacts/embedded/selection/manifest.sha256 ]; then \
		$(MAKE) native-qualify-local; \
	fi

# Two builds of every embedded helper architecture, the second with a fresh
# build cache, must be byte-identical; prints recipe/toolchain/source/hashes.
check-native-reproducible:
	GOOS=$(BUILD_HOST_OS) GOARCH=$(BUILD_HOST_ARCH) GOFLAGS= go run -buildvcs=false ./scripts/native-artifacts.go reproduce

.PHONY: build-vm-init
build-vm-init: $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS= go build -buildvcs=false -trimpath -ldflags "-s -w" -o $(BIN_DIR)/yoloctl-vm-init-linux-amd64 ./cmd/yoloctl-vm-guest

build-linux-amd64: native-embed $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS= go build -buildvcs=$(BUILDVCS) $(GUEST_GOFLAGS) -ldflags "$(BUNDLE_LDFLAGS)" -o $(BIN_DIR)/yoloctl-linux-amd64 $(MAIN)

build-linux-arm64: native-embed $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOFLAGS= go build -buildvcs=$(BUILDVCS) $(GUEST_GOFLAGS) -ldflags "$(BUNDLE_LDFLAGS)" -o $(BIN_DIR)/yoloctl-linux-arm64 $(MAIN)

# Release maintenance only: ordinary builds/downloads never compile the PID 1
# helper. Freeze these inputs and arrange the shared build slot before running.
.PHONY: release-openshell-helper
release-openshell-helper:
	@test -n "$(HELPER_OUT)" -a -n "$(HELPER_NATIVE_ROOT)" -a -n "$(HELPER_GO_VERSION)" -a -n "$(HELPER_TOOLCHAIN_SHA256)" -a -n "$(HELPER_SOURCE_SHA256)"
	python3 scripts/build-openshell-helper.py --out "$(HELPER_OUT)" --native-root "$(HELPER_NATIVE_ROOT)" --go-version "$(HELPER_GO_VERSION)" --toolchain-sha256 "$(HELPER_TOOLCHAIN_SHA256)" --source-sha256 "$(HELPER_SOURCE_SHA256)"

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

test:
	go test $(GOFLAGS) $(PKG)

# Strict core corpus plus host-input/PTY acceptance (also in ordinary tests).
.PHONY: test-terminal-fidelity
test-terminal-fidelity:
	go test $(GOFLAGS) -run 'TestTerminalFidelity|TestHostCompatibility' -count=1 ./internal/tui ./internal/tui/term

# Maintenance only: normal builds embed the checked-in artifact. Requires the
# exact Zig version pinned by the builder; ZIG may name an explicit executable.
.PHONY: terminal-artifact-check
terminal-artifact-check:
	python3 scripts/build-terminal.py --check

# Maintenance only; ordinary Go checks verify the qualified plugin artifacts.
.PHONY: audit-plugin-check
audit-plugin-check:
	python3 scripts/build-audit-plugin.py --check

# Maintenance verification of the pinned Go renderer and its isolated patch.
.PHONY: host-renderer-check
host-renderer-check:
	python3 scripts/vendor-ultraviolet.py --check

test-scripts:
	python3 -B -m unittest discover -s scripts -p test_vendor_openshell.py
	python3 -B -m unittest discover -s scripts -p test_openshell_build.py
	python3 -B -m unittest discover -s scripts -p test_helper_release.py
	python3 -B -m unittest discover -s scripts -p test_release.py
	python3 -B -m unittest discover -s scripts -p test_push_main_checked.py
	python3 -B -m unittest discover -s scripts -p test_agent_observe.py
	python3 -B -m unittest discover -s scripts -p test_image_cache_probe.py
	python3 -B -m unittest discover -s scripts -p test_airc_runtime.py
	python3 -B -m unittest discover -s scripts -p test_pty_run.py
	python3 -B -m unittest discover -s scripts -p test_attach_mixed.py
	python3 -B -m unittest discover -s scripts -p test_box_use.py
	python3 -B -m unittest discover -s scripts -p test_privileged_session.py
	python3 -B -m unittest discover -s scripts -p test_privileged_runtime.py
	python3 -B -m unittest discover -s scripts -p test_host_integration.py
	python3 -B -m unittest discover -s scripts -p test_vm_smoke.py
	python3 -B -m unittest discover -s scripts -p test_lima_stack.py
	python3 -B -m unittest discover -s scripts/vm/firecracker -p 'test_*.py'

vet:
	go vet $(PKG)

# vet-darwin type-checks every package, tests included, for macOS. It catches
# Linux-only symbols in files without a linux build constraint before CI's
# native macOS job does; one architecture covers the darwin constraint.
vet-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet $(PKG)

# lint uses golangci-lint from PATH when there is one. Where nothing is
# installed (a yoloctl box), it builds the version CI pins with `go run`, so
# the pin stays in one place: the workflow. The first such run compiles the
# linter into the Go caches and takes a few minutes.
GOLANGCI_LINT_PIN = $(shell sed -n 's/^ *version: *\(v[0-9][0-9.]*\) *$$/\1/p' .github/workflows/ci.yml | head -n 1)

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run $(PKG); \
	else \
		pin="$(GOLANGCI_LINT_PIN)"; \
		[ -n "$$pin" ] || { echo "lint: no golangci-lint version pin found in .github/workflows/ci.yml" >&2; exit 1; }; \
		echo "lint: golangci-lint is not on PATH; running the CI-pinned $$pin with go run"; \
		go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$$pin run $(PKG); \
	fi

# Documentation structure is checked locally and in CI; claims still need review.
docs-check:
	python3 scripts/check-docs.py
	python3 -B -m unittest discover -s scripts -p test_check_docs.py

# check is the pre-commit/pre-push gate, matching the checks run in CI.
check: docs-check test-scripts vet vet-darwin test lint check-native-reproducible

# test-rego runs OPA's test suite against the sandbox policy. Requires
# `opa` on PATH (https://www.openpolicyagent.org/docs/latest/#running-
# opa). Not part of `check` because opa isn't a yoloctl build dep; run
# manually before changing the rego, or wire into a CI job that
# installs opa first.
test-rego:
	@if command -v opa >/dev/null 2>&1; then \
		opa test assets/sandbox/sandbox-policy.rego assets/sandbox/sandbox-policy_test.rego; \
	else \
		echo "skip: opa not on PATH (https://www.openpolicyagent.org/docs/latest/#running-opa)"; \
	fi

# smoke-live-vm provisions an isolated temporary yoloctl config/cache + VM
# space and runs these phases in order: space-lifecycle, image-ensure, box-stop, memory,
# features, npm, pip, storage-gc, agent-state, box-attach, host-integration,
# controller-operator, space-archive, gemini, agy, pi, and opencode. It never
# touches the caller's normal spaces/accounts/boxes and purges the VM on exit.
# Not part of check because first-run image builds take several minutes and
# require a working selected backend plus network access.
smoke-live-vm: build
	@scripts/smoke/live-vm.sh

.PHONY: smoke-vm-startup
smoke-vm-startup: build
	@bash scripts/smoke/vm-startup.sh

.PHONY: smoke-controller-operator smoke-space-archive
smoke-controller-operator: build
	@scripts/smoke/controller-operator.sh

# Portable declaration/native-state archive and independent restore.
smoke-space-archive: build
	@scripts/smoke/space-archive.sh

# smoke-space-lifecycle verifies that mount mutations are transactional and
# immediately live after their confirmed VM restart, space recreate preserves
# declarations, and space destroy removes the owned bundle and account disk.
smoke-space-lifecycle: build
	@scripts/smoke/space-lifecycle.sh

# smoke-features runs its isolated live-VM phase independently. It verifies
# that the computed feature policy round-trips cleanly, that curl cannot
# borrow the Claude CLI's per-binary Anthropic egress grant, and that PTY
# allocation is denied by default and works once fs.pty.allocate is granted.
smoke-features: build
	@scripts/smoke/features-as-policy.sh

# npm development tools and package operations through the confined SSH path.
.PHONY: smoke-npm
smoke-npm: build
	@scripts/smoke/npm.sh

# Go module download, verify and build through the confined SSH path.
.PHONY: smoke-go
smoke-go: build
	@scripts/smoke/go-modules.sh

# Python development tools and package operations through the confined SSH path.
.PHONY: smoke-pip
smoke-pip: build
	@scripts/smoke/pip.sh

.PHONY: smoke-disk
# Disposable Firecracker space: autosized system disk, its grow at a
# recreate, and the offline data disk grow with its checks.
smoke-disk: build
	@SMOKE_BACKEND=firecracker scripts/smoke/disk.sh

.PHONY: smoke-storage-gc
smoke-storage-gc: build
	@scripts/smoke/storage-gc.sh

# smoke-agent-state independently exercises account-owned native state:
# required initial account, native filesystem semantics, same-space sharing,
# persistence across box/space recreate, independent accounts in two spaces,
# and scoped account/space destruction.
smoke-agent-state: build
	@scripts/smoke/agent-state.sh

# smoke-image-ensure independently starts with an empty yoloctl image cache,
# creates a Claude + Codex + Go box, verifies CLI/bwrap availability, fresh
# composition and cached selection, checks ownership-layer size and the npm build cache
# after a forced rebuild, then poisons ~/.npm and proves the default lifecycle
# autoupdate uses its isolated sandbox-owned cache. Agent features stay latest;
# --no-autoupdate only separates the image-selection setup from that final
# autoupdate regression.
smoke-image-ensure: build
	@scripts/smoke/image-ensure.sh

.PHONY: smoke-box-stop
smoke-box-stop: build
	@scripts/smoke/box-stop.sh

# Disposable hosts only: creates space-VM-root boxes; needs explicit opt-in.
.PHONY: smoke-dev-privileged
smoke-dev-privileged: build
	@scripts/smoke/dev-privileged.sh

.PHONY: smoke-memory
smoke-memory: build
	@scripts/smoke/memory.sh

# smoke-box-attach verifies the lifecycle auto-attach tail end-to-end under a
# real PTY (Python on Linux/macOS): create/copy/start/restart/recreate attach
# by default (generic-shell bash -l), the session's exit status becomes the
# command's exit code, an externally terminated attach detaches (exit 0 +
# space-qualified reattach hint) while the session survives, and --no-attach
# opts out. Simultaneous tab/managed/passthrough viewers then exercise ordered
# input, queries, resize, detach/rejoin and an unread peer under changing output.
smoke-box-attach: build
	@scripts/smoke/box-attach.sh

# smoke-attach-interrupt interrupts attach clients (SIGTERM, SIGKILL, closed
# output pipe) during setup in an isolated space; one SIGTERM must detach the
# client, and another box mutation must succeed without acknowledgment.
.PHONY: smoke-attach-interrupt
smoke-attach-interrupt: build
	@scripts/smoke/attach-interrupt.sh

.PHONY: smoke-host-integration
smoke-host-integration: build
	@scripts/smoke/host-integration.sh

# smoke-account-auth runs scripts/smoke/account-auth-injection.sh against a real
# Lima VM: space create -> account create -> auth import NAME claude (extract
# ~/.claude.json primaryApiKey) -> box create, asserting the agent receives
# ANTHROPIC_API_KEY via the supervisor's env injection
# (OPENSHELL_USER_ENVIRONMENT), that replacement reaches the env only on `box
# recreate`, and that a one-time Codex auth.json import becomes agent-owned and
# survives `box restart` unchanged. Requires
# SMOKE_ANTHROPIC_API_KEY + a VM backend. Run after touching the account-auth /
# secret delivery paths or the openshell-sandbox pin.
smoke-account-auth: build
	@scripts/smoke/account-auth-injection.sh

# Gemini image/policy/native auth smoke; optional SMOKE_GEMINI_API_KEY enables
# a real headless API + shell-tool request. Uses an isolated disposable VM.
smoke-gemini: build
	@scripts/smoke/gemini.sh

# Native Antigravity image/policy/account smoke; optional personal OAuth file.
smoke-agy: build
	@scripts/smoke/agy.sh

# Pi image/policy/account smoke plus a deterministic real-CLI tool fixture.
smoke-pi: build
	@scripts/smoke/pi.sh

# OpenCode image/policy/account smoke, with a local model/tool fixture.
smoke-opencode: build
	@scripts/smoke/opencode.sh

.PHONY: smoke-airc
smoke-airc: build
	@scripts/smoke/airc.sh

# macOS cross-compilation targets (local dev on Apple Silicon or Intel Mac)
build-darwin-arm64: build-guests $(BIN_DIR)
	$(call build-host,GOOS=darwin GOARCH=arm64,$(BIN_DIR)/yoloctl-darwin-arm64)

build-darwin-amd64: build-guests $(BIN_DIR)
	$(call build-host,GOOS=darwin GOARCH=amd64,$(BIN_DIR)/yoloctl-darwin-amd64)

clean:
	rm -rf $(BIN_DIR)

help:
	@echo "Targets:"
	@echo "  build (default)         Build $(BIN) for the host plus both Linux guest binaries"
	@echo "  build-guests            Build Linux amd64 and arm64 session-holder binaries"
	@echo "  build-linux-amd64       Build the Linux amd64 binary"
	@echo "  build-linux-arm64       Build the Linux arm64 binary"
	@echo "  build-darwin-arm64      Build for macOS Apple Silicon"
	@echo "  build-darwin-amd64      Build for macOS Intel"
	@echo "  test                    Run unit tests"
	@echo "  test-terminal-fidelity  Run the core and host-input/PTY acceptance corpus"
	@echo "  terminal-artifact-check Verify the pinned WASM rebuild (requires Zig 0.16.0)"
	@echo "  test-rego               Run OPA tests on sandbox-policy.rego (requires opa on PATH)"
	@echo "  smoke-live-vm           Full isolated VM runtime smoke gate"
	@echo "  smoke-vm-startup        Fresh-space creation/restart timings and guest memory"
	@echo "  smoke-controller-operator  VM controller and in-box operator smoke gate"
	@echo "  smoke-attach-interrupt  Interrupted attach clients never pause the space"
	@echo "  smoke-space-archive Portable space archive/restore lifecycle smoke"
	@echo "  smoke-space-lifecycle   Isolated VM mount/destroy lifecycle smoke test"
	@echo "  smoke-features          Isolated VM feature-policy enforcement smoke test"
	@echo "  smoke-npm               Isolated npm environment and registry confinement smoke"
	@echo "  smoke-go                Isolated Go module download/build and proxy confinement smoke"
	@echo "  smoke-pip               Isolated Python/pip environment and PyPI confinement smoke"
	@echo "  smoke-storage-gc        Automatic VM image cleanup and retained-data smoke"
	@echo "  smoke-disk              VM system and data disk sizes and the offline data disk grow (Firecracker)"
	@echo "  smoke-agent-state       Isolated VM account-state persistence smoke test"
	@echo "  smoke-image-ensure      VM-required end-to-end image-build-on-demand smoke test"
	@echo "  smoke-box-stop          VM-required graceful stop and cached recreate regression"
	@echo "  smoke-dev-privileged    Disposable-host yolo.dev.privileged gate (SMOKE_DEV_PRIVILEGED_CONFIRM=1)"
	@echo "  smoke-memory            VM-required shared budget, OOM and builder recovery gate"
	@echo "  smoke-box-attach        VM-required lifecycle auto-attach smoke test (PTY)"
	@echo "  smoke-host-integration  VM-required image paste and browser request smoke (Linux fixtures)"
	@echo "  smoke-account-auth      VM-required end-to-end account-auth env-injection smoke test"
	@echo "  smoke-pi                Isolated Pi CLI, policy, tools and account smoke"
	@echo "  smoke-opencode          Isolated OpenCode CLI, policy and account smoke"
	@echo "  smoke-airc              Isolated AIRC images, server sessions and cross-box collaboration"
	@echo "  smoke-agy               Isolated Antigravity native CLI and account smoke"
	@echo "  smoke-gemini            Isolated Gemini image, policy and account smoke"
	@echo "  vet                     Run go vet"
	@echo "  vet-darwin              Cross-vet every package, tests included, for macOS"
	@echo "  lint                    Run golangci-lint"
	@echo "  docs-check              Validate KB structure, citations and local links"
	@echo "  check                   Run docs + vet + darwin cross-vet + test + lint (mirrors CI)"
	@echo "  clean                   Remove $(BIN_DIR)"

# Release bundles share one make invocation and therefore one identity.
DIST_DIR ?= dist
export VERSION
.PHONY: dist validate-dist-version
.NOTPARALLEL: dist
dist: validate-dist-version build-guests
	$(call build-host,GOOS=linux GOARCH=amd64,$(BIN_DIR)/yoloctl-host-linux-amd64)
	$(call build-host,GOOS=linux GOARCH=arm64,$(BIN_DIR)/yoloctl-host-linux-arm64)
	$(call build-host,GOOS=darwin GOARCH=amd64,$(BIN_DIR)/yoloctl-darwin-amd64)
	$(call build-host,GOOS=darwin GOARCH=arm64,$(BIN_DIR)/yoloctl-darwin-arm64)
	go run -buildvcs=false ./scripts/dist.go "$(BIN_DIR)" "$(DIST_DIR)" "$$VERSION" "$(BUNDLE_ID)"

validate-dist-version:
	go run -buildvcs=false ./scripts/dist.go validate "$$VERSION"
