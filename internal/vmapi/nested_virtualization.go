package vmapi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// NestedPreference leaves absent values as auto without rewriting old bootstraps.
func NestedPreference(value string) string {
	if value == "" {
		return "auto"
	}
	return value
}

func ValidateNestedPreference(value string) error {
	switch NestedPreference(value) {
	case "auto", "on", "off":
		return nil
	default:
		return fmt.Errorf("nested virtualization must be auto, on or off, not %q", value)
	}
}

// NestedHostFacts are current read-only host observations, not a capability query.
type NestedHostFacts struct {
	OS      string
	Chip    string
	Version string
}

var appleMChip = regexp.MustCompile(`^Apple M([0-9]+)(?: .*)?$`)

func (h NestedHostFacts) Eligible() bool {
	match := appleMChip.FindStringSubmatch(h.Chip)
	if h.OS != "darwin" || len(match) != 2 {
		return false
	}
	generation, err := strconv.Atoi(match[1])
	if err != nil || generation < 3 {
		return false
	}
	major, err := strconv.Atoi(strings.Split(h.Version, ".")[0])
	return err == nil && major >= 15
}

// NestedResolution describes only yoloctl's selected instance-file value.
// Lima overrides and the guest's KVM availability are deliberately not inferred.
type NestedResolution struct {
	Managed bool
	Enabled bool
	Reason  string
}

func ResolveNestedVirtualization(preference, backend string, instance Instance, host NestedHostFacts) (NestedResolution, error) {
	var result NestedResolution
	if err := ValidateNestedPreference(preference); err != nil {
		return result, err
	}
	preference = NestedPreference(preference)
	switch {
	case backend == "firecracker":
		result.Reason = "yoloctl does not manage it for the Firecracker backend"
	case host.OS != "darwin":
		result.Reason = "yoloctl does not manage it for Lima on this system"
	default:
		result.Managed = true
		if instance.VMType != "vz" || instance.Arch != "aarch64" {
			driver, arch := instance.VMType, instance.Arch
			if driver == "" {
				driver = "unknown"
			}
			if arch == "" {
				arch = "unknown"
			}
			result.Reason = fmt.Sprintf("yoloctl manages this setting only for Lima vz instances with a native aarch64 guest (found driver %s, guest %s); the VM type is fixed at creation", driver, arch)
		} else if host.Chip == "" || host.Version == "" {
			result.Reason = "yoloctl could not tell whether this Mac supports it; --nested-virtualization on makes Lima decide"
		} else if !host.Eligible() {
			result.Reason = fmt.Sprintf("this Mac needs Apple M3 or later and macOS 15 or later (found %s, macOS %s)", host.Chip, host.Version)
		}
	}
	if preference == "off" {
		return result, nil
	}
	if preference == "on" {
		if !result.Managed || instance.VMType != "vz" || instance.Arch != "aarch64" {
			return result, fmt.Errorf("%s", result.Reason)
		}
		result.Enabled = true
		return result, nil
	}
	result.Enabled = result.Managed && result.Reason == ""
	return result, nil
}
