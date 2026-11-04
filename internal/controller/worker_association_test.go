package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// Metadata fixture only: never Configure/LoadRuntime/a source constructor.
func workerAssociationMetadataFixture(t *testing.T) (WorkerControllerAssociation, Receipt) {
	t.Helper()
	h := hello()
	c := WorkerControllerAssociation{Version: 1, Hello: h, DiskID: testDisk, SourceAssociation: "fixture-correlation-not-authority", Singleton: WorkerSingletonAssociation{Descriptor: 9, Device: 1, Inode: 2, Mount: 3}, Context: spaceenv.Context{Schema: 1, Space: h.Space, DiskID: testDisk, DiskUUID: "fixture-uuid", DiskMount: "/fixture-native", ConfigDir: "/fixture-native/config", CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: "/fixture-host", UID: 1000, GID: 1000, Bundle: h.Bundle, Generation: h.Generation, Mounts: []spaceenv.Mount{{Source: "/workdir", Writable: true}}}}
	r, err := (Receipts{t.TempDir()}).begin(request("test.worker-association"), Principal{}, h.Generation, testDisk, true)
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}
func TestWorkerAssociationAllowsWaitingNilScopeAndFreezesInitialProjection(t *testing.T) {
	c, r := workerAssociationMetadataFixture(t)
	if r.EffectPhase != EffectWaiting || r.Scope != nil {
		t.Fatal("fixture not actual pre-Begin receipt")
	}
	digest, raw, err := projectWorkerAssociationV1(c, r)
	if err != nil || len(digest) != 64 {
		t.Fatal(err)
	}
	saved := bytes.Clone(raw)
	// Scope legitimately advances after worker Start. Existing frozen digest is
	// retained; it is NOT recomputed from this later mutable scope/phase.
	r.EffectPhase = EffectStarted
	r.Scope = &Scope{Writes: []Resource{{Kind: ResourceBox, Name: "box"}}}
	c.Context.Mounts[0].Source = "/later"
	if !bytes.Equal(raw, saved) {
		t.Fatal("frozen projection aliased mutable fields")
	}
	sum := sha256.Sum256(raw)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatal("wrong frozen digest")
	}
	if _, _, err := projectWorkerAssociationV1(c, r); err == nil {
		t.Fatal("post-Begin metadata used as fresh bootstrap")
	}
}
func TestWorkerAssociationBindsOriginalControllerAndReceiptFields(t *testing.T) {
	for _, change := range []string{"version", "context-generation", "context-disk", "hello", "singleton", "empty-source", "started", "phase", "principal"} {
		t.Run(change, func(t *testing.T) {
			c, r := workerAssociationMetadataFixture(t)
			original, _, err := projectWorkerAssociationV1(c, r)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "version":
				c.Version = 2
			case "context-generation":
				c.Context.Generation = "different"
			case "context-disk":
				c.Context.DiskID = strings.Repeat("a", 32)
			case "hello":
				c.Hello.Bundle = "different"
			case "singleton":
				c.Singleton.Inode = 0
			case "empty-source":
				c.SourceAssociation = ""
			case "started":
				r.Started = r.Started.Add(1)
			case "phase":
				r.EffectPhase = EffectStarted
			case "principal":
				r.Principal = Principal{Box: "other", Incarnation: "actor"}
			}
			got, _, err := projectWorkerAssociationV1(c, r)
			if change == "started" || change == "principal" {
				if err != nil || got == original {
					t.Fatal("recorded initial association not bound", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid/mismatching metadata accepted")
			}
		})
	}
}
func TestWorkerInputDigestUsesFinalTypedSlotsAndSeparateAssociation(t *testing.T) {
	for _, view := range []bool{false, true} {
		for _, phase := range []bool{false, true} {
			work := WorkerInput{Principal: Principal{}, Request: request("test.worker-association"), WorkerAssociationDigest: strings.Repeat("a", 64)}
			work.Request.View = view
			next := 5
			if view {
				next++
			}
			if phase {
				work.NativePhaseFD = next
				work.NativePhasePipe = &NativePhasePipeID{Device: 1, Inode: 2}
				next++
			}
			work.PIDNamespaceFD = next
			work.PIDNamespaceBootstrapFD = next + 1
			raw, digest, err := CanonicalWorkerInput(work)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(work)
			if err != nil || !bytes.Equal(raw, expected) {
				t.Fatal("not canonical typed input", err)
			}
			sum := sha256.Sum256(expected)
			if digest != hex.EncodeToString(sum[:]) {
				t.Fatal("wrong separate input digest")
			}
			if bytes.Contains(raw, []byte("input_digest")) {
				t.Fatal("self-referential input digest")
			}
			changed := work
			changed.PIDNamespaceBootstrapFD++
			_, other, err := CanonicalWorkerInput(changed)
			if err != nil || digest == other {
				t.Fatal("final slots not bound", err)
			}
		}
	}
}
