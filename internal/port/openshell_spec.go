package port

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/vmwire"
)

const (
	OpenShellUnitPrefix  = vmwire.OpenShellPortUnitPrefix
	ForwardWorkerCommand = "openshell-forward-internal"
)

// ForwardBinding is nonsecret fixed service metadata, never authority. The
// native worker must independently re-read the matching disk/container/record
// and pinned SSH endpoint on every connection. No address, executable, shell
// command, credential or caller-supplied PID/namespace is accepted here.
type ForwardBinding struct {
	ContainerID   string
	Incarnation   string
	Qualification string
	HelperSHA256  string
	Generation    string
	RecordSHA256  string
	Attempt       string
}

func (b ForwardBinding) validate() error {
	for _, field := range []struct {
		value string
		size  int
	}{{b.ContainerID, 32}, {b.Incarnation, 16}, {b.Qualification, 32}, {b.HelperSHA256, 32}, {b.RecordSHA256, 32}, {b.Attempt, 16}} {
		if len(field.value) != field.size*2 {
			return errors.New("invalid OpenShell forward identity")
		}
		raw, err := hex.DecodeString(field.value)
		if err != nil || len(raw) != field.size || hex.EncodeToString(raw) != field.value {
			return errors.New("invalid OpenShell forward identity")
		}
	}
	if len(b.Generation) != 49 || b.Generation[0] != 'g' {
		return errors.New("invalid OpenShell forward generation")
	}
	raw, err := hex.DecodeString(b.Generation[1:])
	if err != nil || len(raw) != 24 || hex.EncodeToString(raw) != b.Generation[1:] {
		return errors.New("invalid OpenShell forward generation")
	}
	return nil
}

// OpenShellUnitName distinguishes the SSH transport from legacy DNAT units.
// A teardown can identify its actual transport from an authoritative service
// inventory instead of guessing from a later container with the same name.
func OpenShellUnitName(spec Spec) (string, error) {
	if spec.Direct || !validPortRange(spec.HostPort) || !validPortRange(spec.ContainerPort) {
		return "", errors.New("OpenShell forward requires an ordinary box and declared TCP ports")
	}
	if err := layout.ValidateName("agentbox", spec.Agentbox); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s-%d-%d.service", OpenShellUnitPrefix, spec.Agentbox, spec.HostPort, spec.ContainerPort), nil
}

func ParseOpenShellUnitName(name string) (Spec, bool) {
	if !strings.HasPrefix(name, OpenShellUnitPrefix) || !strings.HasSuffix(name, ".service") {
		return Spec{}, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, OpenShellUnitPrefix), ".service")
	if strings.ContainsRune(body, 0) {
		return Spec{}, false
	}
	last := strings.LastIndexByte(body, '-')
	if last < 0 {
		return Spec{}, false
	}
	previous := strings.LastIndexByte(body[:last], '-')
	if previous < 0 {
		return Spec{}, false
	}
	host, err := strconv.Atoi(body[previous+1 : last])
	if err != nil {
		return Spec{}, false
	}
	port, err := strconv.Atoi(body[last+1:])
	if err != nil {
		return Spec{}, false
	}
	spec := Spec{Agentbox: body[:previous], HostPort: host, ContainerPort: port}
	canonical, err := OpenShellUnitName(spec)
	return spec, err == nil && canonical == name
}

func ForwardWorkerArgs(spec Spec, binding ForwardBinding) ([]string, error) {
	if _, err := OpenShellUnitName(spec); err != nil {
		return nil, err
	}
	if err := binding.validate(); err != nil {
		return nil, err
	}
	return []string{ForwardWorkerCommand, "--box", spec.Agentbox, "--host-port", strconv.Itoa(spec.HostPort), "--target-port", strconv.Itoa(spec.ContainerPort),
		"--container-id", binding.ContainerID, "--incarnation", binding.Incarnation, "--qualification", binding.Qualification, "--helper-sha256", binding.HelperSHA256,
		"--generation", binding.Generation, "--record-sha256", binding.RecordSHA256, "--attempt", binding.Attempt}, nil
}

// ParseForwardWorker accepts only the body following ForwardWorkerCommand.
// This is an infrastructure descriptor parser, not a public CLI command.
func ParseForwardWorker(args []string) (Spec, ForwardBinding, error) {
	var spec Spec
	var binding ForwardBinding
	seen := map[string]bool{}
	for len(args) > 0 {
		if len(args) < 2 || seen[args[0]] {
			return Spec{}, ForwardBinding{}, errors.New("incomplete or duplicate OpenShell forward argument")
		}
		flag, value := args[0], args[1]
		seen[flag] = true
		args = args[2:]
		switch flag {
		case "--box":
			spec.Agentbox = value
		case "--host-port", "--target-port":
			parsed, err := strconv.Atoi(value)
			if err != nil || strconv.Itoa(parsed) != value {
				return Spec{}, ForwardBinding{}, errors.New("noncanonical OpenShell forward TCP port")
			}
			if flag == "--host-port" {
				spec.HostPort = parsed
			} else {
				spec.ContainerPort = parsed
			}
		case "--container-id":
			binding.ContainerID = value
		case "--incarnation":
			binding.Incarnation = value
		case "--qualification":
			binding.Qualification = value
		case "--helper-sha256":
			binding.HelperSHA256 = value
		case "--generation":
			binding.Generation = value
		case "--record-sha256":
			binding.RecordSHA256 = value
		case "--attempt":
			binding.Attempt = value
		default:
			return Spec{}, ForwardBinding{}, errors.New("unknown OpenShell forward argument")
		}
	}
	if len(seen) != 10 {
		return Spec{}, ForwardBinding{}, errors.New("missing OpenShell forward identity argument")
	}
	if _, err := ForwardWorkerArgs(spec, binding); err != nil {
		return Spec{}, ForwardBinding{}, err
	}
	return spec, binding, nil
}
