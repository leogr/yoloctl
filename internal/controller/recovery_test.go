package controller

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDisk = "0123456789abcdef0123456789abcdef"

func evidenceServer(t *testing.T, method string, gated bool) *Server {
	t.Helper()
	key, err := LoadRecoveryKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, DiskID: testDisk, RecoveryKey: key,
		Operations: map[string]Operation{method: {Mutates: true, EffectGated: gated}},
		Execute:    RunWorker,
	}
}

func TestRecoveryEvidenceThroughRealWorker(t *testing.T) {
	const secret = "fixture-secret-value-never-persisted"
	for _, tc := range []struct {
		action, state, disposition string
		gated                      bool
		status                     int
	}{
		{"complete-kill", "recovered", DispositionRecordedCompletion, true, 3},
		{"complete-ungated", "recovered", DispositionRecordedCompletion, false, 3},
		{"complete-uncertain", "uncertain", "", true, 0},
		{"complete-uncertain-hold", "uncertain", "", true, 0},
		{"proof-kill", "uncertain", "", true, 0},
		{"expand-kill", "uncertain", "", true, 0},
		{"proof-refused", "complete", "", true, 0},
	} {
		t.Run(tc.action, func(t *testing.T) {
			s := evidenceServer(t, "test.evidence", tc.gated)
			req := request("test.evidence")
			req.Args = []string{tc.action, secret}
			_ = invoke(t, s, req, nil, io.Discard, io.Discard)
			r, err := s.Receipts.Read(req.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.State != tc.state || r.ProofVersion != documentProofVersion || r.DiskID != testDisk || s.RecoveryRequired != (tc.state == "uncertain") {
				t.Fatalf("receipt=%+v recovery=%v", r, s.RecoveryRequired)
			}
			if tc.disposition != "" && (r.Recovery == nil || r.Recovery.Disposition != tc.disposition || r.Status != tc.status) {
				t.Fatalf("disposition %+v status %d", r.Recovery, r.Status)
			}
			if strings.HasPrefix(tc.action, "complete-uncertain") && r.Completion != nil {
				t.Fatal("contradicted completion retained", r.Completion)
			}
			if tc.gated && tc.action != "complete-ungated" && (r.Scope == nil || len(r.Scope.Writes) == 0 || r.Scope.Writes[0] != (Resource{ResourceAccount, "personal"})) {
				t.Fatalf("scope %+v", r.Scope)
			}
			if tc.action == "expand-kill" && (len(r.Scope.Writes) != 2 || r.Scope.Writes[1] != (Resource{Kind: ResourceImages})) {
				t.Fatalf("expansion %+v", r.Scope)
			}
			if tc.action == "proof-kill" && (len(r.Proofs) != 1 || r.Proofs[0].KeyID != s.RecoveryKey.ID || r.Proofs[0].Fingerprint == "") {
				t.Fatalf("proofs %+v", r.Proofs)
			}
			raw, err := os.ReadFile(filepath.Join(s.Receipts.Dir, req.ID+".json"))
			if err != nil || bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("receipt leaks document data: %v", err)
			}
		})
	}
}

func TestCompletionSurvivesStartupRecoveryOnly(t *testing.T) {
	store := Receipts{t.TempDir()}
	req := request("box.set")
	r, err := store.begin(req, Principal{}, "generation", testDisk, false)
	if err != nil {
		t.Fatal(err)
	}
	r.Completion = &Completion{Status: 1, At: time.Now().UTC()}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}
	if recovery, err := store.Recover(); err != nil || recovery {
		t.Fatal(recovery, err)
	}
	r, err = store.Read(req.ID)
	// A recorded failed result stays failed; it is never relabeled success.
	if err != nil || r.State != "recovered" || r.Status != 1 || r.Recovery.Disposition != DispositionRecordedCompletion {
		t.Fatal(r, err)
	}
	waiting := request("box.set")
	waiting.ID += "-waiting"
	r, err = store.begin(waiting, Principal{}, "generation", testDisk, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Recover(); err != nil {
		t.Fatal(err)
	}
	r, err = store.Read(waiting.ID)
	if err != nil || r.State != "recovered" || r.Status != 2 || r.Recovery.Disposition != DispositionNotApplied {
		t.Fatal(r, err)
	}
}

func TestReceiptEvidenceValidationFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	valid := func() Receipt {
		return Receipt{ID: "test-request-0123456789", Method: "box.set", State: "uncertain", Started: now, Generation: "g",
			ProofVersion: ProofVersion, DiskID: testDisk, EffectPhase: EffectStarted,
			Scope: &Scope{Writes: []Resource{{ResourceBox, "web"}}, Reads: []Resource{{ResourceAccount, "personal"}}}}
	}
	for name, mutate := range map[string]func(*Receipt){
		"future version":    func(r *Receipt) { r.ProofVersion = ProofVersion + 1 },
		"legacy with scope": func(r *Receipt) { r.ProofVersion, r.DiskID = 0, "" },
		"legacy recovered": func(r *Receipt) {
			r.ProofVersion, r.DiskID, r.Scope, r.EffectPhase, r.State = 0, "", nil, "", "recovered"
		},
		"bad disk":            func(r *Receipt) { r.DiskID = "disk" },
		"unknown kind":        func(r *Receipt) { r.Scope.Writes[0].Kind = "volume" },
		"named shared domain": func(r *Receipt) { r.Scope.Writes[0] = Resource{ResourceChat, "room"} },
		"invalid box name":    func(r *Receipt) { r.Scope.Writes[0].Name = "../web" },
		"empty writes":        func(r *Receipt) { r.Scope.Writes = nil },
		"unknown verifier":    func(r *Receipt) { r.Verifier = "looks-healthy" },
		"global verifier":     func(r *Receipt) { r.Verifier, r.Scope = VerifierAccountDocument, &Scope{Global: true} },
		"proof without verifier": func(r *Receipt) {
			r.Proofs = []Proof{{Kind: DocumentAccount, Name: "personal", Present: true, Fingerprint: strings.Repeat("0", 64), KeyID: testDisk}}
		},
		"completion while waiting": func(r *Receipt) {
			r.EffectPhase, r.Completion = EffectWaiting, &Completion{At: now}
		},
		"recovered without disposition": func(r *Receipt) { r.State = "recovered" },
		"disposition while uncertain": func(r *Receipt) {
			r.Recovery = &Recovery{Disposition: DispositionNotApplied, Proof: ProofGateWaiting, Version: 1, At: now}
		},
		"reserved disposition": func(r *Receipt) {
			r.State, r.Recovery = "recovered", &Recovery{Disposition: DispositionConsistentResultUnknown, Proof: ProofEffectsDone, Version: 1, At: now}
		},
		"applied without proofs": func(r *Receipt) {
			r.State, r.Recovery = "recovered", &Recovery{Disposition: DispositionApplied, Proof: ProofDocumentImage, Version: 1, At: now}
		},
		"completion status mismatch": func(r *Receipt) {
			r.Completion = &Completion{Status: 1, At: now}
			r.State, r.Recovery = "recovered", &Recovery{Disposition: DispositionRecordedCompletion, Proof: ProofEffectsDone, Version: 1, At: now}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := Receipts{t.TempDir()}
			r := valid()
			if err := store.Save(r); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(r.ID); err != nil {
				t.Fatal("valid baseline rejected", err)
			}
			mutate(&r)
			if err := store.Save(r); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(r.ID); err == nil {
				t.Fatal("malformed evidence accepted")
			}
			if _, err := store.List(); err == nil {
				t.Fatal("inventory accepted malformed evidence")
			}
		})
	}
}

func TestRecoveredReceiptsAreEvictable(t *testing.T) {
	store := Receipts{t.TempDir()}
	for i := range receiptLimit {
		req := request("box.set")
		req.ID = fmt.Sprintf("%s-%03d", req.ID, i)
		r, err := store.begin(req, Principal{}, "g", testDisk, true)
		if err != nil {
			t.Fatal(i, err)
		}
		r.interrupt()
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	next := request("box.set")
	next.ID += "-next"
	if _, err := store.begin(next, Principal{}, "g", testDisk, true); err != nil {
		t.Fatal("recovered receipts blocked admission", err)
	}
}

func TestRecoveryKeyCustody(t *testing.T) {
	meta := t.TempDir()
	key, err := LoadRecoveryKey(meta)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadRecoveryKey(meta)
	if err != nil || again.ID != key.ID {
		t.Fatal("same-disk restart changed the recovery key", err)
	}
	path := filepath.Join(meta, "recovery", "key")
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(st, err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal(st, err)
	}
	d := DocumentIntent{Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte("postimage"), Mode: 0o600}
	p := key.proof(testDisk, "request-a", VerifierAccountDocument, d)
	if !key.matches(testDisk, "request-a", VerifierAccountDocument, p, d) {
		t.Fatal("exact postimage does not match")
	}
	for name, observed := range map[string]DocumentIntent{
		"preimage": {Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte("preimage"), Mode: 0o600},
		"mode":     {Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte("postimage"), Mode: 0o644},
		"owner":    {Kind: DocumentAccount, Name: "personal", Present: true, Data: []byte("postimage"), Mode: 0o600, UID: 7},
		"absent":   {Kind: DocumentAccount, Name: "personal"},
		"name":     {Kind: DocumentAccount, Name: "other", Present: true, Data: []byte("postimage"), Mode: 0o600},
	} {
		if key.matches(testDisk, "request-a", VerifierAccountDocument, p, observed) {
			t.Fatal("mismatch accepted:", name)
		}
	}
	if key.matches(testDisk, "request-b", VerifierAccountDocument, p, d) || key.matches("fedcba9876543210fedcba9876543210", "request-a", VerifierAccountDocument, p, d) {
		t.Fatal("fingerprint is not bound to its receipt and disk")
	}
	if other := newRecoveryKey(bytes.Repeat([]byte{1}, recoveryKeySize)); other.matches(testDisk, "request-a", VerifierAccountDocument, p, d) {
		t.Fatal("a different key verified the proof")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecoveryKey(meta); err == nil {
		t.Fatal("readable key accepted")
	}
	if err := os.WriteFile(path, []byte{9, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRecoveryKey(meta); err == nil {
		t.Fatal("foreign key format replaced or accepted")
	}
	if raw, _ := os.ReadFile(path); !bytes.Equal(raw, []byte{9, 1, 2}) {
		t.Fatal("malformed key was regenerated")
	}
}
