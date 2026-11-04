package openshell

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Only metadata codec fixtures: these literal namespace numbers/boot text do
// not stand for a current kernel, native disk, admitted worker or receipt.
func precreateMetadataFixture(t *testing.T) (precreateOriginClaim, precreateCurrentKernel, precreateMetadata, []byte) {
	t.Helper()
	r := ownedReservationFixture(t)
	c, err := r.precreateClaim()
	if err != nil {
		t.Fatal(err)
	}
	k := precreateCurrentKernel{"controller-generation", "11111111-2222-4333-8444-555555555555", namespaceIdentity{123, 456}, namespaceIdentity{123, 789}}
	w := newPrecreateMetadata(c, k)
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return c, k, w, raw
}

func TestPrecreateMetadataCanonicalPrivateRoundTripAndIndependentCopy(t *testing.T) {
	c, k, _, raw := precreateMetadataFixture(t)
	w, err := decodePrecreateMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		raw[i] = 0
	}
	got, err := w.claim(c.origin, k)
	if err != nil || got != c {
		t.Fatal("private metadata round trip changed original scope", err)
	}
	if bytes.Contains(raw, []byte("controller-generation")) {
		t.Fatal("fixture did not discard original payload")
	}
}

func TestPrecreateMetadataRejectsAmbiguousNoncanonicalPrivateEnvelopes(t *testing.T) {
	_, _, _, raw := precreateMetadataFixture(t)
	variants := map[string][]byte{
		"empty": nil, "oversized": bytes.Repeat([]byte("x"), precreateMetadataLimit+1),
		"leading-space": append([]byte(" "), raw...), "trailing-space": append(append([]byte(nil), raw...), ' '),
		"trailing-json":          append(append([]byte(nil), raw...), []byte("{}")...),
		"unknown":                bytes.Replace(raw, []byte(`{"schema":1,`), []byte(`{"unexpected":1,"schema":1,`), 1),
		"duplicate":              bytes.Replace(raw, []byte(`{"schema":1,`), []byte(`{"schema":1,"schema":1,`), 1),
		"alias":                  bytes.Replace(raw, []byte(`"schema"`), []byte(`"SCHEMA"`), 1),
		"duplicate-alias":        bytes.Replace(raw, []byte(`{"schema":1,`), []byte(`{"Schema":2,"schema":1,`), 1),
		"nested-alias":           bytes.Replace(raw, []byte(`"box":"box"`), []byte(`"Box":"box"`), 1),
		"nested-duplicate-alias": bytes.Replace(raw, []byte(`"box":"box"`), []byte(`"Box":"other","box":"box"`), 1),
		"null-kernel":            bytes.Replace(raw, []byte(`"mount_namespace":{"device":123,"inode":456}`), []byte(`"mount_namespace":null`), 1),
		"missing-controller":     bytes.Replace(raw, []byte(`"controller_generation":"controller-generation",`), nil, 1),
		"negative-inode":         bytes.Replace(raw, []byte(`"inode":456`), []byte(`"inode":-1`), 1),
		"overflow-inode":         bytes.Replace(raw, []byte(`"inode":456`), []byte(`"inode":18446744073709551616`), 1),
	}
	for name, data := range variants {
		t.Run(name, func(t *testing.T) {
			if name != "empty" && name != "oversized" && bytes.Equal(data, raw) {
				t.Fatal("bad-envelope fixture did not change exact payload")
			}
			if got, err := decodePrecreateMetadata(data); err == nil || got != (precreateMetadata{}) {
				t.Fatal("ambiguous private precreate envelope accepted")
			}
		})
	}
}

func TestPrecreateMetadataRequiresIndependentCurrentKernelControllerAndScope(t *testing.T) {
	c, current, w, _ := precreateMetadataFixture(t)
	for _, change := range []string{"schema", "controller", "empty-controller", "invalid-controller", "boot", "invalid-current-boot", "mount", "network", "empty-current-mount", "empty-current-network", "box", "disk", "incarnation", "digest", "upper-digest"} {
		t.Run(change, func(t *testing.T) {
			wire := w
			k := current
			expected := c.origin
			switch change {
			case "schema":
				wire.Schema = 2
			case "controller":
				wire.ControllerGeneration = "other"
			case "empty-controller":
				wire.ControllerGeneration = ""
				k.generation = ""
			case "invalid-controller":
				wire.ControllerGeneration = "controller\n"
				k.generation = wire.ControllerGeneration
			case "boot":
				wire.BootID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			case "invalid-current-boot":
				wire.BootID = "not-a-kernel-uuid"
				k.boot = wire.BootID
			case "mount":
				wire.MountNamespace.Inode++
			case "network":
				wire.NetworkNamespace.Inode++
			case "empty-current-mount":
				wire.MountNamespace = namespaceIdentity{}
				k.mount = namespaceIdentity{}
			case "empty-current-network":
				wire.NetworkNamespace = namespaceIdentity{}
				k.network = namespaceIdentity{}
			case "box":
				expected.Box = "other"
			case "disk":
				expected.DiskUUID = "other"
			case "incarnation":
				wire.Origin.Incarnation = "../foreign"
			case "digest":
				wire.OriginSHA256 = strings.Repeat("f", 64)
			case "upper-digest":
				wire.OriginSHA256 = strings.ToUpper(wire.OriginSHA256)
			}
			if got, err := wire.claim(expected, k); err == nil || got != (precreateOriginClaim{}) {
				t.Fatal("wrong current precreate kernel/controller/scope accepted")
			}
		})
	}
}

func TestNativePrecreateReadRequiresUnexpiredOriginalNativeBoundary(t *testing.T) {
	ctx := t.Context()
	uses := 0
	use := func(*NativePrecreateRead) error { uses++; return nil }
	if err := WithNativePrecreateObservation(ctx, []byte("{}"), "box", QualifiedRelease{}, nil, use); err == nil || uses != 0 {
		t.Fatal("metadata supplied native permission without actual storage callback")
	}
	var r *NativeReservation
	if raw, err := r.PrecreateMetadata(ctx, QualifiedRelease{}, nil); err == nil || raw != nil {
		t.Fatal("nil reservation/current storage callback minted metadata")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	checks := 0
	current := func(context.Context) error { checks++; return nil }
	if err := WithNativePrecreateObservation(canceled, []byte("{}"), "box", QualifiedRelease{}, current, use); err == nil || uses != 0 || checks != 0 {
		t.Fatal("canceled or ordinary boundary invoked native callbacks")
	}
	if raw, err := r.PrecreateMetadata(canceled, QualifiedRelease{}, current); err == nil || raw != nil || checks != 0 {
		t.Fatal("canceled or ordinary creator minted metadata")
	}
	for _, s := range []*NativePrecreateRead{nil, {}, {active: false}} {
		if err := s.Check(ctx); err == nil {
			t.Fatal("expired/zero native read scope accepted")
		}
		if got, err := s.Selection(ctx); err == nil || got != (NativePrecreateSelection{}) {
			t.Fatal("expired/zero native scope yielded a current selection")
		}
	}
}
