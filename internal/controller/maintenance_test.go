package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaintenanceSkipsBusyOrUncertainController(t *testing.T) {
	for _, s := range []*Server{{active: 1}, {draining: true}, {RecoveryRequired: true}, {receiptFailure: errors.New("disk failure")}} {
		if ran, err := s.RunMaintenance(t.Context(), func(context.Context) error { t.Fatal("must not collect"); return nil }); ran || err != nil {
			t.Fatalf("ran=%v %v", ran, err)
		}
	}
}

func TestMaintenanceYieldsToMutationAndDrain(t *testing.T) {
	for _, drain := range []bool{false, true} {
		t.Run(map[bool]string{false: "mutation", true: "drain"}[drain], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			s := &Server{Receipts: Receipts{t.TempDir()}, Hello: hello()}
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := s.RunMaintenance(ctx, func(ctx context.Context) error { close(started); <-ctx.Done(); return nil })
				done <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Read-only admission neither waits for nor cancels maintenance.
			read, _, err := s.admit(request("box.list"), Principal{}, false)
			if err != nil {
				t.Fatal(err)
			}
			read()
			if drain {
				if err := s.Drain(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				release, _, err := s.admit(request("box.create"), Principal{}, true)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("maintenance did not yield")
			}
		})
	}
}

func TestRejectedMutationDoesNotInterruptMaintenance(t *testing.T) {
	s := &Server{Receipts: Receipts{t.TempDir()}, Hello: hello()}
	req := request("box.exec")
	// The duplicate ID fails receipt admission after all earlier checks.
	if _, err := s.Receipts.Begin(req, Principal{}, s.Hello.Generation); err != nil {
		t.Fatal(err)
	}
	_, err := s.RunMaintenance(t.Context(), func(ctx context.Context) error {
		if _, _, err := s.admit(req, Principal{}, true); err == nil {
			t.Fatal("duplicate mutation admitted")
		}
		if ctx.Err() != nil {
			t.Fatal("failed admission canceled maintenance")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A finished read is reported once, so background observation can run while
// someone looks; mutations are not reported here.
func TestReadFinishedReportsOnlyReads(t *testing.T) {
	var got []string
	s := &Server{Receipts: Receipts{t.TempDir()}, Hello: hello(), ReadFinished: func(m string) { got = append(got, m) }}
	read, _, err := s.admit(request("box.inventory"), Principal{}, false)
	if err != nil {
		t.Fatal(err)
	}
	read()
	read()
	release, _, err := s.admit(request("box.create"), Principal{}, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(got) != 1 || got[0] != "box.inventory" {
		t.Fatalf("reported %v, want [box.inventory]", got)
	}
}
