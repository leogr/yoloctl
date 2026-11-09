package term

import (
	"os"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
)

func TestResumeTargetChild(t *testing.T) {
	mode := os.Getenv("YOLOCTL_TEST_RESUME_TARGET")
	if mode == "" {
		t.Skip("child helper")
	}
	f, err := termio.InheritedTabRequests()
	if err != nil || f == nil {
		t.Fatalf("private channel: %v", err)
	}
	defer func() { _ = f.Close() }()
	requests := []termio.TabRequest{
		{Op: "resume-target", Space: "../invalid", Box: "wrong"},
		{Op: "attach", Space: "wrong", Box: "wrong"},
		{Op: "resume-target", Space: "original", Box: "copied", NoAutoupdate: true},
		{Op: "resume-target", Space: "changed", Box: "other"},
	}
	if mode == "malformed" {
		_, _ = f.WriteString("{\"op\":\"resume-target\",\"argv\":[\"destroy\"]}\n")
		return
	}
	for _, r := range requests {
		if err := termio.WriteTabRequest(f, r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResumeTargetRetainedThroughFastCompletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    termio.ResumeTargetKind
		trusted bool
		mode    string
		want    bool
	}{
		{"valid", termio.ResumeTargetBox, true, "normal", true},
		{"wrong-command-class", termio.ResumeTargetSpace, true, "normal", false},
		{"no-command-class", termio.ResumeTargetNone, true, "normal", false},
		{"untrusted", termio.ResumeTargetBox, false, "normal", false},
		{"malformed", termio.ResumeTargetBox, true, "malformed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			openRequests := make(chan termio.TabRequest, 1)
			s, err := Start(Options{TrustedView: tc.trusted, ResumeTargetKind: tc.kind,
				Argv: []string{os.Args[0], "-test.run=^TestResumeTargetChild$"},
				Env:  append(os.Environ(), "YOLOCTL_TEST_RESUME_TARGET="+tc.mode), Rows: 8, Cols: 60,
				OnTabRequest: func(r termio.TabRequest) {
					if r.Op != "attach" {
						t.Error("metadata reached open-tab handler")
					}
					openRequests <- r
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			select {
			case <-s.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("fast child did not finish")
			}
			got, ok := s.ResumeTarget()
			if ok != tc.want || (ok && got != (termio.TabRequest{Op: "resume-target", Space: "original", Box: "copied", NoAutoupdate: true})) {
				t.Fatalf("target = %+v, %v", got, ok)
			}
			if tc.kind == termio.ResumeTargetNone {
				select {
				case <-openRequests:
				case <-time.After(time.Second):
					t.Fatal("ordinary open request lost")
				}
			} else {
				select {
				case <-openRequests:
					t.Fatal("metadata-only command opened a tab")
				default:
				}
			}
			if ok {
				got.Space = "modified-copy"
				s.Close()
				retained, _ := s.ResumeTarget()
				if retained.Space != "original" {
					t.Fatal("target changed after completion or through returned copy")
				}
			}
		})
	}
}
