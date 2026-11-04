package main

import (
	"strings"

	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// workloadPoolRel is the box workload cgroup pool below /sys/fs/cgroup for a
// guest service manager: the microvm group, or for systemd (and an unset
// manager) the systemd slice. Space telemetry and the chat peer check share
// it, so they cannot disagree about where boxes run.
func workloadPoolRel(serviceManager string) string {
	return strings.TrimPrefix(resources.CgroupPath(serviceManager != "microvm"), "/sys/fs/cgroup/")
}

// currentServiceManager is the trusted space context's service manager, or
// "" outside a native controller.
func currentServiceManager() string {
	if current := spaceenv.Current(); current != nil {
		return current.ServiceManager
	}
	return ""
}
