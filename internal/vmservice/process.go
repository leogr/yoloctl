package vmservice

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// Process observes the currently supervised main process. Callers must still
// pin and verify that process independently before trusting its status data.
func Process(ctx context.Context, c *container.Client, name string) (vmwire.UnitProcess, error) {
	if !validProcessUnit(name) {
		return vmwire.UnitProcess{}, errors.New("invalid managed process unit")
	}
	native, err := microvm(c)
	if err != nil {
		return vmwire.UnitProcess{}, err
	}
	if native {
		raw, err := request(ctx, c, vmwire.UnitRequest{Action: "process", Unit: Unit{Name: name}})
		if err != nil {
			return vmwire.UnitProcess{}, err
		}
		var observed vmwire.UnitProcess
		if len(raw) > 4096 || json.Unmarshal([]byte(raw), &observed) != nil {
			return observed, errors.New("invalid managed process response")
		}
		canonical, err := json.Marshal(observed)
		if err != nil || string(canonical) != raw {
			return observed, errors.New("noncanonical managed process response")
		}
		return observed, validateProcess(observed, name)
	}
	raw, err := c.ShellOutput(ctx, "sudo", "systemctl", "show", "--no-pager", "--property=Id,ActiveState,MainPID", "--", name)
	if err != nil {
		return vmwire.UnitProcess{}, err
	}
	return parseSystemdProcess(raw, name)
}

func validProcessUnit(name string) bool {
	return len(name) <= 180 && strings.HasSuffix(name, ".service") && !strings.ContainsAny(name, "/\\\x00\r\n\t ") &&
		(strings.HasPrefix(name, "yoloctl-session-") || strings.HasPrefix(name, "yoloctl-port-") || strings.HasPrefix(name, "yoloctl-openshell-port-"))
}

func validateProcess(process vmwire.UnitProcess, name string) error {
	if !validProcessUnit(name) || process.Name != name || process.PID < 0 || process.PID == 1 {
		return errors.New("managed process unit/identity mismatch")
	}
	switch process.State {
	case "active":
		if process.PID <= 1 {
			return errors.New("active managed unit has no main process")
		}
	case "activating", "deactivating":
	case "inactive", "failed", "unknown":
		if process.PID != 0 {
			return errors.New("inactive managed unit still reports a main process")
		}
	default:
		return errors.New("unknown managed process state")
	}
	return nil
}

func parseSystemdProcess(raw, name string) (vmwire.UnitProcess, error) {
	var observed vmwire.UnitProcess
	if len(raw) > 4096 {
		return observed, errors.New("managed process properties exceed bound")
	}
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] {
			return observed, errors.New("invalid/duplicate managed process property")
		}
		seen[key] = true
		switch key {
		case "Id":
			observed.Name = value
		case "ActiveState":
			observed.State = value
		case "MainPID":
			pid, err := strconv.Atoi(value)
			if err != nil || strconv.Itoa(pid) != value {
				return observed, errors.New("invalid managed process PID")
			}
			observed.PID = pid
		default:
			return observed, errors.New("unknown managed process property")
		}
	}
	if len(seen) != 3 {
		return observed, errors.New("missing managed process property")
	}
	return observed, validateProcess(observed, name)
}
