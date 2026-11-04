// Package features defines the yoloctl feature set: installable components an
// agentbox image can carry, each declaring the capabilities it needs.
//
// A feature is an INSTALLER, not a policy object. Network/filesystem
// access lives in internal/capabilities; a feature names the capabilities it
// requires (RequiresCapabilities) and the composer inherits them. The old
// inline NetworkPolicy block is gone — its data moved into the capability
// registry.
//
// Features install from inputs carried directly in their definitions: a raw
// Dockerfile fragment or an npm package.
// The build itself still needs network (apt / npm / go.dev), like the base
// image build; once a composed image is cached, reuse is offline.
//
// WHERE INSTALL FRAGMENTS ARE CONSUMED: InstallPlan / GenerateDockerfile render
// a feature's install definition into a Dockerfile, driving TWO paths now that
// the on-demand builder is wired: (1) the feature-composed RUNTIME image that
// `box create` / `run` / `reconcile` build-or-select automatically
// (internal/agentboximage.EnsureComposedImage — image = base + resolved
// features), and (2) the manual `yoloctl image build --features … --tag …`
// path. So `--features` both grants a feature's required capabilities AND
// installs its tool into the box's image.
package features

import (
	"fmt"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/internal/operator"
)

// Feature is a single named installable component for an agentbox.
type Feature struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Install describes how the component is installed into the image.
	// Nil for policy-only features whose tool already lives in the base
	// image (e.g. github: git/gh are baked in).
	Install *Install `yaml:"install,omitempty"`
	// RequiresCapabilities lists the capability names this feature needs
	// (e.g. "network.anthropic"). The policy composer resolves these against
	// internal/capabilities and inherits them into the effective set.
	RequiresCapabilities []string `yaml:"requires_capabilities,omitempty"`
}

// InstallKind enumerates how a feature is installed.
const (
	// InstallDockerfile splices a raw Dockerfile fragment after FROM.
	InstallDockerfile = "dockerfile"
	// InstallNpm installs a global npm package (the agent-CLI install path).
	InstallNpm = "npm"
)

// Install is a feature's installer definition.
type Install struct {
	Kind string `yaml:"kind"`
	// Dockerfile is the raw fragment for kind=dockerfile.
	Dockerfile string `yaml:"dockerfile,omitempty"`
	// Package / Version drive kind=npm (npm install -g package@version).
	Package string `yaml:"package,omitempty"`
	Version string `yaml:"version,omitempty"`
}

// NPMGlobalPrefix is where agent image builds and runtime maintenance install
// global packages. Those paths always pass it explicitly. The optional npm
// development feature also configures it as the sandbox user's default.
const NPMGlobalPrefix = "/home/sandbox/.npm-global"

