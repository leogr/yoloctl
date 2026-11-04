//go:build linux

package resources

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PrepareWorkloads is only for the dedicated microVM init, before any workloads
// exist. Systemd guests use a slice instead; never edit their hierarchy here.
func PrepareWorkloads() error {
	mem, err := ReadHostMemory(context.Background())
	if err != nil {
		return err
	}
	budget, err := Workloads(mem.Total)
	if err != nil {
		return err
	}
	const root = "/sys/fs/cgroup"
	group := CgroupPath(false)
	for _, dir := range []string{root, group} {
		if dir != root {
			if err := os.Mkdir(dir, 0755); err != nil && !os.IsExist(err) {
				return err
			}
		}
		available, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
		if err != nil {
			return err
		}
		controllers := strings.Fields(string(available))
		foundMemory := false
		for i, name := range controllers {
			foundMemory = foundMemory || name == "memory"
			controllers[i] = "+" + name
		}
		if !foundMemory {
			return fmt.Errorf("memory controller unavailable at %s", dir)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte(strings.Join(controllers, " ")), 0600); err != nil {
			return err
		}
	}
	for file, value := range map[string]string{"memory.high": "max", "memory.max": strconv.FormatUint(budget.Max, 10), "memory.swap.max": "0"} {
		if err := os.WriteFile(filepath.Join(group, file), []byte(value), 0600); err != nil {
			return err
		}
	}
	return nil
}
