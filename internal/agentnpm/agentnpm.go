// Package agentnpm names the managed install of a box agent's npm program.
//
// The npm package an agent profile names (npm_package) is installed into
// Prefix, owned by User, and only yoloctl updates it, as User. The box user can
// read and run it but not change it, so no folder npm searches while running a
// package's install script during an update is writable by the agent. Every
// other npm install (the npm feature, npm tools added as features, manual
// installs) keeps the box user's own prefix.
package agentnpm

import (
	"fmt"
	"regexp"
)

const (
	// Prefix is the npm global prefix of the agent's program.
	Prefix = "/opt/yoloctl/npm"
	// Bin is the folder placed first on the image PATH.
	Bin = Prefix + "/bin"
	// User owns Prefix and runs every install into it.
	User = "yoloctl-maint"
	// Home is User's private home: start folder, temporary files and npm
	// cache of the runtime update. Mode 0700.
	Home = "/var/lib/yoloctl-maint"
	// Cache is the npm cache of the runtime update.
	Cache = Home + "/.npm"
	// Shell keeps User from logging in.
	Shell = "/usr/sbin/nologin"
)

// PackageJSON is the installed package's manifest under Prefix, the file the
// installed version is read from.
func PackageJSON(pkg string) string {
	return Prefix + "/lib/node_modules/" + pkg + "/package.json"
}

// packageName is an npm registry package name: lowercase, optionally scoped,
// without characters a shell or Dockerfile could interpret.
var packageName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// ValidatePackage rejects a string that is not an npm registry package name,
// so it stays one data word wherever a recipe or argv carries it.
func ValidatePackage(pkg string) error {
	if len(pkg) > 214 || !packageName.MatchString(pkg) {
		return fmt.Errorf("invalid npm package name %q: use a lowercase registry name such as my-cli or @scope/my-cli", pkg)
	}
	return nil
}
