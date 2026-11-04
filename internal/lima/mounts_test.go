package lima

import (
	"strings"
	"testing"
)

func TestMarshalMounts_EmitsLimaShape(t *testing.T) {
	// yoloctl mounts only explicitly-granted paths (no blanket `~` home
	// mount); the writable/non-writable rendering is exercised with real
	// paths so the fixture doesn't imply a home mount that no longer exists.
	mounts := []Mount{
		{Location: "/opt/shared", Writable: false},
		{Location: "/home/leogr/projects/myrepo", Writable: true},
	}
	got := MarshalMounts(mounts)
	wantContains := []string{
		`- location: "/opt/shared"`,
		`  writable: false`,
		`- location: "/home/leogr/projects/myrepo"`,
		`  writable: true`,
	}
	for _, want := range wantContains {
		if !strings.Contains(got, want) {
			t.Errorf("MarshalMounts missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestMarshalMounts_Empty(t *testing.T) {
	if got := MarshalMounts(nil); got != "" {
		t.Errorf("nil: got %q want empty", got)
	}
	if got := MarshalMounts([]Mount{}); got != "" {
		t.Errorf("empty: got %q want empty", got)
	}
}
