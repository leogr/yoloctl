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

// Program.Send can wait for the UI event loop, which can itself be closing
// this Session. Completion must not join an ordinary open-tab model callback.
func TestCloseDoesNotWaitForTabRequestCallback(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s, err := Start(Options{TrustedView: true,
		Argv: []string{os.Args[0], "-test.run=^TestTabRequestChild$"},
		Env:  append(os.Environ(), "YOLOCTL_TEST_TAB_REQUEST=1"), Rows: 8, Cols: 60,
		OnTabRequest: func(termio.TabRequest) { close(entered); <-release; close(returned) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer func() {
		close(release)
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Error("callback did not return after release")
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no request callback")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("session close waited for the UI callback")
	}
}
