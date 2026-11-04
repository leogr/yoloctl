package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/strictyaml"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var defaultInstallImage = installDefaultImageReadOnly

// limaOpenShellCheck reads only the instance configuration (works while the
// VM is stopped, never boots it); tests replace it.
var limaOpenShellCheck = lima.CheckOpenShellVM

// installProposals prints each space's commands, which run the install link.
// When nothing will be installed now (print-only or --dry-run), it says so: the
// link still selects the old build until the install itself runs.
func installProposals(ctx context.Context, target, link string, installing bool, out io.Writer) []installmigration.Proposal {
	noted := false
	root, err := layout.SpacesDir()
	if err != nil {
		_, _ = fmt.Fprintln(out, "UNKNOWN spaces:", err)
		return nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		_, _ = fmt.Fprintln(out, "UNKNOWN spaces:", err)
		return nil
	}
	var proposals []installmigration.Proposal
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if err := layout.ValidateName("space", name); err != nil || !entry.IsDir() {
			_, _ = fmt.Fprintf(out, "UNKNOWN space entry %q: not a readable space directory\n", name)
			continue
		}
		boot, err := spaceconfig.LoadBootstrap(name)
		if err != nil {
			_, _ = fmt.Fprintf(out, "UNKNOWN %s: %v\n", name, err)
			continue
		}
		if boot.ImageUpdate != nil {
			_, _ = fmt.Fprintln(out, pendingImageUpdate(boot))
			continue
		}
		snapshot, err := observeInstallConsent(ctx, name)
		if err != nil {
			_, _ = fmt.Fprintf(out, "UNKNOWN %s: %v\n", name, err)
			continue
		}
		coherent, err := consentSnapshot(boot, snapshot.State)
		if err != nil || coherent != snapshot {
			_, _ = fmt.Fprintf(out, "UNKNOWN %s: bootstrap changed during inventory\n", name)
			continue
		}
		var optional *installmigration.Command
		note := ""
		tooOld := false
		if boot.VM.Backend == "firecracker" {
			expected, err := defaultInstallImage()
			note = installImageNote(boot.VM.Image, expected, err)
		} else if err := limaOpenShellCheck(boot.Name); errors.Is(err, lima.ErrOpenShellVMTooOld) {
			// A controller update is known to fail on this VM (and would boot a
			// stopped one first): propose the recreation that fixes it instead.
			tooOld = true
		} else if err != nil {
			_, _ = fmt.Fprintf(out, "UNKNOWN %s: Lima OpenShell provisioning unreadable: %v\n", name, err)
			continue
		} else {
			optional, note = installLimaAdvice(boot.Name)
		}
		p := proposeInstallMigration(snapshot, target, optional, note)
		if tooOld {
			p.Commands = []installmigration.Command{{Args: []string{"space", "recreate", name}, Recreate: true,
				Warning: "This VM predates this build's OpenShell provisioning, so a controller update would fail. Recreating it deploys this build's controller and BOOTS THE VM; sessions and VM-local workdirs are lost. Accounts and declarations survive."}}
		}
		if p.Note != "" {
			_, _ = fmt.Fprintf(out, "%s: %s\n", name, p.Note)
		}
		if len(p.Commands) > 0 && !installing && !noted {
			noted = true
			_, _ = fmt.Fprintf(out, "Not installed yet: install this release first. The commands below run %s, which still selects the old build until then.\n", link)
		}
		for _, command := range p.Commands {
			prefix := ""
			if home := os.Getenv("YOLOCTL_HOME"); home != "" {
				prefix = "YOLOCTL_HOME=" + shellWord(home) + " "
			}
			words := []string{shellWord(link)}
			for _, arg := range command.Args {
				words = append(words, shellWord(arg))
			}
			_, _ = fmt.Fprintf(out, "%s: %s\n  %s%s\n", name, command.Warning, prefix, strings.Join(words, " "))
		}
		if len(p.Commands) > 0 {
			proposals = append(proposals, p)
		}
	}
	return proposals
}

func proposeInstallMigration(snapshot installmigration.Snapshot, target string, optional *installmigration.Command, note string) installmigration.Proposal {
	p := installmigration.Proposal{Snapshot: snapshot, Note: note}
	if snapshot.Bundle != target {
		warning := "Required controller update for this running VM; boot is forbidden if it stops."
		if snapshot.State == "Stopped" {
			warning = "Required controller update; BOOTS THE VM."
		}
		p.Commands = append(p.Commands, installmigration.Command{Args: []string{"space", "start", snapshot.Space, "--update-controller"}, Warning: warning})
	}
	if optional != nil {
		p.Commands = append(p.Commands, *optional)
	}
	return p
}

func installImageNote(recorded, expected string, err error) string {
	if err == nil && recorded == expected {
		return ""
	}
	target := expected
	if target == "" {
		target = "not prepared"
	}
	note := fmt.Sprintf("UNKNOWN: image origin cannot be proved (recorded %s; this build's default %s). See https://github.com/leogr/yoloctl/blob/"+buildinfo.Version+"/docs/digests/installation.md for the manual --update-vm-image decision.", recorded, target)
	if err != nil {
		note += " " + err.Error()
	}
	return note
}

