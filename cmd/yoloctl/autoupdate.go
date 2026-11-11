package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/logging"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// autoupdateAgentboxIfNeeded refreshes a configured npm or native distribution
// on create/start/run unless --no-autoupdate is set. Unavailable version probes
// skip with notice; an install failure is returned. Native maintenance verifies
// the release before replacing the binary; npm uses its explicit private prefix.
// Maintenance runs as sandbox in the outer container. It must never execute
// an agent workload there, including native --version or self-update commands.
// updated records a completed installation for post-launch failure diagnostics.
func autoupdateAgentboxIfNeeded(ctx context.Context, c *container.Client, agentboxName, agentName string, notice io.Writer, updated *bool) error {
	if !hasRealAgent(agentName) {
		// Generic-shell box (no --agent, or agent "none"). Nothing to
		// autoupdate; the user opted out of agent-profile machinery
		// by not picking one — and "none" must never be looked up as a
		// profile.
		return nil
	}
	profile, err := agentprofile.Lookup(agentName)
	if err != nil {
		cliNotice(logging.Box, "autoupdate skipped (load agent %q: %v)", agentName, err)
		return nil
	}
	if profile.Distribution() == "" {
		return nil
	}
	latest, err := preparedAgentLatest(ctx, profile)
	if err != nil {
		cliNotice(logging.Box, "autoupdate skipped (release source unreachable: %v)", err)
		return nil
	}
	id, err := c.ResolveContainerID(ctx, agentboxName)
	var installed string
	if err == nil {
		installed, err = fetchInstalledAgentVersion(ctx, c, id, profile)
	}
	if err != nil {
		cliNotice(logging.Box, "autoupdate skipped (read installed %s version: %v)", profile.Distribution(), err)
		return nil
	}
	if installed == latest.Latest {
		// Already at latest. Quiet — host claude doesn't announce
		// "already up to date" either.
		return nil
	}
	cliInfo(logging.Box, "autoupdate: %s %s → %s", profile.Distribution(), installed, latest.Latest)
	if err := installAgentLatest(ctx, c, id, profile); err != nil {
		return fmt.Errorf("autoupdate %s in %s: %w", profile.Distribution(), agentboxName, err)
	}
	if updated != nil {
		*updated = true
	}
	return nil
}

// sandboxNpmMaintenanceCache is the private cache used by yoloctl's runtime
// npm maintenance. Keep it below NPMGlobalPrefix: the base image creates that
// tree for sandbox, and npm feature installs preserve that ownership in their
// build layer. npm's default POSIX cache is ~/.npm, whose
// contents may have been created by another execution context and therefore
// are not part of this ownership contract.
const sandboxNpmMaintenanceCache = features.NPMGlobalPrefix + "/.cache/npm"

// npmInstallGlobalAtLatest invokes `npm install -g <pkg>@latest`
// inside agentboxName, run as the sandbox user with the explicit shared prefix
// and a yoloctl-owned cache beneath it. --prefer-online makes npm revalidate
// registry metadata after the host-side latest-version check detected drift.
// Shared between
// autoupdate (post-create / post-start refresh) and `yoloctl agent
// update PROFILE` (in-place refresh) so the two paths can never
// drift on their npm invocation semantics.
func npmInstallGlobalAtLatest(ctx context.Context, c *container.Client, agentboxName, pkg string) error {
	id, err := c.ResolveContainerID(ctx, agentboxName)
	if err != nil {
		return err
	}
	return c.Shell(ctx, npmInstallGlobalAtLatestArgs(id, pkg)...)
}

// npmInstallGlobalAtLatestArgs is kept pure so the ownership-sensitive npm
// invocation has a focused contract test without requiring a live Lima VM.
func npmInstallGlobalAtLatestArgs(containerID, pkg string) []string {
	return []string{
		"sudo", "nerdctl", "exec",
		"--user", "sandbox",
		containerID,
		"npm", "install", "-g",
		"--prefix=" + features.NPMGlobalPrefix,
		"--cache=" + sandboxNpmMaintenanceCache,
		"--prefer-online",
		pkg + "@latest",
	}
}

// fetchInstalledNpmVersion returns the version of pkg installed
// under the shared npm prefix in agentboxName, read directly from the
// package's package.json. More robust than `npm list -g` because
// it doesn't depend on npm's runtime config being aware of the sandbox-user
// prefix (the optional npm feature supplies a default; maintenance always
// passes --prefix explicitly).
//
// Returns ("", err) when the package isn't installed (file missing)
// or the JSON is malformed. Callers treat any error as "skip the
// autoupdate, can't tell if a refresh is needed."
func fetchInstalledNpmVersion(ctx context.Context, c *container.Client, agentboxName, pkg string) (string, error) {
	pkgJSONPath := fmt.Sprintf("%s/lib/node_modules/%s/package.json", features.NPMGlobalPrefix, pkg)
	return fetchInstalledVersionFile(ctx, c, agentboxName, pkgJSONPath)
}

// Read metadata only: never run an agent outside OpenShell to probe its version.
func fetchInstalledVersionFile(ctx context.Context, c *container.Client, agentboxName, pkgJSONPath string) (string, error) {
	id, err := c.ResolveContainerID(ctx, agentboxName)
	if err != nil {
		return "", err
	}
	cmd := vmhost.ShellCommand(ctx, c.Env(),
		"sudo", "nerdctl", "exec",
		"--user", "sandbox", id,
		"cat", pkgJSONPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("read %s: %w (%s)", pkgJSONPath, err, strings.TrimSpace(vmhost.FilterStderrText(stderr.String())))
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return "", fmt.Errorf("parse %s: %w", pkgJSONPath, err)
	}
	if parsed.Version == "" {
		return "", fmt.Errorf("%s has no `version` field", pkgJSONPath)
	}
	return parsed.Version, nil
}
