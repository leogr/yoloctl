package vmwire

const (
	AuditUnitName           = "yoloctl-audit"
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