// With no inheritance or global overrides, absent archives prove that Lima
// selects its built-in unpinned stack. Inherited/global configuration is unknown.
func limaArchives(raw []byte) ([]any, error) {
	var node map[string]any
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return nil, err
	}
	if base, ok := node["base"]; ok && base != nil {
		return nil, errors.New("inherited Lima configuration makes archive identity unavailable")
	}
	rawContainerd := node["containerd"]
	if rawContainerd == nil {
		return nil, nil
	}
	containerd, ok := rawContainerd.(map[string]any)
	if !ok {
		return nil, errors.New("invalid Lima containerd configuration")
	}
	rawArchives := containerd["archives"]
	if rawArchives == nil {
		return nil, nil
	}
	archives, ok := rawArchives.([]any)
	if !ok {
		return nil, errors.New("invalid Lima archives configuration")
	}
	return archives, nil
}
func installLimaAdvice(space string) (*installmigration.Command, string) {
	desired, err := limaArchives(vmhost.TemplateWithAccountDisk(nil, "", false))
	if err != nil {
		return nil, "UNKNOWN Lima pins: " + err.Error()
	}
	if len(desired) == 0 {
		return nil, ""
	}
	home, err := layout.LimaHome()
	if err != nil {
		return nil, "UNKNOWN Lima home: " + err.Error()
	}
	for _, name := range []string{"default.yaml", "override.yaml"} {
		raw, err := safepath.ReadFile(home, filepath.Join("_config", name), 1<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "UNKNOWN Lima global configuration: " + err.Error()
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			return nil, "UNKNOWN effective Lima archives: global configuration is present"
		}
	}
	dir, err := vmhost.InstanceDir(space)
	if err != nil {
		return nil, "UNKNOWN Lima configuration: " + err.Error()
	}
	raw, err := safepath.ReadFile(dir, "lima.yaml", 1<<20)
	if err != nil {
		return nil, "UNKNOWN Lima configuration: " + err.Error()
	}
	effective, err := limaArchives(raw)
	if err != nil {
		return nil, "UNKNOWN effective Lima archives: " + err.Error()
	}
	if reflect.DeepEqual(desired, effective) {
		return nil, ""
	}
	warning := "Optional Lima stack replacement, after the controller update: BOOTS THE VM; sessions and VM-local workdirs are lost. Accounts and declarations survive."
	if len(effective) == 0 {
		warning = "Unpinned: the stack follows the host's Lima default. " + warning
	}
	return &installmigration.Command{Args: []string{"space", "recreate", space}, Recreate: true, Warning: warning}, ""
}

func executeInstallProposals(cmd *cobra.Command, proposals []installmigration.Proposal, executable, target string, yes bool) error {
	return installmigration.Execute(cmd.Context(), proposals, installmigration.Execution{
		Interactive: isInteractive(cmd.InOrStdin()), Yes: yes, TargetBundle: target, Out: cmd.OutOrStdout(),
		Confirm: func(prompt string) (bool, error) { return confirmInstall(prompt, cmd.InOrStdin(), cmd.ErrOrStderr()) },
		Observe: observeInstallConsent,
		Run: func(ctx context.Context, s installmigration.Snapshot, c installmigration.Command) (installmigration.Receipt, error) {
			return runInstallMigration(ctx, cmd, executable, s, c)
		},
	})
}

// installMigrationWaitDelay bounds a child that ignores the interrupt.
var installMigrationWaitDelay = 2 * time.Minute

func runInstallMigration(ctx context.Context, parent *cobra.Command, executable string, snapshot installmigration.Snapshot, command installmigration.Command) (installmigration.Receipt, error) {
	var receipt installmigration.Receipt
	read, write, err := os.Pipe()
	if err != nil {
		return receipt, err
	}
	defer func() { _ = read.Close() }()
	args := append(append([]string(nil), command.Args...), "--"+installPreconditionFlag, snapshot.Encode(), "--"+installReceiptFlag, "3")
	if command.Recreate {
		// Consent was given at the installer prompt and is re-checked against
		// the snapshot under the lifecycle lock: no second recreate prompt.
		args = append(args, "--force")
	}
	child := exec.CommandContext(ctx, executable, args...)
	// On interruption the child cancels cooperatively, like the old path's
	// own signal context, instead of being killed mid start/deploy.
	child.Cancel = func() error { return child.Process.Signal(os.Interrupt) }
	child.WaitDelay = installMigrationWaitDelay
	child.Env = os.Environ()
	child.Stdin = parent.InOrStdin()
	child.Stdout = parent.OutOrStdout()
	child.Stderr = parent.ErrOrStderr()
	child.ExtraFiles = []*os.File{write}
	if err := child.Start(); err != nil {
		_ = write.Close()
		return receipt, err
	}
	_ = write.Close()
	result := make(chan []byte, 1)
	go func() { raw, _ := io.ReadAll(io.LimitReader(read, 16385)); result <- raw }()
	runErr := child.Wait()
	raw := <-result
	if runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return receipt, errors.Join(ctxErr, runErr) // interrupted, never "skipped"
		}
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && exit.ExitCode() == installmigration.SkipStatus {
			return receipt, installmigration.ErrChanged
		}
		return receipt, runErr
	}
	if len(raw) > 16384 || !json.Valid(raw) {
		return receipt, errors.New("migration succeeded without a bounded private consent receipt")
	}
	if err := strictyaml.Decode(raw, &receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// Read precisely one answer without consuming input meant for a later lifecycle
// prompt or the inherited terminal of a child command.
func confirmInstall(prompt string, in io.Reader, out io.Writer) (bool, error) {
	_, _ = fmt.Fprint(out, prompt)
	var answer strings.Builder
	one := []byte{0}
	for {
		n, err := in.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			if answer.Len() < 128 {
				answer.WriteByte(one[0])
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
	}
	value := strings.ToLower(strings.TrimSpace(answer.String()))
	return value == "y" || value == "yes", nil
}
