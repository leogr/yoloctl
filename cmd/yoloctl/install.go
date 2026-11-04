package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/installer"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/releasebundle"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	var bin string
	var dry, yes bool
	cmd := &cobra.Command{Use: "install", Short: "Install this verified release bundle", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if buildinfo.Channel() != "release" {
			return asUserError(errors.New("install requires a release build"))
		}
		if spaceenv.Native() || os.Getenv(spaceenv.ContextEnv) != "" {
			return asUserError(errors.New("install is unavailable inside a VM controller"))
		}
		if _, present := os.LookupEnv("OPENSHELL_SANDBOX"); present {
			return asUserError(errors.New("install is unavailable inside an agent box"))
		}
		op, err := loadOperatorClient()
		if err != nil {
			return err
		}
		if op != nil {
			return asUserError(errors.New("install is unavailable to a box operator"))
		}
		if reason := installContainerSignal(); reason != "" {
			return asUserError(fmt.Errorf("install is unavailable inside a container (%s)", reason))
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		self, err = filepath.EvalSymlinks(self)
		if err != nil {
			return err
		}
		source := filepath.Dir(self)
		manifest, err := releasebundle.Verify(source)
		if err != nil {
			return fmt.Errorf("release bundle verification: %w", err)
		}
		identity, err := controller.ExecutableIdentity()
		if err != nil {
			return err
		}
		sum, err := guestbinary.SHA256File(self)
		if err != nil {
			return err
		}
		if manifest.Version != buildinfo.Version || manifest.Bundle != identity || manifest.OS != runtime.GOOS || manifest.Arch != runtime.GOARCH || sum != manifest.Files["yoloctl"] || manifest.Files["yoloctl-linux-amd64"] != guestbinary.AMD64SHA256 || manifest.Files["yoloctl-linux-arm64"] != guestbinary.ARM64SHA256 {
			return errors.New("bundle does not match this running executable")
		}
		for _, arch := range []string{"amd64", "arm64"} {
			if _, err := guestbinary.Resolve(self, arch); err != nil {
				return err
			}
		}
		if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
			if _, err := guestbinary.ResolveInit(self); err != nil {
				return err
			}
		}
		if bin == "" {
			bin, err = layout.DefaultBinDir()
			if err != nil {
				return err
			}
		}
		if !filepath.IsAbs(bin) || filepath.Clean(bin) != bin {
			return asUserError(errors.New("--bin-dir must be absolute and clean"))
		}
		store, err := layout.InstallRoot()
		if err != nil {
			return err
		}
		if err := installer.ValidatePaths(source, store, bin); err != nil {
			return err
		}
		// One resolved spelling for inspection, publication and printed
		// commands, so a symlinked home is neither refused nor misclassified.
		if store, err = installer.CanonicalPath(store); err != nil {
			return err
		}
		if bin, err = installer.CanonicalPath(bin); err != nil {
			return err
		}
		existing, err := installer.Inspect(bin, store)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintln(out, installPrerequisite(cmd.Context()))
		_, _ = fmt.Fprintf(out, "Release %s (%s)\nInstall link: %s\n", manifest.Version, manifest.Bundle, filepath.Join(bin, "yoloctl"))
		if path, err := exec.LookPath("yoloctl"); err == nil {
			path, _ = filepath.Abs(path)
			// Compare resolved directories: a PATH entry may reach the bin
			// directory through a symlink (and vice versa).
			dir, link := filepath.Dir(path), bin
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			}
			if resolved, err := filepath.EvalSymlinks(link); err == nil {
				link = resolved
			}
			if filepath.Join(dir, filepath.Base(path)) != filepath.Join(link, "yoloctl") {
				_, _ = fmt.Fprintf(out, "Warning: another yoloctl is first on PATH: %s\n", path)
			}
		}
		if existing.Unknown {
			_, _ = fmt.Fprintf(out, "Existing %s: unknown build, SHA256 %s (never executed)\n", existing.Path, existing.Fingerprint)
		}
		if existing.Manifest != nil {
			_, _ = fmt.Fprintf(out, "Existing release: %s (%s)\n", existing.Manifest.Version, existing.Manifest.Bundle)
			if buildinfo.CompareRelease(manifest.Version, existing.Manifest.Version) < 0 {
				_, _ = fmt.Fprintln(out, "Warning: this is a downgrade; older builds may not understand newer state.")
			}
		}
		interactive := isInteractive(cmd.InOrStdin())
		printOnly, confirm, err := installDecision(existing, manifest, interactive, yes, dry)
		if err != nil {
			return err
		}
		proposals := installProposals(cmd.Context(), manifest.Bundle, filepath.Join(bin, "yoloctl"), !printOnly, out)
		if printOnly {
			_, _ = fmt.Fprintln(out, "Print only; nothing installed or migrated. Use --yes to accept an upgrade without migration execution.")
			return nil
		}
		if confirm {
			prompt := "Replace the unknown build with this release? [y/N]: "
			if !existing.Unknown {
				prompt = fmt.Sprintf("Upgrade %s → %s? [y/N]: ", existing.Manifest.Version, manifest.Version)
			}
			ok, err := confirmInstall(prompt, cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if !ok {
				_, _ = fmt.Fprintln(out, "Installation declined.")
				return nil
			}
		}
		result, err := installer.Publish(cmd.Context(), installer.Options{Source: source, Store: store, BinDir: bin, Expected: existing, AllowUnknown: existing.Unknown && confirm})
		if err != nil {
			return err
		}
		if result.Already {
			_, _ = fmt.Fprintln(out, "Already installed (verified bundle).")
		} else {
			_, _ = fmt.Fprintln(out, "Installed:", result.Link)
		}
		for _, path := range result.Retained {
			_, _ = fmt.Fprintln(out, "Retained bundle:", path)
		}
		home, _ := os.UserHomeDir()
		if hint := installPathHint(bin, os.Getenv("PATH"), os.Getenv("SHELL"), home); hint != "" {
			_, _ = fmt.Fprint(out, hint)
		}
		return executeInstallProposals(cmd, proposals, result.Executable, manifest.Bundle, yes)
	}}
	cmd.Long = "Install this verified release bundle as <bin-dir>/yoloctl (default ~/.local/bin).\n\n" +
		"When <bin-dir> is not on PATH, install prints the line that adds it for your shell\n" +
		"(bash, zsh or fish, from $SHELL), for example:\n" +
		"  echo 'export PATH=\"$HOME/.local/bin:$PATH\"' >> ~/.zshrc"
	cmd.Flags().StringVar(&bin, "bin-dir", "", "User binary directory (default ~/.local/bin)")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Verify and print without writing or executing migrations")
	cmd.Flags().BoolVar(&yes, "yes", false, "Accept a managed upgrade only; never resolve a conflict or execute migrations")
	return cmd
}

