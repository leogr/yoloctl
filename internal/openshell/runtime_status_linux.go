package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

// RuntimeStatus is a current observation, never a receipt proving historical
// mutation success. PIDs are inside the container PID namespace; they confer
// no signal/adoption authority. Native callers independently verify the live
// container/helper/incarnation and selected record before using this data.
type RuntimeStatus struct {
	Schema         int       `json:"schema"`
	RecordSHA256   string    `json:"record_sha256"`
	Phase          string    `json:"phase"`
	Reason         string    `json:"reason"`
	SupervisorPID  int       `json:"supervisor_pid"`
	BoundaryPID    int       `json:"boundary_pid"`
	SupervisorExit *int      `json:"supervisor_exit"`
	BoundaryExit   *int      `json:"boundary_exit"`
	ObservedAt     time.Time `json:"observed_at"`
}

func (s RuntimeStatus) validate(record RuntimeRecord) error {
	if s.Schema != BootstrapSchema || s.RecordSHA256 != recordIdentity(record) || s.ObservedAt.IsZero() {
		return errors.New("runtime status belongs to a different generation or schema")
	}
	if s.SupervisorPID < 0 || s.BoundaryPID < 0 || s.SupervisorPID == 1 || s.BoundaryPID == 1 || (s.SupervisorPID != 0 && s.SupervisorPID == s.BoundaryPID) {
		return errors.New("runtime status has invalid child identities")
	}
	for _, exit := range []*int{s.SupervisorExit, s.BoundaryExit} {
		if exit != nil && (*exit < -64 || *exit > 255) {
			return errors.New("runtime status has an invalid exit status")
		}
	}
	if (s.SupervisorExit != nil && s.SupervisorPID == 0) || (s.BoundaryExit != nil && s.BoundaryPID == 0) {
		return errors.New("runtime status reports an exit without a child")
	}
	switch s.Phase {
	case "starting":
		if s.Reason != "startup" || s.SupervisorExit != nil || s.BoundaryExit != nil {
			return errors.New("inconsistent runtime startup status")
		}
	case "ready":
		if s.Reason != "confirmed" || s.SupervisorPID < 2 || s.BoundaryPID < 2 || s.SupervisorExit != nil || s.BoundaryExit != nil {
			return errors.New("inconsistent runtime ready status")
		}
	case "degraded":
		if s.SupervisorPID < 2 || s.BoundaryPID < 2 || s.BoundaryExit != nil || (s.Reason != "supervisor-lost" && s.Reason != "upstream-unready" && s.Reason != "endpoint-invalid") {
			return errors.New("inconsistent degraded runtime status")
		}
		if (s.Reason == "supervisor-lost") != (s.SupervisorExit != nil) {
			return errors.New("runtime supervisor loss lacks its wait result")
		}
	case "stopped":
		if s.Reason != "stopped" || (s.SupervisorPID != 0 && s.SupervisorExit == nil) || (s.BoundaryPID != 0 && s.BoundaryExit == nil) {
			return errors.New("inconsistent stopped runtime status")
		}
	case "stopping":
		if s.Reason != "cleanup-unproved" {
			return errors.New("inconsistent unproved runtime cleanup status")
		}
	default:
		return errors.New("unsupported runtime status phase")
	}
	return nil
}

func observeRuntimeStatus(record RuntimeRecord, phase, reason string, children []*runtimeProcess, now time.Time) RuntimeStatus {
	s := RuntimeStatus{Schema: BootstrapSchema, RecordSHA256: recordIdentity(record), Phase: phase, Reason: reason, ObservedAt: now.UTC()}
	for _, child := range children {
		var exit *int
		if child.exited {
			code := child.status.ExitStatus()
			if child.status.Signaled() {
				code = -int(child.status.Signal())
			}
			exit = &code
		}
		if child.role == runtimeSupervisor {
			s.SupervisorPID, s.SupervisorExit = child.originalPID, exit
		}
		if child.role == runtimeBoundary {
			s.BoundaryPID, s.BoundaryExit = child.originalPID, exit
		}
	}
	return s
}

func writeRuntimeStatus(record RuntimeRecord, status RuntimeStatus) error {
	if os.Getpid() != 1 || os.Geteuid() != 0 || os.Getegid() != 0 {
		return errors.New("runtime observations require the root PID 1 writer")
	}
	if err := status.validate(record); err != nil {
		return err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return safepath.WriteFileOwned(NativeProjection, "generations/"+record.Generation+"/status/runtime.json", raw, 0o600, 0, 0)
}

func decodeRuntimeStatus(raw []byte, record RuntimeRecord) (RuntimeStatus, error) {
	if len(raw) == 0 || len(raw) > 16<<10 {
		return RuntimeStatus{}, errors.New("missing or unbounded runtime observation")
	}
	if err := checkJSONKeys(raw); err != nil {
		return RuntimeStatus{}, err
	}
	var status RuntimeStatus
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return RuntimeStatus{}, err
	}
	return status, status.validate(record)
}
