// Package port manages TCP port forwards from the host into an
// agentbox container. New OpenShell generations use a supervised VM listener
// and a pinned SSH direct-tcpip channel to workload 127.0.0.1:ContainerPort.
// Privileged boxes retain their direct bridge route. The legacy chain is:
//
//	host:HostPort   (loopback exposure through the VM adapter)
//	  -> VM 127.0.0.1:HostPort   (a supervised socat listener)
//	    -> <bridge-ip>:ContainerPort   (outer container netns eth0)
//	      -> PREROUTING DNAT in the outer netns
//	        -> 10.200.0.2:ContainerPort   (inner sandbox netns; the agent)
//
// Legacy dev servers must bind 0.0.0.0 (or 10.200.0.2 directly) inside the
// agentbox. A 127.0.0.1-bound legacy listener is not reachable through the
// DNAT — same caveat as plain `docker run -p`. ip_forward=1 in the
// outer netns is set at agentbox-create time via
// `nerdctl run --sysctl net.ipv4.ip_forward=1`; see
// internal/container.RunOptions.Sysctl. Future fallback (option B) if
// `--sysctl` ever becomes unavailable: enable per-forward via
// `nsenter -t <container-pid> -n sysctl -w net.ipv4.ip_forward=1`
// from the VM. The sysctl is per-netns; nsenter -n joins the netns
// only, leaving the VM's rw /proc visible, so the write succeeds.
package port

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/vmservice"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// unitPrefix identifies forwards across CLI invocations in either supervisor.
const unitPrefix = vmwire.LegacyPortUnitPrefix

// Spec is a single host:container TCP forward owned by an agentbox.
type Spec struct {
	Agentbox      string
	HostPort      int
	ContainerPort int
	// Direct selects privileged creation without inner DNAT. It is not encoded
	// in legacy unit names; teardown inspects the actual current runtime.
	Direct bool
}

// UnitName returns the guest service name for spec. Exposed for the
// CLI / docs / debugging; internal callers should use it too so the
// naming convention stays in one place.
func UnitName(spec Spec) string {
	return fmt.Sprintf("%s%s-%d-%d.service", unitPrefix, spec.Agentbox, spec.HostPort, spec.ContainerPort)
}

// ParseMapping parses "HOST:CONTAINER" or a bare "PORT" (which maps
// PORT:PORT) into host + container port numbers. Used by CLI argument
// parsing.
func ParseMapping(s string) (hostPort, containerPort int, err error) {
	parts := strings.SplitN(s, ":", 2)
	hostPort, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid host port %q: %w", parts[0], err)
	}
	if !validPortRange(hostPort) {
		return 0, 0, fmt.Errorf("host port %d out of range (1-65535)", hostPort)
	}
	if len(parts) == 1 {
		return hostPort, hostPort, nil
	}
	containerPort, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid container port %q: %w", parts[1], err)
	}
	if !validPortRange(containerPort) {
		return 0, 0, fmt.Errorf("container port %d out of range (1-65535)", containerPort)
	}
	return hostPort, containerPort, nil
}

func validPortRange(p int) bool { return p >= 1 && p <= 65535 }

// Forward starts a supervised guest service that listens on
// 127.0.0.1:HostPort in the VM, proxies to the agentbox's outer netns
// at bridge-IP:ContainerPort, and installs a PREROUTING DNAT rule
// inside that outer netns to redirect the packet into the inner
// sandbox netns at 10.200.0.2:ContainerPort where the agent lives.
// Returns an error wrapping ErrPortInUse when another yoloctl-managed
// forward already binds HostPort (the conflict's owning agentbox is
// named in the error message).
func Forward(ctx context.Context, c *container.Client, spec Spec) error {
	ip, err := c.ContainerIP(ctx, spec.Agentbox)
	if err != nil {
		return err
	}
	if ip == "" {
		return fmt.Errorf("agentbox %s has no bridge IP yet — start it first", spec.Agentbox)
	}
	if err := assertNoConflict(ctx, c, spec); err != nil {
		return err
	}
	unit := UnitName(spec)
	desc := fmt.Sprintf("yoloctl port-forward %s %d -> %s:%d",
		spec.Agentbox, spec.HostPort, ip, spec.ContainerPort)
	if err := vmservice.Start(ctx, c, vmservice.Unit{Name: unit, Description: desc, Restart: true, Args: []string{"/usr/bin/socat", fmt.Sprintf("TCP-LISTEN:%d,bind=127.0.0.1,reuseaddr,fork", spec.HostPort), fmt.Sprintf("TCP:%s:%d", ip, spec.ContainerPort)}}); err != nil {
		return err
	}

	if spec.Direct {
		return nil
	}
	if err := installInnerNetnsDNAT(ctx, c, spec.Agentbox, spec.ContainerPort); err != nil {
		// Roll back the socat unit so we don't leave a half-configured
		// forward that listens but never connects.
		_ = stopUnit(ctx, c, unit)
		return fmt.Errorf("install inner-netns DNAT for %s: %w", spec.Agentbox, err)
	}
	return nil
}