// registry maps feature names to definitions. Network grants live in
// internal/capabilities; features reference them via RequiresCapabilities.
//
// Catalog naming: lowercase kebab-case, with yolo.* for yoloctl integrations.
// Version-pinned variants MAY append
// `-<version>` (`python-3.12`), but the built-in `go` is deliberately
// UNVERSIONED: it installs latest stable Go and relies on GOTOOLCHAIN=auto to
// fetch whatever toolchain a repository's go.mod pins (the download rides the same
// network.go-modules grant — proxy.golang.org serves the golang.org/toolchain
// module). Policy-only features (`github`, `pypi`) carry no install —
// their tools live in the base image.
var registry = map[string]Feature{
	"yolo.chat.skill": {
		Name:        "yolo.chat.skill",
		Description: "AIRC agent JSONL client, server binary and collaboration skill; endpoint access requires explicit policy",
		Install:     &Install{Kind: InstallDockerfile, Dockerfile: aircInstallDockerfile()},
	},
	"yolo.agent.observe": {
		Name:        "yolo.agent.observe",
		Description: "Claude Code status line and async hooks reporting session, model, effort, context and version to the launch's observation directory, then running the user's own status line; agent-reported, inert without a controller launch generation; needs Python 3.8+ (installed with apt when the base lacks it)",
		Install:     &Install{Kind: InstallDockerfile, Dockerfile: agentObserveInstallDockerfile()},
	},
	"yolo.agent.observe-codex": {
		Name:        "yolo.agent.observe-codex",
		Description: "Codex System-layer hooks (SessionStart, Stop, SessionEnd, SubagentStart) reporting session and model to the launch's observation directory; agent-reported, inert without a controller launch generation; needs Python 3.8+ (installed with apt when the base lacks it)",
		Install:     &Install{Kind: InstallDockerfile, Dockerfile: agentObserveCodexInstallDockerfile()},
	},
	"yolo.agent.observe-agy": {
		Name:        "yolo.agent.observe-agy",
		Description: "Antigravity CLI status line launcher reporting model, effort, context and state to the launch's observation directory; enabled by the account's settings (seeded for new accounts, `yoloctl account observe` for existing ones); agent-reported, inert without a controller launch generation; needs Python 3.8+ (installed with apt when the base lacks it)",
		Install:     &Install{Kind: InstallDockerfile, Dockerfile: agentObserveAgyInstallDockerfile()},
	},
	operator.Capability: {
		Name:        operator.Capability,
		Description: "yoloctl space operator: full box authority in the current space, including other accounts and delegation",
		// Pre-create the user skill parents before the read-only skill bind.
		// The entrypoint only re-owns the home when the sandbox UID changes.
		Install: &Install{Kind: InstallDockerfile, Dockerfile: `RUN ln -sf /opt/yoloctl/helper/yoloctl /usr/local/bin/yoloctl
RUN install -d -o sandbox -g sandbox /home/sandbox/.agents /home/sandbox/.agents/skills`},
		RequiresCapabilities: []string{operator.Capability},
	},
	"go": {
		Name: "go",
		Description: "Latest stable Go at image build (official go.dev tarball at " +
			"/usr/local/go, symlinked at /usr/local/bin/go); GOTOOLCHAIN=local in its " +
			"go.env, so a go.mod that requires a newer Go fails with Go's own message " +
			"until the box is recreated",
		// Install latest stable Go from the official tarball — apt's golang-N is
		// pinned to the distro's version, which can't track "latest".
		// GOTOOLCHAIN=local keeps that Go: a downloaded toolchain would run from
		// a version-specific GOMODCACHE path that no policy can grant, so its
		// egress would be denied after the download. It is set in
		// $GOROOT/go.env, Go's installation default, not as an image ENV, so
		// every go process reads it whatever environment launched it; an
		// explicit GOTOOLCHAIN still overrides it. OpenShell attributes egress
		// to the resolved executable, so network.go-modules names
		// /usr/local/go/bin/go; the /usr/local/bin symlinks only serve PATH.
		Install: &Install{
			Kind: InstallDockerfile,
			Dockerfile: `RUN GO_VERSION="$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -n1)" \
    && GO_ARCH="$(dpkg --print-architecture)" \
    && curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o /tmp/go.tar.gz \
    && rm -rf /usr/local/go \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && ln -sf /usr/local/go/bin/go /usr/local/bin/go \
    && ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt \
    && sed -i 's/^GOTOOLCHAIN=.*/GOTOOLCHAIN=local/' /usr/local/go/go.env \
    && grep -qx 'GOTOOLCHAIN=local' /usr/local/go/go.env`,
		},
		RequiresCapabilities: []string{"network.go-modules"},
	},

	// Agent-install features. Installing the agent CLI is a feature (the
	// agent's requires_features names which one — there is no agent_feature
	// field). Built-ins install npm or verified native releases;
	// the network grant is inherited from the required
	// capability, not inline.
	"claude": {
		Name:                 "claude",
		Description:          "Claude Code CLI (Anthropic), installed from npm",
		Install:              &Install{Kind: InstallNpm, Package: "@anthropic-ai/claude-code", Version: "latest"},
		RequiresCapabilities: []string{"network.anthropic"},
	},
	"codex": {
		Name:                 "codex",
		Description:          "OpenAI Codex CLI, installed from npm with bubblewrap",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: codexInstallDockerfile},
		RequiresCapabilities: []string{"network.openai"},
	},
	"agy": {
		Name:                 "agy",
		Description:          "Antigravity CLI (Google), installed from verified native releases",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: agyInstallDockerfile()},
		RequiresCapabilities: []string{"network.agy"},
	},
	"gemini": {
		Name:                 "gemini",
		Description:          "Gemini CLI (Google), installed from npm with managed update and shell defaults",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: geminiInstallDockerfile},
		RequiresCapabilities: []string{"network.gemini"},
	},
	"pi": {
		Name:                 "pi",
		Description:          "Pi coding agent, installed from npm with local search tools",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: piInstallDockerfile},
		RequiresCapabilities: []string{"network.pi"},
	},

	"opencode": {
		Name:                 "opencode",
		Description:          "OpenCode native CLI, installed from npm with ripgrep",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: opencodeInstallDockerfile},
		RequiresCapabilities: []string{"network.opencode"},
	},

	// Ecosystem features: policy-only (tool in the base image), egress via
	// the required capability.
	"github": {
		Name:                 "github",
		Description:          "GitHub egress restricted to git + gh (tools in the base image)",
		RequiresCapabilities: []string{"network.github"},
	},
	"pypi": {
		Name:                 "pypi",
		Description:          "PyPI + pythonhosted egress (opt-in)",
		RequiresCapabilities: []string{"network.pypi"},
	},

	// The base supplies Node.js/npm; this feature configures its development
	// environment as well as granting registry access.
	"npm": {
		Name:                 "npm",
		Description:          "JavaScript environment: base-image Node.js, npm and npx, user-writable global installs/cache, and npm registry access",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: npmInstallDockerfile},
		RequiresCapabilities: []string{"network.npm"},
	},

	"pip": {
		Name:                 "pip",
		Description:          "Python environment: base-image Python 3.13, venv and pip, user-writable installs/cache, and PyPI access",
		Install:              &Install{Kind: InstallDockerfile, Dockerfile: pipInstallDockerfile},
		RequiresCapabilities: []string{"network.pypi"},
	},

	// Explicit distro toolchain. This does not replace the base's default
	// Python in /usr/local/bin; use pip for that development environment.
	"python-3.12": {
		Name:        "python-3.12",
		Description: "Ubuntu Python + pip via apt (3.12 on Ubuntu 24.04); leaves the base's default Python unchanged; pair with `pypi` for egress",
		Install: &Install{
			Kind:       InstallDockerfile,
			Dockerfile: aptInstallDockerfile("python3 python3-pip"),
		},
		// No capability by design — pair with `pypi` for egress.
	},
	"cargo": {
		Name:        "cargo",
		Description: "Rust toolchain (cargo + rustc) + crates.io egress, restricted to /usr/bin/cargo",
		Install: &Install{
			Kind:       InstallDockerfile,
			Dockerfile: aptInstallDockerfile("cargo rustc"),
		},
		RequiresCapabilities: []string{"network.cargo"},
	},
}

