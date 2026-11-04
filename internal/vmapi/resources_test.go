package vmapi

import "testing"

func TestValidateResourcesUsesEachAdaptersBootBounds(t *testing.T) {
	for _, tc := range []struct {
		backend string
		r       Resources
		ok      bool
	}{
		{"firecracker", Resources{1, "128MiB"}, true},
		{"firecracker", Resources{32, "16GiB"}, true},
		{"firecracker", Resources{0, "4GiB"}, false},
		{"firecracker", Resources{33, "4GiB"}, false},
		{"firecracker", Resources{4, "127MiB"}, false},
		{"firecracker", Resources{4, "4097KiB"}, false},
		{"lima", Resources{64, "4097KiB"}, true},
		{"", Resources{1, "1GiB"}, true},
		{"lima", Resources{0, "4GiB"}, false},
		{"lima", Resources{4, "0GiB"}, false},
		{"lima", Resources{4, "4G"}, false},
		{"lima", Resources{4, "auto"}, false},
		{"other", Resources{4, "4GiB"}, false},
	} {
		if err := ValidateResources(tc.backend, tc.r); (err == nil) != tc.ok {
			t.Errorf("%q %+v: %v", tc.backend, tc.r, err)
		}
	}
}