func installDecision(existing installer.Existing, desired releasebundle.Manifest, interactive, yes, dry bool) (printOnly, confirm bool, err error) {
	// --dry-run never writes, so it prints even an unknown-build conflict.
	if dry {
		return true, false, nil
	}
	if existing.Unknown && !interactive {
		return false, false, errors.New("unknown build conflict requires explicit terminal confirmation")
	}
	if !interactive && !yes {
		return true, false, nil
	}
	if existing.Unknown {
		return false, true, nil
	}
	same := existing.Manifest != nil && existing.Manifest.Key() == desired.Key()
	return false, existing.Manifest != nil && !same && !yes, nil
}

func shellWord(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// installPathHint is what install prints when bin is not a PATH entry: the
// line that adds it for the user's shell ($SHELL: bash, zsh or fish), or a
// generic instruction for any other shell. A bin under home is written as
// $HOME/...; only that inserted prefix may expand, so a path with a literal $
// or any character that would need shell quoting gets the generic
// instruction. Nothing is printed when bin is already on PATH.
func installPathHint(bin, pathEnv, shell, home string) string {
	resolve := func(dir string) string {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return resolved
		}
		return filepath.Clean(dir)
	}
	want := resolve(bin)
	for _, entry := range filepath.SplitList(pathEnv) {
		if entry != "" && filepath.IsAbs(entry) && resolve(entry) == want {
			return ""
		}
	}
	generic := fmt.Sprintf("%s is not on PATH; add it to PATH in your shell's startup file to run yoloctl from anywhere.\n", bin)
	for _, r := range bin {
		if !plainPathRune(r) {
			return generic
		}
	}
	display := bin
	if rest, ok := strings.CutPrefix(bin, home+string(filepath.Separator)); ok && home != "" {
		display = "$HOME/" + rest
	}
	var command string
	switch filepath.Base(shell) {
	case "zsh":
		command = fmt.Sprintf("echo 'export PATH=\"%s:$PATH\"' >> ~/.zshrc", display)
	case "bash":
		command = fmt.Sprintf("echo 'export PATH=\"%s:$PATH\"' >> ~/.bashrc", display)
	case "fish":
		command = fmt.Sprintf("fish_add_path \"%s\"", display)
	default:
		return generic
	}
	return fmt.Sprintf("%s is not on PATH; to run yoloctl from anywhere, run this once and open a new terminal:\n  %s\n", bin, command)
}

// plainPathRune reports a character that needs no shell quoting in the hint.
func plainPathRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("/._-+", r)
}
