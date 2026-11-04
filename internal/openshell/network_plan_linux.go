package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"
)

// nativeEgressPlan is a read-only discovery result, never proof that a rule,
// namespace pin or activation exists. The ownership ledger must durably bind
// this complete final plan before policy effects. It is not wired into the
// activation/apply path until that ledger and its owned retirement exist.
type nativeEgressPlan struct {
	Schema      int               `json:"schema"`
	Namespace   namespaceIdentity `json:"namespace"`
	Seed        EgressInputs      `json:"seed"`
	Observed    []netip.Prefix    `json:"observed"`
	Inputs      EgressInputs      `json:"inputs"`
	RulesSHA256 string            `json:"rules_sha256"`
}

func newNativeEgressPlan(namespace namespaceIdentity, seed EgressInputs, observed []netip.Prefix) (nativeEgressPlan, error) {
	if namespace.Device == 0 || namespace.Inode == 0 {
		return nativeEgressPlan{}, errors.New("native egress plan lacks its selected namespace")
	}
	seed, err := canonicalNativeEgressInputs(seed)
	if err != nil {
		return nativeEgressPlan{}, err
	}
	observed, err = canonicalNativeObservedPrefixes(observed)
	if err != nil {
		return nativeEgressPlan{}, err
	}
	inputs := copyNativeEgressInputs(seed)
	inputs.LocalPrefixes = compactPrefixes(append(inputs.LocalPrefixes, observed...))
	policy, err := prepareEgress(inputs)
	if err != nil {
		return nativeEgressPlan{}, err
	}
	canonical, err := canonicalNFTObjects(policy.expectedObjects())
	if err != nil {
		return nativeEgressPlan{}, err
	}
	return nativeEgressPlan{BootstrapSchema, namespace, seed, observed, inputs, sha256Hex(canonical)}, nil
}

func canonicalNativeEgressInputs(input EgressInputs) (EgressInputs, error) {
	if _, err := prepareEgress(input); err != nil {
		return EgressInputs{}, err
	}
	input = copyNativeEgressInputs(input)
	slices.SortFunc(input.TrustedResolvers, func(a, b netip.Addr) int { return a.Compare(b) })
	input.TrustedResolvers = slices.Compact(input.TrustedResolvers)
	input.LocalPrefixes = compactPrefixes(input.LocalPrefixes)
	return input, nil
}

func canonicalNativeObservedPrefixes(prefixes []netip.Prefix) ([]netip.Prefix, error) {
	if len(prefixes) == 0 || len(prefixes) > 4096 {
		return nil, errors.New("native egress plan requires a bounded complete namespace prefix inventory")
	}
	for _, prefix := range prefixes {
		if !prefix.IsValid() || prefix.Bits() == 0 || prefix != prefix.Masked() || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() {
			return nil, errors.New("native egress plan has an invalid observed prefix")
		}
	}
	return compactPrefixes(slices.Clone(prefixes)), nil
}

func copyNativeEgressInputs(input EgressInputs) EgressInputs {
	input.TrustedResolvers = slices.Clone(input.TrustedResolvers)
	input.LocalPrefixes = slices.Clone(input.LocalPrefixes)
	return input
}

func sameNativeEgressInputs(a, b EgressInputs) bool {
	return a.OwnerSHA256 == b.OwnerSHA256 && slices.Equal(a.TrustedResolvers, b.TrustedResolvers) && slices.Equal(a.LocalPrefixes, b.LocalPrefixes)
}

func (p nativeEgressPlan) validate(namespace namespaceIdentity, seed EgressInputs) error {
	if p.Schema != BootstrapSchema || p.Namespace != namespace {
		return errors.New("native egress plan changed its schema or selected namespace")
	}
	expected, err := newNativeEgressPlan(namespace, seed, p.Observed)
	if err != nil {
		return err
	}
	if !sameNativeEgressInputs(p.Seed, expected.Seed) || !sameNativeEgressInputs(p.Inputs, expected.Inputs) || !slices.Equal(p.Observed, expected.Observed) || p.RulesSHA256 != expected.RulesSHA256 {
		return errors.New("native egress plan differs from the complete selected owner/resolvers/prefixes/rules")
	}
	return nil
}

// validateObserved lets a future fixed-plan apply refuse changed namespace
// inputs before mutation, rather than appending unrecorded prefixes at apply.
func (p nativeEgressPlan) validateObserved(observed []netip.Prefix) error {
	current, err := canonicalNativeObservedPrefixes(observed)
	if err != nil {
		return err
	}
	if !slices.Equal(current, p.Observed) {
		return errors.New("native namespace prefixes changed after durable policy planning")
	}
	return nil
}

func decodeNativeEgressPlan(raw []byte, namespace namespaceIdentity, seed EgressInputs) (nativeEgressPlan, error) {
	if len(raw) == 0 || len(raw) > 3<<20 {
		return nativeEgressPlan{}, errors.New("unbounded native egress plan response")
	}
	if err := checkJSONKeys(raw); err != nil {
		return nativeEgressPlan{}, err
	}
	var plan nativeEgressPlan
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nativeEgressPlan{}, err
	}
	if err := plan.validate(namespace, seed); err != nil {
		return nativeEgressPlan{}, err
	}
	return plan, nil
}

// discoverNativeEgressPlan only reads trusted VM inputs and the independently
// selected target namespace. Its fixed child role never runs nft. The caller
// must retain its existing fresh peer/process/record/namespace verification;
// this result supplies no launch or historical-success authority.
func discoverNativeEgressPlan(ctx context.Context, namespace *os.File, record RuntimeRecord, identity namespaceIdentity) (nativeEgressPlan, error) {
	if namespace == nil || !record.Launch.UnconfinedNetwork {
		return nativeEgressPlan{}, errors.New("native egress planning requires its selected unconfined namespace")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	selected, err := nativeInfrastructureInputs()
	if err != nil {
		return nativeEgressPlan{}, err
	}
	seed := EgressInputs{egressOwner(record), selected.resolvers, selected.prefixes}
	raw, err := json.Marshal(networkRequest{BootstrapSchema, identity, seed})
	if err != nil {
		return nativeEgressPlan{}, err
	}
	cmd := exec.CommandContext(ctx, "/proc/self/exe", NetworkWorkerCommand, "plan")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.ExtraFiles = []*os.File{namespace}
	cmd.Stdin = bytes.NewReader(raw)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	output, stderr := boundedCommandOutput(3<<20), boundedCommandOutput(64<<10)
	cmd.Stdout, cmd.Stderr = output, stderr
	if err := cmd.Run(); err != nil {
		return nativeEgressPlan{}, fmt.Errorf("native egress discovery failed: %w: %s", err, stderr.String())
	}
	return decodeNativeEgressPlan(output.Bytes(), identity, seed)
}
