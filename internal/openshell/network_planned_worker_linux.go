package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

func nativeNetworkWorkerMode(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil // Historical private apply path, until caller migration.
	}
	if len(args) == 1 {
		switch args[0] {
		case "plan", "apply", "retire":
			return args[0], nil
		}
	}
	return "", errors.New("unsupported native network child mode")
}

// decodePlannedNetworkRequest checks only the private request's structure and
// internal plan consistency. The parent must independently select the plan
// from protected storage and provide the exact authorized namespace descriptor.
// Neither this JSON nor a successful child reply supplies that authority.
func decodePlannedNetworkRequest(raw []byte) (nativeEgressPlan, error) {
	if len(raw) == 0 || len(raw) > 3<<20 {
		return nativeEgressPlan{}, errors.New("missing or unbounded planned network request")
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
	if err := plan.validate(plan.Namespace, plan.Seed); err != nil {
		return nativeEgressPlan{}, err
	}
	return plan, nil
}

// plannedNetworkWorker is a fixed root-only one-shot infrastructure role. It
// is not yet called by activation/lifecycle: durable pin/origin/plan, fresh
// activation authority or acknowledged Stop/inactivity must precede that wiring.
// The child never reads workload environment, chooses a PID or accepts argv
// for nft. On failure the parent's future invocation must retain uncertainty
// about effects; it cannot infer a pre-effect outcome from stderr/exit status.
func plannedNetworkWorker(ctx context.Context, mode string) error {
	if mode != "apply" && mode != "retire" {
		return errors.New("invalid planned network child effect")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (3<<20)+1))
	if err != nil {
		return err
	}
	plan, err := decodePlannedNetworkRequest(raw)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	nft, err := enterNetworkWorkerNamespace(plan.Namespace)
	if err != nil {
		return err
	}
	defer func() { _ = nft.Close() }()
	execute := func(ctx context.Context, args []string, input io.Reader) ([]byte, error) {
		return runNativeNFT(ctx, nft, args, input)
	}
	if mode == "retire" {
		result, err := retirePlannedNativeEgress(ctx, plan, plan.Namespace, execute)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	observed, err := discoverLocalPrefixes()
	if err != nil {
		return err
	}
	result, err := applyPlannedNativeEgress(ctx, plan, plan.Namespace, observed, execute)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
