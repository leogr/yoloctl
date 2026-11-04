package controller

import (
	"bytes"
	"io"
	"testing"
)

func TestImageOverlapMatrixForReservationsAndHolds(t *testing.T) {
	image := func(name string) Resource { return Resource{Kind: ResourceImages, Name: name} }
	domain, x, y := image(""), image("yoloctl/prepared/x"), image("yoloctl/prepared/y")
	for _, state := range []string{"pending", "uncertain"} {
		for _, tc := range []struct {
			name              string
			held, access      Resource
			writing, conflict bool
		}{
			{"domain/named-read", domain, x, false, true},
			{"domain/named-write", domain, x, true, true},
			{"domain/domain-read", domain, domain, false, true},
			{"domain/domain-write", domain, domain, true, true},
			{"named/domain-write", x, domain, true, true},
			{"named/compatibility-read", x, domain, false, false},
			{"named/same-read", x, x, false, true},
			{"named/same-write", x, x, true, true},
			{"named/different-read", x, y, false, false},
			{"named/different-write", x, y, true, false},
			{"named/normalized-alias", x, image("docker.io/yoloctl/prepared/x:latest"), true, true},
			{"other-kind", x, box("x"), true, false},
		} {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				s := storeServer(t)
				seedReceipt(t, s, "image-reservation-12345", state, writes(tc.held), func(r *Receipt) { r.ProofVersion = 2 })
				h, err := s.loadHolds("")
				if err != nil {
					t.Fatal(err)
				}
				scope := uses([]Resource{box("consumer")}, []Resource{tc.access})
				if tc.writing {
					scope = writes(tc.access)
				}
				_, got, _ := h.conflict(scope)
				if (len(got) != 0) != tc.conflict {
					t.Fatalf("conflicts=%v want %v", got, tc.conflict)
				}
			})
		}
	}
}

func TestMixedVersionDocumentProofsSurviveRestart(t *testing.T) {
	for _, version := range []int{1, 2} {
		s := storeServer(t)
		r, d := provenReceipt(t, s, "mixed version fixture")
		r.ProofVersion = version
		r.Proofs = []Proof{s.RecoveryKey.proofVersion(version, r.DiskID, r.ID, r.Verifier, d)}
		if err := s.Receipts.Save(r); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Receipts.Recover(); err != nil {
			t.Fatal(err)
		}
		current, err := s.Receipts.Read(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.publishApplied(current, []DocumentIntent{d}); err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
	}
	key := newRecoveryKey(bytes.Repeat([]byte{7}, recoveryKeySize))
	d := DocumentIntent{Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte("data"), Mode: 0600}
	p := key.proofVersion(1, testDisk, "receipt", VerifierAccountDocument, d)
	if key.matchesVersion(2, testDisk, "receipt", VerifierAccountDocument, p, d) {
		t.Fatal("cross-version HMAC accepted")
	}
}

func TestNamedImagesRequireNewSchema(t *testing.T) {
	s := storeServer(t)
	r := seedReceipt(t, s, "named-image-schema-123", "uncertain", writes(Resource{Kind: ResourceImages, Name: "yoloctl/prepared/job"}), nil)
	if _, err := s.Receipts.Read(r.ID); err == nil {
		t.Fatal("v1 interpreted a named image")
	}
	r.ProofVersion = 2
	if err := s.Receipts.Save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receipts.Read(r.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bad image", "yoloctl/UPPER", "../escape", "x\nimage"} {
		if err := (Resource{Kind: ResourceImages, Name: name}).validate(); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestNamedImageGateEmitsV2OnlyWhenNeeded(t *testing.T) {
	for _, named := range []bool{false, true} {
		var effects []string
		image := Resource{Kind: ResourceImages}
		if named {
			image.Name = "yoloctl/prepared/job"
		}
		s := gatedServer(t, func(Request) EffectIntent { return scopeIntent(writes(image)) }, &effects)
		req := request("box.start")
		if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		r, err := s.Receipts.Read(req.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if named {
			want = 2
		}
		if r.ProofVersion != want {
			t.Fatalf("named=%v version=%d want=%d", named, r.ProofVersion, want)
		}
	}
}
