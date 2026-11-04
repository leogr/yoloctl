package openshell

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
)

const (
	HelperCommand        = "openshell-runtime-internal"
	NetworkWorkerCommand = "openshell-network-internal"
	HelperProtocol       = "2"
	HelperMember         = "yoloctl-openshell-helper"
	HelperDirectory      = "/opt/yoloctl/openshell-helper"
	HelperBinary         = HelperDirectory + "/" + HelperMember
	NativeProjection     = "/.openshell/native"
	CurrentRecordPath    = NativeProjection + "/current.json"
	ArtifactProjection   = "/.openshell/runtime"
	RuntimeRoot          = "/opt/openshell/runtime"
	HelperRoot           = "/opt/yoloctl/openshell-helpers"
	LabelProtocol        = "yoloctl.openshell.protocol"
	LabelQualification   = "yoloctl.openshell.qualification"
	LabelHelperSHA256    = "yoloctl.openshell.helper_sha256"
	LabelIncarnation     = "yoloctl.openshell.incarnation"
	LabelControlRoot     = "yoloctl.openshell.control_root"
)

func RuntimeDirectory(qualification string) (string, error) {
	if !lowerHex(qualification, 32) {
		return "", errors.New("invalid OpenShell runtime directory identity")
	}
	return RuntimeRoot + "/" + qualification, nil
}

func HelperSourceDirectory(helperSHA256 string) (string, error) {
	if !lowerHex(helperSHA256, 32) {
		return "", errors.New("invalid OpenShell helper directory identity")
	}
	return HelperRoot + "/" + helperSHA256, nil
}

// HelperLaunch is the nonsecret, inspectable PID 1 contract. Authority comes
// from verified native preparation and read-only exact-build binds; these
// arguments cannot authorize a workload or supply an arbitrary executable.
type HelperLaunch struct {
	Protocol      string
	Workdir       string
	Qualification string
	HelperSHA256  string
	Incarnation   string
}

func (c LaunchConfig) HelperLaunch() HelperLaunch {
	return HelperLaunch{HelperProtocol, c.Workdir, c.Binding.Qualification, c.HelperSHA256, c.Binding.Incarnation}
}

func (l HelperLaunch) Args() ([]string, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	return []string{HelperBinary, HelperCommand, "runtime", "--protocol", l.Protocol, "--workdir", l.Workdir, "--qualification", l.Qualification, "--helper-sha256", l.HelperSHA256, "--incarnation", l.Incarnation}, nil
}

// ParseHelperLaunch accepts only the recorded PID 1 form, after HelperCommand.
// It accepts a well-formed historical protocol for inspection; serving that
// protocol is a separate exact-build check. Child roles are never PID 1 proof.
func ParseHelperLaunch(args []string) (HelperLaunch, error) {
	if len(args) == 0 || args[0] != "runtime" {
		return HelperLaunch{}, errors.New("missing OpenShell runtime role")
	}
	return parseHelperFlags(args[1:])
}

func parseHelperFlags(args []string) (HelperLaunch, error) {
	var launch HelperLaunch
	seen := map[string]bool{}
	for len(args) > 0 {
		if len(args) < 2 || seen[args[0]] {
			return HelperLaunch{}, errors.New("incomplete or duplicate OpenShell helper argument")
		}
		flag, value := args[0], args[1]
		seen[flag] = true
		switch flag {
		case "--protocol":
			launch.Protocol = value
		case "--workdir":
			launch.Workdir = value
		case "--qualification":
			launch.Qualification = value
		case "--helper-sha256":
			launch.HelperSHA256 = value
		case "--incarnation":
			launch.Incarnation = value
		default:
			return HelperLaunch{}, fmt.Errorf("unknown OpenShell helper argument %q", flag)
		}
		args = args[2:]
	}
	if err := launch.validate(); err != nil {
		return HelperLaunch{}, err
	}
	return launch, nil
}

func (l HelperLaunch) validate() error {
	if l.Protocol == "" || len(l.Protocol) > 16 || strings.IndexFunc(l.Protocol, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return errors.New("invalid OpenShell helper protocol")
	}
	if !path.IsAbs(l.Workdir) || path.Clean(l.Workdir) != l.Workdir || l.Workdir == "/" || strings.IndexFunc(l.Workdir, unicode.IsControl) >= 0 {
		return errors.New("OpenShell helper requires its explicit real workdir")
	}
	if !lowerHex(l.Qualification, 32) || !lowerHex(l.HelperSHA256, 32) || !lowerHex(l.Incarnation, 16) {
		return errors.New("OpenShell helper requires exact qualification, binary and incarnation identities")
	}
	return nil
}

// SupervisorEnvironment never inherits the root helper or workload environment.
// In particular OPENSHELL_ENDPOINT, parent liveness, arbitrary proxy settings
// and the private user-environment channel cannot reach the supervisor.
func SupervisorEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/.openshell/supervisor/run",
		"TMPDIR=/.openshell/supervisor/run/tmp",
		"OPENSHELL_ADMITTED_ISOLATION_BACKEND=openshell-sandbox",
		"OPENSHELL_PROXY_TLS_DIR=/.openshell/supervisor/run/proxy-tls",
		"OPENSHELL_TELEMETRY_ENABLED=false",
		"OPENSHELL_LOG_LEVEL=info",
	}
}

// BoundaryEnvironment supplies controlled defaults. The verified PID 1 also
// carries nonreserved image/container ENV through the sealed boundary payload;
// private account overrides stay in the consumed bootstrap, never launch argv.
func BoundaryEnvironment() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/home/sandbox", "OPENSHELL_LOG_LEVEL=info"}
}