// IsBuiltin reports whether name is one of the embedded built-in features.
// Built-ins are yoloctl-owned and read-only; the catalog commands use this to
// reject editing/destroying a built-in and to reject a custom item that would
// shadow one (same-name overrides of built-ins are not allowed).
func IsBuiltin(name string) bool {
	_, ok := registry[name]
	return ok || name == PrivilegedRuntime
}

// BuiltinNames returns the sorted names of the embedded built-in features only
// (no user-defined items). Used by the catalog `ls` command to mark which items
// are built-in.
func BuiltinNames() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the Feature for name. A custom feature under
// ~/.config/yoloctl/features/ resolves first, then the embedded registry.
// Custom features may NOT shadow a built-in name (LoadUserFeatures errors on a
// collision), so the two namespaces are disjoint.
func Lookup(name string) (Feature, error) {
	if ocifeature.IsReference(name) {
		if _, err := ocifeature.Parse(name); err != nil {
			return Feature{}, err
		}
		return Feature{Name: name}, nil
	}
	if name == PrivilegedRuntime {
		return privilegedRuntime, nil
	}
	userFeatures, err := LoadUserFeatures()
	if err != nil {
		return Feature{}, fmt.Errorf("load user features: %w", err)
	}
	if f, ok := userFeatures[name]; ok {
		return f, nil
	}
	if f, ok := registry[name]; ok {
		return f, nil
	}
	if removedFeatures[name] {
		return Feature{}, fmt.Errorf("feature %q was removed with the Repartee client and its airc-box launcher: use the managed room (yoloctl chat) for chat, yolo.chat.skill for the AIRC tools, and edit this box's features and session_command", name)
	}
	return Feature{}, fmt.Errorf("unknown feature %q (known: %s)", name, strings.Join(Names(), ", "))
}

// removedFeatures are former built-ins that only get a clearer error. They
// are not aliases: a declaration naming one fails until it is edited.
var removedFeatures = map[string]bool{"yolo.chat.server": true, "yolo.chat.repartee": true}

