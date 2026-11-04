// Package assets bundles the Lima template and guest build input
// files used by yoloctl; the Go binary embeds them via this package.
package assets

import "embed"

//go:embed lima/yoloctl.yaml.tmpl
var LimaTemplate string

// VMImageRecipe contains the upstream pins for native local image assembly.
// Python recipes are maintenance tools and are not shipped in the CLI.
//
//go:embed vm/firecracker/pins.json vm/firecracker/runtime-pins.json
var VMImageRecipe embed.FS

// BoxOperatorSkill is mounted read-only only into enabled operator boxes.
//
//go:embed operator/yolo-box-operator/SKILL.md
var BoxOperatorSkill string

// AgentboxContexts ships the Dockerfile build context(s) for yoloctl's
// agentbox image(s) so the binary can build them on demand inside a space VM
// when nerdctl reports one missing. Today this is just the base `agentbox`
// image; each box's runtime image is the base COMPOSED with its features (see
// internal/agentboximage.EnsureComposedImage), including the selected agent
// CLI feature. Subtree layout:
// containers/<image>/Dockerfile (+ any siblings).
//
//go:embed containers
var AgentboxContexts embed.FS

// SandboxVersion identifies the native runtime target. Executable/Rego bytes
// and their hashes come from the compiled qualification and embedded artifacts,
// not a VM-side upstream download or the retired /opt/openshell installation.
const SandboxVersion = "v0.1.6"
