package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leogr/yoloctl/internal/agentnpm"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/logging"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// autoupdateAgentboxIfNeeded refreshes a configured npm or native distribution
// on create/start/run unless --no-autoupdate is set. Unavailable version probes
// skip with notice; an install failure is returned. Native maintenance verifies
// the release before replacing the binary, as sandbox; npm installs run as the
// agent maintenance user into the agent's managed prefix, so the package's
// install scripts meet nothing the box user can write. Maintenance runs in the
// outer container. yoloctl must never execute an agent workload there,
// including native --version or self-update commands.
// updated records a completed installation for post-launch failure diagnostics.
func autoupdateAgentboxIfNeeded(ctx context.Context, c *container.Client, agentboxName, agentName string, updated *bool) error {
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
	return updateAgentInRun(ctx, c, agentboxName, profile, latest.Latest, updated)
}

// updateAgentInRun reads the installed version and installs the latest into
// the run this operation started or read: a stopped box or a replacement
// answering on the same name ends the operation, before the version read
// and before the install. A box the operation has no expectation for is
// resolved by name, as before; an unreadable version still skips.
func updateAgentInRun(ctx context.Context, c *container.Client, agentboxName string, profile *agentprofile.Profile, latest string, updated *bool) error {
	var stopped *container.StoppedDuringOperation
	id := ""
	expected, err := c.CheckExpectedRun(ctx, agentboxName, "the agent update")
	switch {
	case errors.As(err, &stopped):
		return err
	case expected != nil:
		id = expected.ID
	case err == nil:
		id, err = c.ResolveContainerID(ctx, agentboxName)
	}
	var installed string
	if err == nil {
		installed, err = fetchInstalledAgentVersion(ctx, c, id, profile)
	}
	if err != nil {
		cliNotice(logging.Box, "autoupdate skipped (read installed %s version: %v)", profile.Distribution(), err)
		return nil
	}
	if installed == latest {
		// Already at latest. Quiet — host claude doesn't announce
		// "already up to date" either.
		return nil
	}
	cliInfo(logging.Box, "autoupdate: %s %s → %s", profile.Distribution(), installed, latest)
	if _, err := c.CheckExpectedRun(ctx, agentboxName, "the agent install"); errors.As(err, &stopped) {
		return err
	} else if err != nil {
		cliNotice(logging.Box, "autoupdate skipped (read the box before the install: %v)", err)
		return nil
	}
	if err := installAgentLatest(ctx, c, id, agentboxName, profile); err != nil {
		return fmt.Errorf("autoupdate %s in %s: %w", profile.Distribution(), agentboxName, err)
	}
	if updated != nil {
		*updated = true
	}
	return nil
}

// Agent maintenance runs outside the inner sandbox, with tools resolved
// through the fixed system search path. Metadata reads, process probes and the
// native installer run as sandbox; npm installs use npmInstallGlobalAtLatestArgs.
func agentMaintenanceExecArgs(containerID string, args ...string) []string {
	return append([]string{
		"sudo", "nerdctl", "exec",
		"--user", "sandbox",
		"--env", "PATH=" + container.SystemExecPath,
		containerID,
	}, args...)
}

// npmInstallGlobalAtLatest invokes `npm install -g <pkg>@latest` inside
// agentboxName as the agent maintenance user, into the agent's managed prefix
// (internal/agentnpm), with that user's private home as start folder,
// temporary folder, HOME and cache. Nothing npm searches, starts from or writes
// to is writable by the box user. --prefer-online revalidates registry metadata
// after the host-side version check detects drift. Both autoupdate and
// `yoloctl agent update PROFILE` use this installer, which also repairs an
// install that was cut off.
// target is the selected container (name or exact ID) every exec uses; box
// is the box name the recreate remedy names.
func npmInstallGlobalAtLatest(ctx context.Context, c *container.Client, target, box, pkg string) error {
	id, err := c.ResolveContainerID(ctx, target)
	if err != nil {
		return err
	}
	if err := c.Shell(ctx, npmInstallGlobalAtLatestArgs(id, pkg)...); err != nil {
		if predatesAgentNpm(ctx, c, id) {
			return fmt.Errorf("this box predates the managed agent folder %s, so its agent cannot be updated in place; recreate it: yoloctl box recreate %s --space %s", agentnpm.Prefix, box, c.Env())
		}
		return fmt.Errorf("npm install using the system PATH (%s): %w. If npm is unavailable on that PATH, install npm and Node.js in a system directory or pass --no-autoupdate to skip automatic updates", container.SystemExecPath, err)
	}
	return nil
}

// predatesAgentNpm reports, after a failed install, whether the container
// was built before the managed agent npm folder existed: its image has no
// /opt/yoloctl/npm (and no maintenance user). Only a successful
// `test ! -d` proves that; any probe failure keeps the install's own error.
func predatesAgentNpm(ctx context.Context, c *container.Client, id string) bool {
	return vmhost.ShellCommand(ctx, c.Env(), agentMaintenanceExecArgs(id, "test", "!", "-d", agentnpm.Prefix)...).Run() == nil
}

func npmInstallGlobalAtLatestArgs(containerID, pkg string) []string {
	// Skip both npmrc files the box user could write. npm rejects loading the
	// same path twice, so use a separate absent file under the container's /dev.
	return append([]string{
		"sudo", "nerdctl", "exec",
		"--user", agentnpm.User,
		"--workdir", agentnpm.Home,
		"--env", "PATH=" + container.SystemExecPath,
		"--env", "HOME=" + agentnpm.Home,
		"--env", "TMPDIR=" + agentnpm.Home,
		containerID,
	},
		"npm", "install", "-g",
		"--userconfig=/dev/null",
		"--globalconfig=/dev/yoloctl-maintenance.npmrc",
		"--prefix="+agentnpm.Prefix,
		"--cache="+agentnpm.Cache,
		"--prefer-online",
		// A published name may start with "-"; "--" keeps it the package.
		"--",
		pkg+"@latest",
	)
}

// fetchInstalledNpmVersion returns the version of the agent's pkg installed in
// the managed prefix in agentboxName, read directly from the package's
// package.json. A copy in the box user's prefix never counts.
//
// Returns ("", err) when the package isn't installed (file missing)
// or the JSON is malformed. Callers treat any error as "skip the
// autoupdate, can't tell if a refresh is needed."
func fetchInstalledNpmVersion(ctx context.Context, c *container.Client, agentboxName, pkg string) (string, error) {
	return fetchInstalledVersionFile(ctx, c, agentboxName, agentnpm.PackageJSON(pkg))
}

// Read metadata only: never run an agent outside OpenShell to probe its version.
func fetchInstalledVersionFile(ctx context.Context, c *container.Client, agentboxName, pkgJSONPath string) (string, error) {
	id, err := c.ResolveContainerID(ctx, agentboxName)
	if err != nil {
		return "", err
	}
	cmd := vmhost.ShellCommand(ctx, c.Env(), agentMaintenanceExecArgs(id, "cat", pkgJSONPath)...)
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
