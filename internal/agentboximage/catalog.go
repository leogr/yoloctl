// Package agentboximage owns the "ensure the agentbox base image exists in the
// space VM" flow. The yoloctl-managed base is built locally from a Dockerfile
// context embedded in the binary; feature composition derives each box's
// runtime image from that base.
//
// External callers use EnsureImage. The internal/agentbox Applier
// invokes it before `nerdctl run` so a fresh space (no images
// pre-built) just-works on the first `yoloctl box create`. The same code
// path keeps yoloctl usable in air-gapped environments — the binary
// carries every Dockerfile it needs.
//
// User-supplied refs are left alone: EnsureImage is a no-op for them, so
// nerdctl run's existing pull-or-fail behavior is preserved.
package agentboximage

// baseTag is the single source of truth for the suffix every
// yoloctl-managed image carries today. Bump in lockstep with the
// FROM-base-image upgrade in assets/containers/agentbox/Dockerfile.
//
// Note: the embedded agent-profile YAMLs (agy.yaml, claude.yaml, codex.yaml, gemini.yaml, opencode.yaml, pi.yaml)
// carry the literal "yoloctl/agentbox:ubuntu-24.04" string (the agent
// CLI is added during feature composition). Keep them in sync with this
// constant when bumping; the embedded-context and round-trip integration
// tests catch drift.
const baseTag = "ubuntu-24.04"

// RefAgentbox is the one yoloctl-managed catalog image: the plain base every
// box composes its features onto. Other packages that need "the default agentbox
// image" reference this constant rather than re-declaring the string, keeping
// the catalog lookup and the default-image flag default in agreement.
const RefAgentbox = "yoloctl/agentbox:" + baseTag

// agentboxContext is the embedded Docker build context for RefAgentbox.
const agentboxContext = "containers/agentbox"
