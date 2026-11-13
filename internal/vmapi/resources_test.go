package vmapi

import "testing"

func TestValidateResourcesUsesEachAdaptersBootBounds(t *testing.T) {
	for _, tc := range []struct {
		backend string
		r       Resources
		ok      bool
	}{
		{"firecracker", Resources{CPUs: 1, Memory: "128MiB"}, true},
		{"firecracker", Resources{CPUs: 32, Memory: "16GiB"}, true},
		{"firecracker", Resources{CPUs: 0, Memory: "4GiB"}, false},
		{"firecracker", Resources{CPUs: 33, Memory: "4GiB"}, false},
		{"firecracker", Resources{CPUs: 4, Memory: "127MiB"}, false},
		{"firecracker", Resources{CPUs: 4, Memory: "4097KiB"}, false},
		{"lima", Resources{CPUs: 64, Memory: "4097KiB"}, true},
		{"", Resources{CPUs: 1, Memory: "1GiB"}, true},
		{"lima", Resources{CPUs: 0, Memory: "4GiB"}, false},
		{"lima", Resources{CPUs: 4, Memory: "0GiB"}, false},
		{"lima", Resources{CPUs: 4, Memory: "4G"}, false},
		{"lima", Resources{CPUs: 4, Memory: "auto"}, false},
		{"other", Resources{CPUs: 4, Memory: "4GiB"}, false},
	} {
		if err := ValidateResources(tc.backend, tc.r); (err == nil) != tc.ok {
			t.Errorf("%q %+v: %v", tc.backend, tc.r, err)
		}
	}
}
