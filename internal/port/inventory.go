package port

import (
	"bufio"
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmservice"
)

// ForwardService retains its actual service/transport identity separately
// from declared numeric ports. A later same-name container cannot change it.
type ForwardService struct {
	Spec      Spec
	Unit      string
	OpenShell bool
}

// Inventory includes all known port services, not just currently active
// workers. New native effects require this matching-init API explicitly.
func Inventory(ctx context.Context, c *container.Client, agentbox string) ([]ForwardService, error) {
	raw, err := vmservice.InventoryPorts(ctx, c)
	if err != nil {
		return nil, err
	}
	return parseForwardInventory(raw, agentbox)
}

// ListServices returns the managed port view with actual transport identity.
// Matching init and Lima include retained/restarting services. An older init
// keeps its historical legacy view; it cannot qualify new native effects.
func ListServices(ctx context.Context, c *container.Client, agentbox string) ([]ForwardService, error) {
	raw, err := vmservice.List(ctx, c, unitPrefix)
	if err != nil {
		return nil, err
	}
	return parseForwardInventory(raw, agentbox)
}

func parseForwardInventory(raw, agentbox string) ([]ForwardService, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("managed forward inventory exceeds bound")
	}
	var forwards []ForwardService
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		var spec Spec
		var ok, native bool
		switch {
		case strings.HasPrefix(name, OpenShellUnitPrefix):
			spec, ok = ParseOpenShellUnitName(name)
			native = true
		case strings.HasPrefix(name, unitPrefix):
			spec, ok = ParseUnitName(name)
			_, shape := OpenShellUnitName(spec)
			ok = ok && shape == nil && UnitName(spec) == name
		default:
			continue
		}
		if !ok || seen[name] {
			return nil, errors.New("invalid or duplicate managed forward identity")
		}
		seen[name] = true
		if agentbox != "" && spec.Agentbox != agentbox {
			continue
		}
		forwards = append(forwards, ForwardService{Spec: spec, Unit: name, OpenShell: native})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(forwards, func(i, j int) bool {
		left, right := forwards[i], forwards[j]
		if left.Spec.Agentbox != right.Spec.Agentbox {
			return left.Spec.Agentbox < right.Spec.Agentbox
		}
		if left.Spec.HostPort != right.Spec.HostPort {
			return left.Spec.HostPort < right.Spec.HostPort
		}
		return left.Unit < right.Unit
	})
	return forwards, nil
}
