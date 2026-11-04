package buildinfo

import "testing"

func TestReleaseIdentity(t *testing.T) {
	for _, v := range []string{"v0.0.0", "v1.2.3", "v1.2.3-qa.1", "v1.2.3-0"} {
		if !IsRelease(v) {
			t.Errorf("valid tag rejected: %s", v)
		}
	}
	for _, v := range []string{"dev", "1.2.3", "v1.2", "v01.2.3", "v1.2.3-01", "v1.2.3+meta", "v1.2.3-", "v1.2.3\n", "../v1.2.3"} {
		if IsRelease(v) {
			t.Errorf("invalid tag accepted: %q", v)
		}
	}
}

func TestReleaseOrdering(t *testing.T) {
	tags := []string{"v1.0.0-0", "v1.0.0-2", "v1.0.0-10", "v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-beta", "v1.0.0", "v1.1.0", "v2.0.0", "v10.0.0"}
	for i, a := range tags {
		for j, b := range tags {
			c := CompareRelease(a, b)
			if (i < j && c >= 0) || (i == j && c != 0) || (i > j && c <= 0) {
				t.Fatal(a, b, c)
			}
		}
	}
}