// Names returns every known feature name (embedded + user), sorted.
func Names() []string {
	userFeatures, _ := LoadUserFeatures()
	seen := make(map[string]struct{}, len(registry)+len(userFeatures))
	out := make([]string, 0, len(registry)+len(userFeatures))
	for n := range userFeatures {
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for n := range registry {
		if _, dup := seen[n]; dup {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func validateOCIPlans(featureNames []string, plans []ocifeature.Plan) error {
	for _, name := range featureNames {
		if ocifeature.IsReference(name) {
			found := false
			for _, plan := range plans {
				for _, lock := range plan.Locks {
					if lock.Ref == name {
						found = true
					}
				}
			}
			if !found {
				return fmt.Errorf("OCI Feature %s requires a resolved installation plan", name)
			}
		}
	}
	return nil
}

// GenerateDockerfile renders a Dockerfile that extends base with the supplied
// features spliced in order. Each feature renders its Install: a raw
// Dockerfile fragment verbatim, or an npm global install. Policy-only features
// (no Install) contribute nothing to the image.
//
// Output is deterministic for a given input (feature order preserved).
func GenerateDockerfile(base string, featureNames []string, plans ...ocifeature.Plan) (string, error) {
	if base == "" {
		return "", fmt.Errorf("base image is required")
	}
	if err := validateOCIPlans(featureNames, plans); err != nil {
		return "", err
	}
	resolved := make([]Feature, 0, len(featureNames))
	for _, n := range featureNames {
		f, err := Lookup(n)
		if err != nil {
			return "", err
		}
		resolved = append(resolved, f)
	}

	var b strings.Builder
	b.WriteString("# Generated by yoloctl from a feature list.\n")
	b.WriteString("# DO NOT EDIT by hand — regenerate via `yoloctl image build --tag REF --features ...`.\n")
	fmt.Fprintf(&b, "FROM %s\n\n", base)
	b.WriteString("ENV DEBIAN_FRONTEND=noninteractive\n")
	if len(resolved) == 0 && len(plans) == 0 {
		return b.String(), nil
	}
	b.WriteByte('\n')
	for _, f := range resolved {
		frag, err := renderInstall(f)
		if err != nil {
			return "", err
		}
		if frag == "" {
			continue
		}
		fmt.Fprintf(&b, "# Feature: %s — %s\n", f.Name, f.Description)
		b.WriteString(frag)
		b.WriteString("\n\n")
	}
	for _, plan := range plans {
		fragment, err := plan.Dockerfile()
		if err != nil {
			return "", err
		}
		b.WriteString(fragment)
	}
	return b.String(), nil
}

// InstallPlan resolves featureNames and reports how they compose onto base:
// the generated Dockerfile, the subset of features that actually contribute an
// install step (a non-empty rendered fragment), and whether ANY do. Policy-only
// features (github, pypi) render nothing, so a set containing only them
// returns hasInstall=false — the caller then runs the base image as-is instead
// of building a pointless composed image. Unknown feature names error (no
// partial result), matching GenerateDockerfile.
//
// The on-demand feature-composed image builder keys a content-addressed image
// off the returned dockerfile and gates the build on hasInstall.
func InstallPlan(base string, featureNames []string, plans ...ocifeature.Plan) (dockerfile string, installing []string, hasInstall bool, err error) {
	if err := validateOCIPlans(featureNames, plans); err != nil {
		return "", nil, false, err
	}
	for _, n := range featureNames {
		f, err := Lookup(n)
		if err != nil {
			return "", nil, false, err
		}
		frag, err := renderInstall(f)
		if err != nil {
			return "", nil, false, err
		}
		if frag != "" {
			installing = append(installing, n)
		}
	}
	for _, plan := range plans {
		for _, lock := range plan.Locks {
			installing = append(installing, lock.Ref)
		}
	}
	// Nothing installs → no image to compose, so don't require a base. The
	// caller runs the base as-is (which may even be empty for a degenerate
	// manifest — that is the manifest's problem to surface, not ours).
	if len(installing) == 0 {
		return "", nil, false, nil
	}
	dockerfile, err = GenerateDockerfile(base, featureNames, plans...)
	if err != nil {
		return "", nil, false, err
	}
	return dockerfile, installing, true, nil
}

// renderInstall renders one feature's install step into a Dockerfile fragment,
// or "" when the feature installs nothing (policy-only).
func renderInstall(f Feature) (string, error) {
	if f.Install == nil {
		return "", nil
	}
	switch f.Install.Kind {
	case InstallDockerfile:
		return f.Install.Dockerfile, nil
	case InstallNpm:
		version := f.Install.Version
		if version == "" {
			version = "latest"
		}
		return npmPackageInstallDockerfile(f.Install.Package, version), nil
	default:
		return "", fmt.Errorf("feature %q has unknown install kind %q", f.Name, f.Install.Kind)
	}
}

// Cache downloads separately from image layers. Revalidate registry metadata
// so the cache cannot pin @latest. The image builder refreshes built-in recipes
// with a fresh ARG: --no-cache would also reset these cache mounts.
// Set ownership in the installation layer: a later recursive chown would copy
// the installed tree into another layer. Leave already-owned packages alone
// when composing more than one npm feature. A custom base can gain its sandbox
// identity from a later feature; the composed image's final repair handles it.
// Separate writable cache subdirectories also preserve manual feature builds
// whose base selects a non-root USER. This cache is never mounted at runtime.
func npmPackageInstallDockerfile(pkg, version string) string {
	return fmt.Sprintf(`RUN --mount=type=cache,id=yoloctl-npm-v1,target=/var/cache/yoloctl/npm,sharing=locked,mode=0777 \
    npm install -g %s@%s --prefix=%s --cache="/var/cache/yoloctl/npm/$(id -u)" --prefer-online \
    && if [ "$(id -u)" = 0 ] && id -u sandbox >/dev/null 2>&1 && getent group sandbox >/dev/null; then \
        find %s -xdev \( ! -user sandbox -o ! -group sandbox \) -exec chown -h sandbox:sandbox {} +; fi`, pkg, version, NPMGlobalPrefix, NPMGlobalPrefix)
}
