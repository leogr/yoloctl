package term

import (
	"os"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
)

// TestTabRequestChild runs as the tab's child process: it writes one request
// on the inherited channel, the way `space view` does.
func TestTabRequestChild(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_TAB_REQUEST") != "1" {
		t.Skip("child helper")
	}
	f, err := termio.InheritedTabRequests()
	if err != nil || f == nil {
		t.Fatalf("no inherited channel: %v", err)
	}
	if err := termio.WriteTabRequest(f, termio.TabRequest{Op: "attach", Space: "dev", Box: "api"}); err != nil {
		t.Fatal(err)
	}
}

func TestTabRequestReachesTUI(t *testing.T) {
	got := make(chan termio.TabRequest, 1)
	s, err := Start(Options{
		TrustedView:  true,
		Argv:         []string{os.Args[0], "-test.run=^TestTabRequestChild$"},
		Env:          append(os.Environ(), "YOLOCTL_TEST_TAB_REQUEST=1"),
		Rows:         8,
		Cols:         60,
		OnTabRequest: func(r termio.TabRequest) { got <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case r := <-got:
		if r != (termio.TabRequest{Op: "attach", Space: "dev", Box: "api"}) {
			t.Fatalf("request = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no request from the child")
	}
}
