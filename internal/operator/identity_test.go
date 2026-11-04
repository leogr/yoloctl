package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func taggedNativeIdentity(e Endpoint) RegistrationIdentity {
	return RegistrationIdentity{Endpoint: e, Mode: RegistrationOpenShellNative, HelperProtocol: 2, Qualification: strings.Repeat("b", 64)}
}

func TestRegistrationIdentityPreservesLegacyBytesAndExplicitNativeTag(t *testing.T) {
	e := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	legacy, err := json.Marshal(LegacyRegistrationIdentity(e))
	old, oldErr := json.Marshal(e)
	if err != nil || oldErr != nil || !bytes.Equal(legacy, old) {
		t.Fatal("legacy producer bytes changed", err, oldErr)
	}
	for _, want := range []RegistrationIdentity{LegacyRegistrationIdentity(e), taggedNativeIdentity(e)} {
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRegistrationIdentity(raw)
		if err != nil || got != want {
			t.Fatal("tagged identity lost fields", got, err)
		}
		var viaJSON RegistrationIdentity
		if err := json.Unmarshal(raw, &viaJSON); err != nil || viaJSON != want {
			t.Fatal("JSON reader bypassed strict tagged decoder", viaJSON, err)
		}
	}
	var zero RegistrationIdentity
	if zero.Validate() == nil {
		t.Fatal("missing identity defaulted to legacy")
	}
	if _, err := json.Marshal(zero); err == nil {
		t.Fatal("unclassified identity was publishable")
	}
	nativeRaw, _ := json.Marshal(taggedNativeIdentity(e))
	if _, err := decodeRegistrationIdentity(nativeRaw); !errors.Is(err, ErrNativeRegistration) {
		t.Fatal("legacy decoder erased native tag", err)
	}
	legacyDecoded, err := decodeRegistrationIdentity(legacy)
	if err != nil || legacyDecoded != e {
		t.Fatal("legacy private reader rejected old shape", err)
	}
}

func TestRegistrationIdentityRejectsMalformedMixedAndAliasedSchemas(t *testing.T) {
	id := strings.Repeat("a", 32)
	qual := strings.Repeat("b", 64)
	old := `{"box":"box","incarnation":"` + id + `"}`
	native := `{"box":"box","incarnation":"` + id + `","mode":"openshell-native","helper_protocol":2,"qualification":"` + qual + `"}`
	bad := map[string]string{
		"empty": "", "null-object": "null", "array": "[]", "missing": `{"box":"box"}`,
		"duplicate":         strings.Replace(old, `"box":"box"`, `"box":"box","box":"other"`, 1),
		"escaped-duplicate": strings.Replace(old, `"box":"box"`, `"box":"box","b\u006fx":"other"`, 1),
		"folded":            strings.Replace(old, `"box":`, `"Box":`, 1),
		"unknown":           strings.TrimSuffix(old, "}") + `,"other":true}`,
		"unknown-native":    strings.TrimSuffix(native, "}") + `,"other":true}`,
		"trailing":          old + `{}`, "oversize": strings.Replace(old, "{", "{"+strings.Repeat(" ", 4097), 1),
		"mode-only":             strings.TrimSuffix(old, "}") + `,"mode":"openshell-native"}`,
		"protocol-only":         strings.TrimSuffix(old, "}") + `,"helper_protocol":2}`,
		"qualification-only":    strings.TrimSuffix(old, "}") + `,"qualification":"` + qual + `"}`,
		"legacy-mode":           strings.Replace(native, "openshell-native", "legacy", 1),
		"unknown-mode":          strings.Replace(native, "openshell-native", "other", 1),
		"folded-mode":           strings.Replace(native, `"mode":`, `"Mode":`, 1),
		"folded-protocol":       strings.Replace(native, `"helper_protocol":`, `"Helper_Protocol":`, 1),
		"folded-qualification":  strings.Replace(native, `"qualification":`, `"Qualification":`, 1),
		"missing-qualification": strings.Replace(native, `,"qualification":"`+qual+`"`, "", 1),
		"null-box":              strings.Replace(old, `"box":"box"`, `"box":null`, 1),
		"null-incarnation":      strings.Replace(old, `"incarnation":"`+id+`"`, `"incarnation":null`, 1),
		"null-mode":             strings.Replace(native, `"mode":"openshell-native"`, `"mode":null`, 1),
		"null-qualification":    strings.Replace(native, `"qualification":"`+qual+`"`, `"qualification":null`, 1),
		"upper-qualification":   strings.Replace(native, qual, strings.Repeat("B", 64), 1),
		"short-qualification":   strings.Replace(native, qual, strings.Repeat("b", 63), 1),
		"bad-box":               strings.Replace(old, `"box":"box"`, `"box":"bad/name"`, 1),
		"bad-incarnation":       strings.Replace(old, id, "bad", 1),
		"duplicate-mode":        strings.Replace(native, `"mode":"openshell-native"`, `"mode":"openshell-native","mode":"openshell-native"`, 1),
	}
	for _, value := range []string{"0", "1", "3", "2.0", "2e0", `"2"`, "null", "true", "{}", "[]"} {
		bad["protocol-"+value] = strings.Replace(native, `"helper_protocol":2`, `"helper_protocol":`+value, 1)
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRegistrationIdentity([]byte(raw)); err == nil {
				t.Fatal("bad identity accepted")
			}
			oldValue := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: id})
			got := oldValue
			if err := json.Unmarshal([]byte(raw), &got); err == nil || got != oldValue {
				t.Fatal("strict JSON failure replaced known variant", err)
			}
		})
	}
}

func TestRegistrationIdentityHistoricalQualificationRequiresExactOrigin(t *testing.T) {
	i := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
	// An arbitrary valid historical release is compared directly. There is no
	// lookup of current helper source or current build in this metadata method.
	if err := i.MatchesNativeOrigin(i.Endpoint, 2, i.Qualification); err != nil {
		t.Fatal("historical matching origin rejected", err)
	}
	for _, origin := range []struct {
		protocol      int
		qualification string
	}{
		{1, i.Qualification}, {2, strings.Repeat("c", 64)}, {2, ""}, {2, strings.Repeat("B", 64)},
	} {
		if err := i.MatchesNativeOrigin(i.Endpoint, origin.protocol, origin.qualification); err == nil {
			t.Fatal("foreign/incomplete origin accepted")
		}
	}
	for _, endpoint := range []Endpoint{{Box: "other", Incarnation: i.Incarnation}, {Box: i.Box, Incarnation: strings.Repeat("c", 32)}} {
		if err := i.MatchesNativeOrigin(endpoint, 2, i.Qualification); err == nil {
			t.Fatal("native identity adopted another origin box/incarnation")
		}
	}
	if err := LegacyRegistrationIdentity(i.Endpoint).MatchesNativeOrigin(i.Endpoint, 2, i.Qualification); err == nil {
		t.Fatal("legacy identity became native through origin comparison")
	}
}
