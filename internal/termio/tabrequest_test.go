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

func TestResumeTargetValidation(t *testing.T) {
	good := TabRequest{Op: "resume-target", Space: "original", Box: "copied", NoAutoupdate: true}
	if err := good.ValidateResumeTarget(ResumeTargetBox); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []TabRequest{
		{Op: "attach", Space: "original", Box: "copied"},
		{Op: "resume-target", Space: "../other", Box: "copied"},
		{Op: "resume-target", Space: "original", Box: "../other"},
		{Op: "resume-target", Space: "original", Box: "copied", ChatAllowStart: true},
		{Op: "resume-target", Space: "original"},
	} {
		if bad.ValidateResumeTarget(ResumeTargetBox) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if good.ValidateResumeTarget(ResumeTargetNone) == nil || good.ValidateResumeTarget(ResumeTargetSpace) == nil {
		t.Fatal("box metadata accepted for a different original command class")
	}
	if err := (TabRequest{Op: "resume-target", Space: "original"}).ValidateResumeTarget(ResumeTargetSpace); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"op":"resume-target","space":"original","box":"copied","argv":["destroy"]}`,
		`{"op":"resume-target","space":"original","box":"copied"} {}`,
	} {
		if err := ReadTabRequests(strings.NewReader(raw+"\n"), func(TabRequest) { t.Error("malformed target delivered") }); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
