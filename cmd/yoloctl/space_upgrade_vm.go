package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/spf13/cobra"
)

// spaceVMReplacement is a VM recreation that this build can prove the space
// needs to match it, with the recreate command that brings it there.
type spaceVMReplacement struct {
	reason string
	args   []string // yoloctl arguments
}

func (r spaceVMReplacement) command() string { return "yoloctl " + strings.Join(r.args, " ") }

// spaceUpgradeVMResult is what an unapplied VM replacement leaves for the
// rest of the upgrade to report: an explicit decline, or a question that had no
// answer.
type spaceUpgradeVMResult struct {
	declined, outstanding []string
}

// readSpaceVMReplacement compares the space's VM with this build using only the
// existing read-only selections that install advice also uses: a Firecracker
// space's recorded image against this build's published default image, and a
// Lima space's effective container-stack archives against this build's
// template pins. It never boots, prepares, downloads or changes anything. note
// names what could not be compared; on Lima, template differences beyond the
// OpenShell provisioning (checked separately as VM compatibility) and the
// container-stack pins are not detectable.
func readSpaceVMReplacement(boot *spaceconfig.Bootstrap) (*spaceVMReplacement, string) {
	if boot.VM.Backend == "firecracker" {
		state, err := firecrackerImageState(boot)
		switch {
		case err != nil:
			return nil, "could not compare this space's VM image with this build's: " + err.Error()
		case state == vmImageCurrent:
			return nil, ""
		}
		reason := "this space's VM image is not this build's default VM image"
		if state == vmImageNotPrepared {
			reason = "this build's default VM image has not been prepared on this host, so this space does not use it"
		}
		return &spaceVMReplacement{reason: reason, args: []string{"space", "recreate", boot.Name, "--update-vm-image"}}, ""
	}
	const undetectable = "Lima VM template changes other than its OpenShell provisioning and container-stack pins are not detectable"
	state, problem := readLimaStackState(boot.Name)
	switch {
	case problem != "":
		return nil, problem + "; " + strings.ToLower(undetectable[:1]) + undetectable[1:]
	case state == limaStackDiffers:
		return &spaceVMReplacement{reason: "this space's Lima container stack is pinned to other versions than this build's", args: []string{"space", "recreate", boot.Name}}, ""
	case state == limaStackUnpinnedSpace:
		return &spaceVMReplacement{reason: "this space's Lima container stack is unpinned and follows the host's Lima default, while this build pins it", args: []string{"space", "recreate", boot.Name}}, ""
	}
	return nil, undetectable
}

// spaceUpgradeVMInteractive reports whether a person can answer at the
// terminal: the answer is read from stdin and the question shown on stderr.
var spaceUpgradeVMInteractive = func(cmd *cobra.Command) bool {
	return stdinIsTerminalFn(cmd.InOrStdin()) && writerIsTerminal(cmd.ErrOrStderr())
}

// offerSpaceUpgradeVMRecreate runs before the controller update. When this build
// can prove the space's VM does not match it, it shows why and asks once,
// default No. A yes runs the ordinary `space recreate --force` with this
// build's image selection (the same command as the VM compatibility repair),
// which also deploys this build's controller, so the caller continues with the
// repaired-upgrade box review. A no is a warning; no terminal or no answer
// leaves the recreate outstanding. Recreation is never automatic.
func offerSpaceUpgradeVMRecreate(cmd *cobra.Command, space string) (bool, spaceUpgradeVMResult, error) {
	var result spaceUpgradeVMResult
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return false, result, err
	}
	if boot.ImageUpdate != nil {
		return false, result, pendingImageUpdate(boot)
	}
	need, note := readSpaceVMReplacement(boot)
	if note != "" {
		cliNotice(logging.Host, "Space %s: %s.", space, note)
	}
	if need == nil {
		return false, result, nil
	}
	if err := cmd.Context().Err(); err != nil {
		return false, result, err
	}
	cliLogLines(prettylog.LevelNotice, logging.Host, fmt.Sprintf("Space %s needs a VM recreate to match this build: %s.", space, need.reason),
		[]string{need.command(), "Recreation gives the space a fresh VM: " + recreateEffects + "."})
	line := fmt.Sprintf("space %s VM (%%s): %s", space, need.command())
	if !spaceUpgradeVMInteractive(cmd) {
		result.outstanding = append(result.outstanding, fmt.Sprintf(line, "needs terminal consent"))
		return false, result, nil
	}
	consent, err := confirmSpaceUpgradeVM(cmd.InOrStdin(), cmd.ErrOrStderr(), space)
	if err != nil {
		return false, result, fmt.Errorf("no change was made: %w", err)
	}
	if err := cmd.Context().Err(); err != nil {
		return false, result, err
	}
	switch consent {
	case upgradeAccepted:
		return true, result, recreateCompatibleSpace(cmd, boot)
	case upgradeDeclined:
		result.declined = append(result.declined, fmt.Sprintf(line, "declined"))
	default:
		result.outstanding = append(result.outstanding, fmt.Sprintf(line, "no yes or no answer received"))
	}
	return false, result, nil
}

// confirmSpaceUpgradeVM reads exactly one answer line byte by byte, so it never
// consumes input meant for the box questions that follow. Blank, n and no
// decline; y and yes accept; EOF or anything else leaves it unanswered.
func confirmSpaceUpgradeVM(in io.Reader, out io.Writer, space string) (spaceUpgradeConsent, error) {
	resume := suspendCLISteps()
	defer resume()
	if _, err := fmt.Fprintf(out, "Recreate VM %s now to match this build? The box review continues afterwards. [y/N]: ", space); err != nil {
		return upgradeUnanswered, err
	}
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
		if errors.Is(err, io.EOF) {
			return upgradeUnanswered, nil
		}
		if err != nil {
			return upgradeUnanswered, err
		}
	}
	switch strings.ToLower(strings.TrimSpace(answer.String())) {
	case "y", "yes":
		return upgradeAccepted, nil
	case "", "n", "no":
		return upgradeDeclined, nil
	}
	return upgradeUnanswered, nil
}
