package storagegc

import "testing"

func TestBuildReserveAdmissionDoesNotChangeCleanup(t *testing.T) {
	const giB = uint64(1 << 30)
	for _, tc := range []struct {
		value                 *string
		total, free, required uint64
		admit                 bool
	}{
		{nil, 145, 15, 10, true},
		{nil, 40, 7, 8, false},
		{nil, 145, 10, 10, true},
		{nil, 145, 9, 10, false},
		{str("none"), 145, 15, 29, false},
		{str("0GiB"), 145, 0, 0, true},
		{str("12GiB"), 145, 11, 12, false},
	} {
		r, err := ParseReserve(tc.value)
		d := Disk{Total: tc.total * giB, Available: tc.free * giB}
		if err != nil || r.AdmissionFree(d) != tc.required*giB || r.Admits(d) != tc.admit {
			t.Fatalf("value=%v disk=%+v reserve=%+v err=%v", tc.value, d, r, err)
		}
		if d.TargetFree() != d.Total/5 || d.Low() != (d.Available < d.Total/5) || d.CacheBudget() != min(4*giB, d.Total/10) {
			t.Fatal("admission changed cleanup targets", d)
		}
	}
	for _, invalid := range []string{"", "500MiB", "1.5GiB", "-1GiB", "nonsense", "18446744073709551616GiB", "17179869184GiB"} {
		if _, err := ParseReserve(&invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func str(s string) *string { return &s }
