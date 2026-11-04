package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func draftPrivateWorkerInput() WorkerInput {
	return WorkerInput{Principal: Principal{Box: "box", Incarnation: strings.Repeat("a", 32)}, Request: Request{ID: "11111111-2222-4333-8444-555555555555", Method: "box.show", Space: "fixture", Flags: map[string][]string{"alpha": {"one", "two"}}, Payload: json.RawMessage(`{"Alpha":1,"alpha":2}`)}}
}
func TestWorkerInputExactTypedCanonicalDigestAndEncoderNewline(t *testing.T) {
	work := draftPrivateWorkerInput()
	raw, digest, err := CanonicalWorkerInput(work)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatal("digest algorithm changed")
	}
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(work); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded.Bytes(), append(bytes.Clone(raw), '\n')) {
		t.Fatal("actual parent Encoder differs")
	}
	for _, wire := range [][]byte{raw, encoded.Bytes()} {
		got, hash, err := DecodeWorkerInput(wire)
		if err != nil || hash != digest || got.Request.ID != work.Request.ID || !bytes.Equal(got.Request.Payload, work.Request.Payload) {
			t.Fatal("actual private data changed", hash, err)
		}
	}
	// Arbitrary operation payload/map semantics stay in that operation; there
	// is no blanket folding of its two distinct case-sensitive payload keys.
	work.Request.Flags["Alpha"] = []string{"separate-case-sensitive-map-key"}
	if _, _, err := CanonicalWorkerInput(work); err != nil {
		t.Fatal(err)
	}
}
func TestWorkerInputRejectsBothOrderTypedAliasesAndErasure(t *testing.T) {
	work := draftPrivateWorkerInput()
	raw, _, _ := CanonicalWorkerInput(work)
	principal, _ := json.Marshal(work.Principal)
	request, _ := json.Marshal(work.Request)
	var bad [][]byte
	for _, entry := range []struct {
		key   string
		value []byte
	}{{"principal", principal}, {"request", request}} {
		field := []byte(`"` + entry.key + `":` + string(entry.value))
		alias := strings.ToUpper(entry.key[:1]) + entry.key[1:]
		for _, replacement := range []string{string(field) + `,` + string(field), string(field) + `,"` + alias + `":` + string(entry.value), `"` + alias + `":` + string(entry.value) + `,` + string(field), string(field) + `,"` + entry.key + `":null`} {
			bad = append(bad, bytes.Replace(raw, field, []byte(replacement), 1))
		}
	}
	id := []byte(`"method":"box.show"`)
	for _, member := range []string{string(id) + `,"Method":"box.show"`, `"Method":"box.show",` + string(id), string(id) + `,` + string(id)} {
		bad = append(bad, bytes.Replace(raw, id, []byte(member), 1))
	}
	for _, wire := range bad {
		got, digest, err := DecodeWorkerInput(wire)
		if err == nil || digest != "" || got.Request.ID != "" {
			t.Fatal("ambiguous envelope yielded routing data")
		}
	}
}
func TestWorkerInputLimitsClosedTypesWhitespaceAndNoSecretErrors(t *testing.T) {
	work := draftPrivateWorkerInput()
	work.Request.Payload = json.RawMessage(`{"secret":"do-not-print-fixture"}`)
	raw, _, _ := CanonicalWorkerInput(work)
	bad := [][]byte{nil, []byte("null"), []byte(`{}`), []byte(`[]`), append(bytes.Clone(raw), '\n', '\n'), append([]byte(" "), raw...), append(bytes.Clone(raw), []byte(" {}")...), bytes.Replace(raw, []byte(`"principal":`), []byte(`"_not_worker_field":`), 1), bytes.Replace(raw, []byte(`"method":"box.show"`), []byte(`"method":17`), 1), bytes.Repeat([]byte("x"), MaxFrame+1)}
	for _, wire := range bad {
		got, digest, err := DecodeWorkerInput(wire)
		if err == nil || digest != "" || got.Request.ID != "" {
			t.Fatal("invalid bounded input accepted")
		}
		if strings.Contains(err.Error(), "do-not-print-fixture") {
			t.Fatal("secret input printed in error")
		}
	}
	work.Request.Cwd = strings.Repeat("x", MaxFrame)
	if raw, digest, err := CanonicalWorkerInput(work); err == nil || raw != nil || digest != "" {
		t.Fatal("oversized private producer data accepted")
	}
	work.Request.Cwd = ""
	work.Request.Payload = json.RawMessage(`not-json`)
	if _, _, err := CanonicalWorkerInput(work); err == nil {
		t.Fatal("invalid RawMessage producer accepted")
	}
}
