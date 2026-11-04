package lima

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// This marker belongs to the retained VM provisioning, not the replaceable
// controller bundle. Mount/resource updates never insert it into an old VM.
const OpenShellVMMarker = "# yoloctl-openshell-vm: nftables-v2"

var ErrOpenShellVMTooOld = errors.New("this space's Lima VM predates native OpenShell provisioning")

// CheckOpenShellVM reads only the selected instance configuration, including
// while stopped. It never boots, rewrites or provisions a VM.
func CheckOpenShellVM(name string) error {
	path, err := InstanceConfigPath(name)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("this space's Lima VM configuration exceeds read bound")
	}
	return checkOpenShellVMConfig(raw)
}
func checkOpenShellVMConfig(raw []byte) error {
	var config struct {
		Provision []struct {
			Mode   string `yaml:"mode"`
			Script string `yaml:"script"`
		} `yaml:"provision"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("read Lima VM provisioning: %w", err)
	}
	for _, p := range config.Provision {
		if p.Mode != "system" {
			continue
		}
		for _, line := range strings.Split(p.Script, "\n") {
			if strings.TrimSpace(line) == OpenShellVMMarker {
				return nil
			}
		}
	}
	return ErrOpenShellVMTooOld
}
