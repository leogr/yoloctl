package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/operator"
)

// The scanner reads a legacy registration as before, a native one as native
// (never as legacy authority), and still refuses a mismatched or malformed
// identity.
func TestScannedOperatorRegistrationRoutesByMode(t *testing.T) {
	incarnation := strings.Repeat("a", 32)
	endpoint := operator.Endpoint{Box: "box", Incarnation: incarnation}
	identity := func(i operator.RegistrationIdentity) []byte {
		t.Helper()
		raw, err := json.Marshal(i)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	legacy := identity(operator.LegacyRegistrationIdentity(endpoint))
	native := identity(operator.RegistrationIdentity{Endpoint: endpoint, Mode: operator.RegistrationOpenShellNative,
		HelperProtocol: operator.NativeRegistrationHelperProtocol, Qualification: strings.Repeat("d", 64)})

	got, err := scannedOperatorRegistration(incarnation, legacy)
	if err != nil || got != operator.LegacyRegistrationIdentity(endpoint) {
		t.Fatalf("legacy: %+v err=%v", got, err)
	}
	got, err = scannedOperatorRegistration(incarnation, native)
	if err != nil || got.Mode != operator.RegistrationOpenShellNative || got.Endpoint != endpoint {
		t.Fatalf("native: %+v err=%v", got, err)
	}
	for name, tc := range map[string]struct {
		dir string
		raw []byte
	}{
		"legacy in another directory": {strings.Repeat("b", 32), legacy},
		"native in another directory": {strings.Repeat("b", 32), native},
		"malformed":                   {incarnation, []byte(`{"box":"box"`)},
		"unknown key":                 {incarnation, []byte(`{"box":"box","incarnation":"` + incarnation + `","extra":"x"}`)},
		"partial native shape":        {incarnation, []byte(`{"box":"box","incarnation":"` + incarnation + `","mode":"openshell-native"}`)},
	} {
		if _, err := scannedOperatorRegistration(tc.dir, tc.raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