// dnatRuleArgs returns the iptables args (without the `nerdctl exec`
// prefix) for the inner-netns DNAT rule. op is "-A" to add or "-D" to
// delete; both forms must match exactly for the kernel to find the
// rule on delete. Pulled out so tests can verify the rule shape
// without shelling out.
func dnatRuleArgs(op string, containerPort int) []string {
	return []string{
		"iptables", "-t", "nat", op, "PREROUTING",
		"-i", "eth0", "-p", "tcp",
		"--dport", strconv.Itoa(containerPort),
		"-j", "DNAT",
		"--to-destination", fmt.Sprintf("%s:%d", sandbox.InnerNetnsIP, containerPort),
	}
}

// installInnerNetnsDNAT appends the PREROUTING DNAT rule in the
// agentbox's outer container netns. Runs as `nerdctl exec` — this is
// configuration, not a long-lived sidecar (the rule lives in the
// kernel's netns rule table, not in a user-space process), so the
// runtime adds no long-lived outer-container sidecar.
//
// Multiple forwards to the same container port (e.g., 3000:3000 and
// 8080:3000) intentionally accumulate as duplicate rules; iptables
// matches the first one, and each Unforward removes one copy, so the
// pair stays balanced. Worst case during pathological churn: a few
// redundant rules in PREROUTING that all do the same thing.
func installInnerNetnsDNAT(ctx context.Context, c *container.Client, agentbox string, containerPort int) error {
	args := append([]string{"sudo", "nerdctl", "exec", agentbox}, dnatRuleArgs("-A", containerPort)...)
	return c.Shell(ctx, args...)
}

// removeInnerNetnsDNAT deletes the matching DNAT rule. Best-effort:
// if the container is already stopped, its outer netns is destroyed
// by the runtime and every rule with it, so any non-zero exit from
// iptables -D is treated as already-gone.
func removeInnerNetnsDNAT(ctx context.Context, c *container.Client, agentbox string, containerPort int) {
	args := append([]string{"sudo", "nerdctl", "exec", agentbox}, dnatRuleArgs("-D", containerPort)...)
	_ = c.Shell(ctx, args...)
}

// ErrPortInUse marks a Forward failure caused by a duplicate listener.
var ErrPortInUse = errors.New("host port in use by another forward")

func assertNoConflict(ctx context.Context, c *container.Client, spec Spec) error {
	active, err := List(ctx, c, "")
	if err != nil {
		return fmt.Errorf("list existing forwards: %w", err)
	}
	for _, s := range active {
		if s.HostPort == spec.HostPort {
			return fmt.Errorf("%w: host port %d already mapped by agentbox %s (container port %d)",
				ErrPortInUse, spec.HostPort, s.Agentbox, s.ContainerPort)
		}
	}
	return nil
}

// Unforward removes each retained service for this mapping using its actual
// transport. Desired privilege or a later same-name box cannot reinterpret a
// native SSH service as a legacy DNAT service.
func Unforward(ctx context.Context, c *container.Client, spec Spec) error {
	known, err := forwardsForRemoval(ctx, c, spec.Agentbox)
	if err != nil {
		return err
	}
	for _, service := range known {
		if service.Spec.HostPort == spec.HostPort && service.Spec.ContainerPort == spec.ContainerPort {
			if err := stopForwardService(ctx, c, service); err != nil {
				return err
			}
		}
	}
	return nil
}

func forwardsForRemoval(ctx context.Context, c *container.Client, agentbox string) ([]ForwardService, error) {
	known, err := ListServices(ctx, c, agentbox)
	if err != nil {
		return nil, err
	}
	for _, service := range known {
		if service.OpenShell {
			// No active-only/old-init observation can authorize native stop.
			return Inventory(ctx, c, agentbox)
		}
	}
	return known, nil
}

