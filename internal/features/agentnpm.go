package features

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/leogr/yoloctl/internal/agentnpm"
)

// agentNpmRuntimePrefix names the installer that the composer appends for the
// npm package a box's agent profile names, like PrivilegedRuntime. The package
// is path-escaped so the name never contains "/" (an OCI Feature reference).
const agentNpmRuntimePrefix = "yoloctl:agent-npm:"

// installAgentNpm is the internal install kind of that installer. User
// features cannot select it (their kinds are validated in user.go).
const installAgentNpm = "agent-npm"

// AgentNpmRuntime returns the runtime feature name for the agent's npm package.
// Only the composer's runtime feature list carries it (agentbox.RuntimeFeatures,
// from the box's agent profile); public feature lookups do not know it.
func AgentNpmRuntime(pkg string) string {
	return agentNpmRuntimePrefix + url.PathEscape(pkg)
}

// LookupRuntime resolves a name of the composer's runtime feature list: the
// agent's managed npm installer, or any name Lookup knows.
func LookupRuntime(name string) (Feature, error) {
	if f, ok, err := agentNpmRuntimeFeature(name); ok {
		return f, err
	}
	return Lookup(name)
}

func agentNpmRuntimeFeature(name string) (Feature, bool, error) {
	escaped, ok := strings.CutPrefix(name, agentNpmRuntimePrefix)
	if !ok {
		return Feature{}, false, nil
	}
	pkg, err := url.PathUnescape(escaped)
	if err == nil {
		err = agentnpm.ValidatePackage(pkg)
	}
	if err != nil || AgentNpmRuntime(pkg) != name {
		return Feature{}, true, fmt.Errorf("invalid agent npm runtime feature %q", name)
	}
	return Feature{
		Name:        name,
		Description: "the agent's npm program, owned and updated by " + agentnpm.User,
		Install:     &Install{Kind: installAgentNpm, Package: pkg},
	}, true, nil
}

// npmInstaller renders the npm installs of one image. The agent's own package
// goes to agentnpm.Prefix once, at the first feature that installs it (later
// build steps may run it), or where the agent runtime feature stands when no
// feature does. Every other package, and every package of an image without an
// npm agent, keeps the box user's prefix exactly as before.
type npmInstaller struct {
	agent     string
	installed bool
}

func (n *npmInstaller) install(pkg, version string) string {
	if n == nil || n.agent == "" || pkg != n.agent {
		return npmPackageInstallDockerfile(pkg, version)
	}
	if n.installed {
		return "" // already installed for the agent earlier in this image
	}
	n.installed = true
	return agentNpmInstallDockerfile(pkg)
}

// prefix is where pkg is installed in this image.
func (n *npmInstaller) prefix(pkg string) string {
	if n != nil && n.agent != "" && pkg == n.agent {
		return agentnpm.Prefix
	}
	return NPMGlobalPrefix
}

// recipe is a built-in Dockerfile installer whose npm step depends on the
// image's agent. Dockerfile keeps the rendering without an npm agent, which is
// what catalog views show.
func recipe(render func(*npmInstaller) string) *Install {
	return &Install{Kind: InstallDockerfile, Dockerfile: render(nil), render: render}
}

// agentNpmInstallDockerfile installs pkg@latest as agentnpm.User into
// agentnpm.Prefix. The user is created after the composed identity step with
// free system numbers; an existing account must have exactly that shape.
// Installing as that user keeps every file it creates owned by it, so the box
// user can read and run the program but not change it. The build cache mount
// is shared with the other npm installs, one subfolder per UID.
func agentNpmInstallDockerfile(pkg string) string {
	const (
		user, home, prefix, shell = agentnpm.User, agentnpm.Home, agentnpm.Prefix, agentnpm.Shell
	)
	return fmt.Sprintf(`RUN set -e; \
    if id -u %[1]s >/dev/null 2>&1; then \
        [ "$(getent passwd %[1]s | cut -d: -f6-)" = "%[2]s:%[4]s" ] \
            && [ "$(id -gn %[1]s)" = %[1]s ] && [ "$(id -G %[1]s)" = "$(id -g %[1]s)" ] \
            || { echo "yoloctl: existing user %[1]s does not match the agent maintenance user" >&2; exit 1; }; \
    else \
        groupadd --system %[1]s; \
        useradd --system --gid %[1]s --no-create-home --home-dir %[2]s --shell %[4]s %[1]s; \
    fi; \
    maint="$(id -u %[1]s)"; \
    if [ "$maint" = 0 ] || { id -u sandbox >/dev/null 2>&1 && [ "$maint" = "$(id -u sandbox)" ]; }; then \
        echo "yoloctl: the agent maintenance user must differ from root and sandbox" >&2; exit 1; \
    fi; \
    install -d -m 0700 -o %[1]s -g %[1]s %[2]s; \
    install -d -m 0755 -o root -g root /opt/yoloctl; \
    install -d -m 0755 -o %[1]s -g %[1]s %[3]s
USER %[1]s
RUN --mount=type=cache,id=yoloctl-npm-v1,target=/var/cache/yoloctl/npm,sharing=locked,mode=0777 \
    cd %[2]s && HOME=%[2]s TMPDIR=%[2]s npm install -g --prefix=%[3]s --cache="/var/cache/yoloctl/npm/$(id -u)" --prefer-online -- %[5]s
USER root
ENV PATH=%[6]s:$PATH`, user, home, prefix, shell, agentnpm.ShellQuote(pkg+"@latest"), agentnpm.Bin)
}

// agentNpmFinalPath keeps the agent's folder first on PATH after every
// feature, including an npm feature that prepends the user's prefix later.
func agentNpmFinalPath() string {
	return "ENV PATH=" + agentnpm.Bin + ":$PATH\n"
}
