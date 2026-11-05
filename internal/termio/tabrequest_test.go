package termio

import (
	"bytes"
	"strings"
	"testing"
)

func TestTabRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTabRequest(&buf, TabRequest{Op: "attach", Space: "dev", Box: "api"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteTabRequest(&buf, TabRequest{Op: "attach", Space: "dev", Box: strings.Repeat("x", maxTabRequest)}); err == nil {
		t.Fatal("oversized request written")
	}
	var got []TabRequest
	if err := ReadTabRequests(&buf, func(r TabRequest) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (TabRequest{Op: "attach", Space: "dev", Box: "api"}) {
		t.Fatalf("got %+v", got)
	}
	if err := ReadTabRequests(strings.NewReader("{oops\n"), func(TabRequest) {}); err == nil {
		t.Fatal("malformed request accepted")
	}
	if err := ReadTabRequests(strings.NewReader(strings.Repeat("x", 2*maxTabRequest)+"\n"), func(TabRequest) {}); err == nil {
		t.Fatal("oversized line accepted")
	}
}

func TestInheritedTabRequestsRejectsBadDescriptor(t *testing.T) {
	t.Setenv(TabRequestFDEnv, "")
	if f, err := InheritedTabRequests(); f != nil || err != nil {
		t.Fatalf("unset = %v %v", f, err)
	}
	for _, v := range []string{"2", "x", "99999"} {
		t.Setenv(TabRequestFDEnv, v)
		if _, err := InheritedTabRequests(); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestTabRequestKeepsLegacySpaceName(t *testing.T) {
	var buf bytes.Buffer
	request := TabRequest{Op: "attach", Space: strings.Repeat("s", 63), Box: "api"}
	if err := WriteTabRequest(&buf, request); err != nil {
		t.Fatal(err)
	}
	var got TabRequest
	if err := ReadTabRequests(&buf, func(r TabRequest) { got = r }); err != nil {
		t.Fatal(err)
	}
	if got != request {
		t.Fatalf("existing space reference lost: %+v", got)
	}
}
