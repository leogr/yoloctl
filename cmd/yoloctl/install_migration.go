package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"gopkg.in/yaml.v3"
)

var defaultInstallImage = installDefaultImageReadOnly

// limaOpenShellCheck reads only the instance configuration (works while the
// VM is stopped, never boots it); tests replace it.
var limaOpenShellCheck = lima.CheckOpenShellVM

// spaceAdvice is what one space needs after an install, for display only:
// install never runs a space command (option C). Problem is a failed check;
// Notice is existing guidance (a pending VM image update); Items are the
// commands the user may run, with what each does.
type spaceAdvice struct {
	Name, State, Header string
	Problem, Notice     string
	Items               []adviceItem
}

type adviceItem struct {
	Label string // update, recreate or optional
	Args  []string
	Note  string
}

const recreateEffects = "a fresh VM, sessions and VM-local workdirs are lost; accounts, box settings and host workdirs stay; start boxes afterwards"

// installSpaceAdvice inventories every space read-only. It never boots,
// locks or changes a space, and it never claims an image is current or
// obsolete: an absent default only says install did not change the image.
func installSpaceAdvice(ctx context.Context, target string) []spaceAdvice {
	root, err := layout.SpacesDir()
	if err != nil {
		return []spaceAdvice{{Problem: "could not list spaces: " + err.Error()}}
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []spaceAdvice{{Problem: "could not list spaces: " + err.Error()}}
	}
	var advice []spaceAdvice
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		a := spaceAdvice{Name: name}
		if err := layout.ValidateName("space", name); err != nil || !entry.IsDir() {
			a.Problem = "not a readable space directory"
			advice = append(advice, a)
			continue
		}
		boot, err := spaceconfig.LoadBootstrap(name)
		if err != nil {
			a.Problem = "could not read this space: " + err.Error()
			advice = append(advice, a)
			continue
		}
		if boot.ImageUpdate != nil {
			a.Notice = pendingImageUpdate(boot).Error()
			advice = append(advice, a)
			continue
		}
		snapshot, err := observeInstallConsent(ctx, name)
		if err != nil {
			a.Problem = "could not check this space: " + err.Error()
			advice = append(advice, a)
			continue
		}
		coherent, err := consentSnapshot(boot, snapshot.State)
		if err != nil || coherent != snapshot {
			a.Problem = "could not check this space: its settings changed during the check"
			advice = append(advice, a)
			continue
		}
		a.State = strings.ToLower(snapshot.State)
		if boot.VM.Backend != "firecracker" {
			if err := limaOpenShellCheck(boot.Name); errors.Is(err, lima.ErrOpenShellVMTooOld) {
				// A controller update is known to fail on this VM (and would boot a
				// stopped one first): advise the recreation that fixes it instead.
				a.Header = "needs recreate"
				a.Items = []adviceItem{{Label: "recreate", Args: []string{"space", "recreate", name},
					Note: "this VM predates this build's OpenShell provisioning, so a controller update would fail; recreating deploys this build's controller and starts the VM: " + recreateEffects}}
				advice = append(advice, a)
				continue
			} else if err != nil {
				a.Problem = "could not check this space's Lima OpenShell provisioning: " + err.Error()
				advice = append(advice, a)
				continue
			}
		}
		if snapshot.Bundle != target {
			a.Header = "uses another controller build"
			note := "when convenient; a controller-only update keeps agents running, and it asks separately about any required recreate or session restart; box commands need this update"
			if snapshot.State == "Stopped" {
				note = "this starts the VM; box commands need this update"
			}
			a.Items = append(a.Items, adviceItem{Label: "update", Args: []string{"space", "start", name, "--update-controller"}, Note: note})
		}
		if boot.VM.Backend == "firecracker" {
			expected, err := defaultInstallImage()
			switch {
			case err == nil && boot.VM.Image == expected:
			case errors.Is(err, os.ErrNotExist) || err == nil && expected == "":
				a.Items = append(a.Items, adviceItem{Label: "optional", Args: []string{"space", "recreate", name, "--update-vm-image"},
					Note: "install did not change this space's VM image; recreating uses this release's: " + recreateEffects})
			case err != nil:
				a.Problem = "could not check this space's VM image: " + err.Error()
			default:
				a.Items = append(a.Items, adviceItem{Label: "optional", Args: []string{"space", "recreate", name, "--update-vm-image"},
					Note: "this space's VM image is not this release's default; install did not change it. Recreating uses this release's: " + recreateEffects})
			}
		} else if item, problem := installLimaAdvice(boot.Name); problem != "" {
			a.Problem = problem
		} else if item != nil {
			a.Items = append(a.Items, *item)
		}
		if a.Problem != "" || len(a.Items) > 0 {
			advice = append(advice, a)
		}
	}
	return advice
}

