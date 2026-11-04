package operator

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

// RegistrationMode is an explicit routing discriminator, never authorization.
// The zero value is deliberately invalid; missing evidence cannot mean legacy.
type RegistrationMode uint8

const (
	RegistrationLegacy RegistrationMode = iota + 1
	RegistrationOpenShellNative
)

const NativeRegistrationMode = "openshell-native"
const NativeRegistrationHelperProtocol = 2

// ErrNativeRegistration prevents generic legacy services from silently
// downgrading a native registration. No identity alone permits publication,
// request handling or cleanup.
var ErrNativeRegistration = errors.New("native operator registration requires its native owner")

// RegistrationIdentity preserves the complete legacy/native discriminator.
// Qualification names the independently protected origin's qualified release,
// not the current controller build or a helper executable digest.
type RegistrationIdentity struct {
	Endpoint
	Mode           RegistrationMode
	HelperProtocol int
	Qualification  string
}

func LegacyRegistrationIdentity(e Endpoint) RegistrationIdentity {
	return RegistrationIdentity{Endpoint: e, Mode: RegistrationLegacy}
}

func (i RegistrationIdentity) Validate() error {
	if err := i.Endpoint.Validate(); err != nil {
		return err
	}
	switch i.Mode {
	case RegistrationLegacy:
		if i.HelperProtocol != 0 || i.Qualification != "" {
			return errors.New("legacy operator identity has native fields")
		}
	case RegistrationOpenShellNative:
		if raw, err := hex.DecodeString(i.Incarnation); err != nil || hex.EncodeToString(raw) != i.Incarnation {
			return errors.New("native operator identity requires a canonical incarnation")
		}
		if i.HelperProtocol != NativeRegistrationHelperProtocol || !registrationQualification(i.Qualification) {
			return errors.New("native operator identity has invalid protocol or qualification")
		}
	default:
		return errors.New("operator identity has no supported explicit mode")
	}
	return nil
}

func registrationQualification(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// MatchesNativeOrigin compares recorded metadata with independently verified
// protected origin evidence. The caller must separately verify helper identity,
// historical compatibility, storage, runtime projection and current authority.
// This method intentionally does not require the current helper source/build.
func (i RegistrationIdentity) MatchesNativeOrigin(endpoint Endpoint, protocol int, qualification string) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if i.Mode != RegistrationOpenShellNative || endpoint != i.Endpoint || protocol != i.HelperProtocol || !registrationQualification(qualification) || qualification != i.Qualification {
		return errors.New("operator identity does not match its protected native origin")
	}
	return nil
}

// MarshalJSON keeps the existing legacy producer's exact Endpoint bytes.
// Native publication still needs the separate admitted parent/receipt protocol.
func (i RegistrationIdentity) MarshalJSON() ([]byte, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	if i.Mode == RegistrationLegacy {
		return json.Marshal(i.Endpoint)
	}
	return json.Marshal(struct {
		Box            string `json:"box"`
		Incarnation    string `json:"incarnation"`
		Mode           string `json:"mode"`
		HelperProtocol int    `json:"helper_protocol"`
		Qualification  string `json:"qualification"`
	}{i.Box, i.Incarnation, NativeRegistrationMode, i.HelperProtocol, i.Qualification})
}

// DecodeRegistrationIdentity accepts exactly the old two-key shape or the
// native five-key shape. It rejects aliases, duplicate decoded keys, null,
// partial/mixed/unknown shapes and non-integral protocol encodings.
func DecodeRegistrationIdentity(raw []byte) (RegistrationIdentity, error) {
	var i RegistrationIdentity
	if len(raw) == 0 || len(raw) > 4096 {
		return i, errors.New("operator identity is missing or oversized")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return i, errors.New("invalid operator identity object")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return RegistrationIdentity{}, errors.New("invalid operator identity key")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return RegistrationIdentity{}, errors.New("ambiguous operator identity keys")
		}
		switch key {
		case "box", "incarnation", "mode", "helper_protocol", "qualification":
		default:
			return RegistrationIdentity{}, errors.New("unknown operator identity key")
		}
		seen[key] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return RegistrationIdentity{}, errors.New("invalid operator identity value")
		}
		if key == "helper_protocol" {
			// The protocol is an integer JSON field, not a string or float.
			if !bytes.Equal(bytes.TrimSpace(value), []byte("2")) {
				return RegistrationIdentity{}, errors.New("unsupported operator helper protocol")
			}
			i.HelperProtocol = NativeRegistrationHelperProtocol
			continue
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return RegistrationIdentity{}, errors.New("invalid operator identity string")
		}
		switch key {
		case "box":
			i.Box = text
		case "incarnation":
			i.Incarnation = text
		case "mode":
			if text != NativeRegistrationMode {
				return RegistrationIdentity{}, errors.New("unsupported operator identity mode")
			}
		case "qualification":
			i.Qualification = text
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || !seen["box"] || !seen["incarnation"] {
		return RegistrationIdentity{}, errors.New("incomplete operator identity object")
	}
	if _, err := d.Token(); err != io.EOF {
		return RegistrationIdentity{}, errors.New("trailing operator identity data")
	}
	switch {
	case len(seen) == 2:
		i.Mode = RegistrationLegacy
	case len(seen) == 5 && seen["mode"] && seen["helper_protocol"] && seen["qualification"]:
		i.Mode = RegistrationOpenShellNative
	default:
		return RegistrationIdentity{}, errors.New("partial or mixed operator identity shape")
	}
	if err := i.Validate(); err != nil {
		return RegistrationIdentity{}, err
	}
	return i, nil
}

// UnmarshalJSON uses the same strict decoder as filesystem readers. A failed
// decode leaves the previous value unchanged instead of selecting legacy.
func (i *RegistrationIdentity) UnmarshalJSON(raw []byte) error {
	if i == nil {
		return errors.New("operator identity destination is unavailable")
	}
	decoded, err := DecodeRegistrationIdentity(raw)
	if err != nil {
		return err
	}
	*i = decoded
	return nil
}

// Old generic readers must not silently ignore the native fields. Only the
// full tagged decoder can select that route; an Endpoint alone is legacy.
func (e *Endpoint) UnmarshalJSON(raw []byte) error {
	if e == nil {
		return errors.New("operator endpoint destination is unavailable")
	}
	i, err := DecodeRegistrationIdentity(raw)
	if err != nil {
		return err
	}
	if i.Mode != RegistrationLegacy {
		return ErrNativeRegistration
	}
	*e = i.Endpoint
	return nil
}

// Legacy-private compatibility reader. Generic callers may not erase the tag.
func decodeRegistrationIdentity(raw []byte) (Endpoint, error) {
	i, err := DecodeRegistrationIdentity(raw)
	if err != nil {
		return Endpoint{}, err
	}
	if i.Mode != RegistrationLegacy {
		return Endpoint{}, ErrNativeRegistration
	}
	return i.Endpoint, nil
}
