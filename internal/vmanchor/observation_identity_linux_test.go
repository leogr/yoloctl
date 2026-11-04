//go:build linux

package vmanchor

import "testing"

// Pure encoding fixtures establish protocol bytes, never source provenance.
func TestObservationIdentityVersionOneFingerprint(t *testing.T) {
	boot := "11111111-1111-4111-8111-111111111111"
	original := processIdentity{PID: 1, Parent: 0, Start: 123, Directory: fileIdentity{Device: 2, Inode: 3}, Namespace: fileIdentity{Device: 4, Inode: 5}}
	got, err := observationFingerprintV1(boot, original)
	if err != nil || got != "02586e80bdb5e4b830041faddc3f41ac" {
		t.Fatalf("version1 bytes changed: %q %v", got, err)
	}
	mutations := []processIdentity{original, original, original, original, original, original, original}
	mutations[0].PID++
	mutations[1].Parent++
	mutations[2].Start++
	mutations[3].Directory.Device++
	mutations[4].Directory.Inode++
	mutations[5].Namespace.Device++
	mutations[6].Namespace.Inode++
	for i, changed := range mutations {
		next, err := observationFingerprintV1(boot, changed)
		if err != nil || next == got || len(next) != 32 {
			t.Fatalf("original field%d not bound: %q %v", i, next, err)
		}
	}
	next, err := observationFingerprintV1("22222222-2222-4222-8222-222222222222", original)
	if err != nil || next == got {
		t.Fatal("boot identity not bound")
	}
}
func TestObservationIdentityUnavailableNeverLeaksCorrelation(t *testing.T) {
	var missing *FirecrackerSource
	for _, source := range []*FirecrackerSource{missing, {}, {closed: true, boot: "retained-but-unavailable"}} {
		boot, fingerprint, err := source.ObservationIdentity()
		if err == nil || boot != "" || fingerprint != "" {
			t.Fatalf("unavailable source leaked correlation: %q %q %v", boot, fingerprint, err)
		}
	}
}

// Existing source mechanism fixture uses this test process; it is NOT actual
// boot/PID1/VM admission. Its actual unsupported pidfd/namespace acquisition is
// explicitly skipped by fixtureSource and remains qualified-executor work.
func TestObservationIdentityOriginalSourceAndClose(t *testing.T) {
	source := fixtureSource(t)
	boot, fingerprint, err := source.ObservationIdentity()
	if err != nil || len(boot) != 36 || len(fingerprint) != 32 {
		t.Fatalf("original source observation: %q %q %v", boot, fingerprint, err)
	}
	again, next, err := source.ObservationIdentity()
	if err != nil || again != boot || next != fingerprint {
		t.Fatal("same source changed correlation", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if boot, fingerprint, err := source.ObservationIdentity(); err == nil || boot != "" || fingerprint != "" {
		t.Fatal("closed source retained positive correlation")
	}
}