// printSpaceAdvice writes one block per space that needs something. When
// nothing was installed, the commands still select the old build.
func printSpaceAdvice(o installOut, advice []spaceAdvice, link string, installed bool) {
	notedOld := false
	for _, a := range advice {
		name := o.paint(roleBrand, a.Name)
		switch {
		case a.Name == "":
			o.bad(a.Problem)
			continue
		case a.Notice != "":
			o.warn(a.Notice)
			continue
		}
		if a.Problem != "" {
			o.bad(name, ": ", a.Problem)
		}
		if len(a.Items) == 0 {
			continue
		}
		if !installed && !notedOld {
			notedOld = true
			o.warn("Not installed yet: the commands below run ", o.paint(roleLabel, link), ", which still selects the old build until this release is installed.")
		}
		o.line()
		header := name
		if a.State != "" {
			state := o.paint(roleDim, " · "+a.State)
			if a.State == "running" {
				state = o.paint(roleDim, " · ") + o.paint(roleOK, "running")
			}
			header += state
		}
		if a.Header != "" {
			header += o.paint(roleDim, " · "+a.Header)
		}
		o.line(header)
		for _, item := range a.Items {
			role := roleWarn
			if item.Label == "optional" {
				role = roleDim
			}
			o.line("  ", o.paint(role, fmt.Sprintf("%-10s", item.Label)), o.paint(roleBold, installCommand(link, item.Args...)))
			o.dim(12, item.Note)
		}
	}
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

// installLimaAdvice is the optional Lima stack replacement for a space whose
// effective archives differ from this build's pins, or a problem when the
// effective archives cannot be proved.
func installLimaAdvice(space string) (*adviceItem, string) {
	desired, err := limaArchives(vmhost.TemplateWithAccountDisk(nil, "", false))
	if err != nil {
		return nil, "could not read this build's Lima pins: " + err.Error()
	}
	if len(desired) == 0 {
		return nil, ""
	}
	home, err := layout.LimaHome()
	if err != nil {
		return nil, "could not find the Lima home: " + err.Error()
	}
	for _, name := range []string{"default.yaml", "override.yaml"} {
		raw, err := safepath.ReadFile(home, filepath.Join("_config", name), 1<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, "could not read the global Lima configuration: " + err.Error()
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			return nil, "could not prove this space's Lima stack: a global Lima configuration is present"
		}
	}
	dir, err := vmhost.InstanceDir(space)
	if err != nil {
		return nil, "could not read this space's Lima configuration: " + err.Error()
	}
	raw, err := safepath.ReadFile(dir, "lima.yaml", 1<<20)
	if err != nil {
		return nil, "could not read this space's Lima configuration: " + err.Error()
	}
	effective, err := limaArchives(raw)
	if err != nil {
		return nil, "could not prove this space's Lima stack: " + err.Error()
	}
	if reflect.DeepEqual(desired, effective) {
		return nil, ""
	}
	note := "replaces this space's Lima container stack with this build's pins, after the controller update; recreating starts the VM: " + recreateEffects
	if len(effective) == 0 {
		note = "this space's stack is unpinned and follows the host's Lima default. " + strings.ToUpper(note[:1]) + note[1:]
	}
	return &adviceItem{Label: "optional", Args: []string{"space", "recreate", space}, Note: note}, ""
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