func stopForwardService(ctx context.Context, c *container.Client, service ForwardService) error {
	if service.OpenShell {
		if err := vmservice.StopAcknowledged(ctx, c, service.Unit); err != nil {
			if ctx.Err() != nil { // lost reply only
				return &NativeEffectsUncertain{Cause: fmt.Errorf("forward service stop was not acknowledged; do not retry automatically: %w", err)}
			}
			return fmt.Errorf("forward service %s stop failed: %w", service.Unit, err)
		}
		_ = vmservice.Collect(ctx, c, service.Unit)
		return nil
	}
	remove, err := legacyDNATRequired(ctx, c, service.Spec.Agentbox)
	if err != nil {
		return err
	}
	if remove {
		removeInnerNetnsDNAT(ctx, c, service.Spec.Agentbox, service.Spec.ContainerPort)
	}
	return stopUnit(ctx, c, service.Unit)
}

// A legacy unit identifies its transport, not the current same-name container.
// Execute DNAT cleanup only in a positively identified, running legacy box.
// An authoritative absent/stopped container has already lost its old netns.
func legacyDNATRequired(ctx context.Context, c *container.Client, agentbox string) (bool, error) {
	boxes, err := c.List(ctx, "yoloctl.agentbox="+agentbox)
	if err != nil {
		return false, err
	}
	found := false
	for _, box := range boxes {
		if box.Name == agentbox {
			if found {
				return false, errors.New("duplicate managed container identity during forward cleanup")
			}
			found = true
		}
	}
	if !found {
		return false, nil
	}
	d, err := c.Inspect(ctx, agentbox)
	if err != nil {
		return false, err
	}
	return legacyDNATDetails(d, agentbox)
}

func legacyDNATDetails(d *container.Details, agentbox string) (bool, error) {
	if d == nil || d.Name != agentbox || d.Labels["yoloctl.agentbox"] != agentbox || d.Privileged == nil {
		return false, errors.New("cannot establish current container mode for legacy forward cleanup")
	}
	if !d.Running || *d.Privileged {
		return false, nil
	}
	args := d.Command
	if len(args) > 0 && args[0] == sandbox.EntrypointShimPath {
		args = args[1:]
	}
	if len(args) > 1 && args[0] == openshell.HelperBinary && args[1] == openshell.HelperCommand {
		return false, nil
	}
	for _, key := range []string{openshell.LabelProtocol, openshell.LabelQualification, openshell.LabelHelperSHA256, openshell.LabelIncarnation, openshell.LabelControlRoot} {
		if _, present := d.Labels[key]; present {
			return false, errors.New("mixed legacy/native runtime evidence during forward cleanup")
		}
	}
	if len(args) > 0 && args[0] == sandbox.BinaryDirContainerPath+"/openshell-sandbox" {
		return true, nil
	}
	return false, errors.New("unrecognized runtime during legacy forward cleanup")
}

// UnforwardAgentbox stops every yoloctl-managed forward for the named
// box. Called by box stop / destroy so port mappings don't
// outlive the container.
func UnforwardAgentbox(ctx context.Context, c *container.Client, agentbox string) error {
	forwards, err := forwardsForRemoval(ctx, c, agentbox)
	if err != nil {
		return err
	}
	for _, service := range forwards {
		if err := stopForwardService(ctx, c, service); err != nil {
			return err
		}
	}
	return nil
}

func stopUnit(ctx context.Context, c *container.Client, unit string) error {
	return vmservice.Stop(ctx, c, unit)
}

// List projects the managed port view to numeric mappings. Call ListServices
// when the actual service/transport identity is needed.
// Pass agentbox="" to return every forward; otherwise filter to the
// named agentbox.
func List(ctx context.Context, c *container.Client, agentbox string) ([]Spec, error) {
	services, err := ListServices(ctx, c, agentbox)
	if err != nil {
		return nil, err
	}
	specs := make([]Spec, 0, len(services))
	for _, service := range services {
		specs = append(specs, service.Spec)
	}
	return specs, nil
}

// ParseUnitName splits a unit name back into a Spec. Returns ok=false
// if the input is not a yoloctl-port-* unit or has the wrong shape.
func ParseUnitName(name string) (Spec, bool) {
	if !strings.HasPrefix(name, unitPrefix) {
		return Spec{}, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, unitPrefix), ".service")
	// body is "<agentbox>-<host>-<container>". Agentbox names allow
	// hyphens, so split from the right: last two segments are the
	// ports, the prefix is the agentbox name.
	parts := strings.Split(body, "-")
	if len(parts) < 3 {
		return Spec{}, false
	}
	container, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return Spec{}, false
	}
	host, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		return Spec{}, false
	}
	agentbox := strings.Join(parts[:len(parts)-2], "-")
	if agentbox == "" {
		return Spec{}, false
	}
	return Spec{Agentbox: agentbox, HostPort: host, ContainerPort: container}, true
}
