package openshell

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/leogr/yoloctl/internal/safepath"
)

type activationProof struct {
	Schema       int               `json:"schema"`
	RecordSHA256 string            `json:"record_sha256"`
	Namespace    namespaceIdentity `json:"namespace"`
	Mode         string            `json:"mode"`
	Inputs       *EgressInputs     `json:"inputs"`
	RulesSHA256  string            `json:"rules_sha256"`
}

func recordIdentity(record RuntimeRecord) string {
	raw, _ := json.Marshal(record)
	return sha256Hex(raw)
}

func egressOwner(record RuntimeRecord) string {
	return sha256Hex([]byte("yoloctl-openshell-egress-v2\x00" + recordIdentity(record)))
}

// checkedActivationProof is constructed only after the native rule readback
// has been checked. The PID1 helper verifies the authenticated native result;
// it never requires a custom base to contain nft or its dynamic libraries.
func checkedActivationProof(record RuntimeRecord, namespace namespaceIdentity, inputs *EgressInputs, readback []byte) (activationProof, error) {
	proof := activationProof{Schema: BootstrapSchema, RecordSHA256: recordIdentity(record), Namespace: namespace, Mode: "confined"}
	if record.Launch.UnconfinedNetwork {
		if inputs == nil {
			return activationProof{}, errors.New("unconfined activation lacks selected infrastructure inputs")
		}
		if err := VerifyEgressPolicyJSON(readback, *inputs); err != nil {
			return activationProof{}, err
		}
		plan, err := prepareEgress(*inputs)
		if err != nil {
			return activationProof{}, err
		}
		canonical, err := canonicalNFTObjects(plan.expectedObjects())
		if err != nil {
			return activationProof{}, err
		}
		proof.Mode = "unconfined"
		proof.Inputs = inputs
		proof.RulesSHA256 = sha256Hex(canonical)
	} else if inputs != nil || len(readback) != 0 {
		return activationProof{}, errors.New("confined activation supplied an unconfined ruleset")
	}
	return proof, proof.validate(record, namespace)
}

func (p activationProof) validate(record RuntimeRecord, namespace namespaceIdentity) error {
	if p.Schema != BootstrapSchema || p.RecordSHA256 != recordIdentity(record) || p.Namespace != namespace || namespace.Inode == 0 {
		return errors.New("activation proof belongs to another runtime or namespace")
	}
	if !record.Launch.UnconfinedNetwork {
		if p.Mode != "confined" || p.Inputs != nil || p.RulesSHA256 != "" {
			return errors.New("confined activation proof contains an unconfined policy")
		}
		return nil
	}
	if p.Mode != "unconfined" || p.Inputs == nil || p.Inputs.OwnerSHA256 != egressOwner(record) {
		return errors.New("unconfined activation proof lacks this runtime's exact owned table")
	}
	plan, err := prepareEgress(*p.Inputs)
	if err != nil {
		return err
	}
	canonical, err := canonicalNFTObjects(plan.expectedObjects())
	if err != nil {
		return err
	}
	if sha256Hex(canonical) != p.RulesSHA256 {
		return errors.New("activation proof does not identify the complete selected egress table")
	}
	return nil
}

func loadActivationKey(generationRoot string, record RuntimeRecord) (activationKey, error) {
	var key activationKey
	raw, info, err := safepath.ReadFileInfo(generationRoot, "status/activation.key", 32)
	if err != nil {
		return key, err
	}
	if err := privateFileOwner(info, 0, 0); err != nil {
		return key, err
	}
	if len(raw) != len(key) || sha256Hex(raw) != record.Files["status/activation.key"] {
		return key, errors.New("native activation authenticator does not match the selected generation")
	}
	copy(key[:], raw)
	return key, nil
}

func loadActivationProof(generationRoot string, record RuntimeRecord, namespace namespaceIdentity) (activationProof, []byte, error) {
	raw, info, err := safepath.ReadFileInfo(generationRoot, "status/activation.json", 1<<20)
	if err != nil {
		return activationProof{}, nil, err
	}
	if err := privateFileOwner(info, 0, 0); err != nil {
		return activationProof{}, nil, err
	}
	if err := checkJSONKeys(raw); err != nil {
		return activationProof{}, nil, err
	}
	var proof activationProof
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return activationProof{}, nil, err
	}
	if err := proof.validate(record, namespace); err != nil {
		return activationProof{}, nil, err
	}
	return proof, raw, nil
}
