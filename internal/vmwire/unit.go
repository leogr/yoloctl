package vmwire

import "strings"

const (
	AuditUnitName = "yoloctl-audit"
	// ControllerUnitName is the space controller's systemd unit in a Lima
	// guest (internal/controller.ServiceName).
	ControllerUnitName      = "yoloctl-controller.service"
	AuditCapabilityAction   = "audit-capability"
	AuditCapability         = "yoloctl-audit/v1"
	LegacyPortUnitPrefix    = "yoloctl-port-"
	OpenShellPortUnitPrefix = "yoloctl-openshell-port-"
	GuestSystemPath         = "/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin"
)

type Unit struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Args        []string `json:"args,omitempty"`
	Env         []string `json:"env,omitempty"`
	Restart     bool     `json:"restart,omitempty"`
}
type UnitRequest struct {
	Action string `json:"action"`
	Unit   Unit   `json:"unit"`
}

// UnitProcess is a current supervisor observation, never a durable operation
// receipt or authority to adopt a process after replacement.
type UnitProcess struct {
	Name  string `json:"name"`
	State string `json:"state"`
	PID   int    `json:"pid"`
}
type ControlRequest struct {
	Action string       `json:"action"`
	Unit   *UnitRequest `json:"unit,omitempty"`
}
type ControlReply struct {
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// AuditUnit is a fixed root-infrastructure launcher, never a general exec seam.
func AuditUnit() Unit {
	return Unit{Name: AuditUnitName, Description: "yoloctl Falco audit", Args: []string{GuestBinary, "controller-internal", "audit-collect"}, Env: []string{"HOME=/root", "YOLOCTL_CONTROLLER_CONTEXT=/etc/yoloctl/controller.json"}}
}

// ManagedUnitName reports whether name is a managed guest service: the fixed
// audit unit, or a session holder or port forwarder `.service` under its
// prefix, at most 180 bytes and without path or line characters.
func ManagedUnitName(name string) bool {
	if name == AuditUnitName {
		return true
	}
	return len(name) <= 180 && strings.HasSuffix(name, ".service") && !strings.ContainsAny(name, "/\\\x00\r\n") &&
		(strings.HasPrefix(name, "yoloctl-session-") || strings.HasPrefix(name, LegacyPortUnitPrefix) || strings.HasPrefix(name, OpenShellPortUnitPrefix))
}
