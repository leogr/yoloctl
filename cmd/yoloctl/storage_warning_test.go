package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/storagegc"
)

func TestDiskReserveWarningUsesPreflightThreshold(t *testing.T) {
	old := storageWarningDisk
	t.Cleanup(func() { storageWarningDisk = old })
	for _, tc := range []struct {
		name string
		disk storagegc.Disk
		err  error
		warn bool
	}{
		{"below", storagegc.Disk{Total: 100 * testGiB, Available: 19 * testGiB}, nil, true},
		{"at", storagegc.Disk{Total: 100 * testGiB, Available: 20 * testGiB}, nil, false},
		{"above", storagegc.Disk{Total: 100 * testGiB, Available: 21 * testGiB}, nil, false},
		{"unreadable", storagegc.Disk{}, errors.New("statfs failed"), false},
		{"unknown", storagegc.Disk{}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storageWarningDisk = func() (storagegc.Disk, error) { return tc.disk, tc.err }
			note := diskReserveWarning()
			if (note != "") != tc.warn {
				t.Fatalf("notice=%q", note)
			}
			if tc.warn {
				for _, want := range []string{"box cache clear BOX", "ordinary recreate keeps it", "box recreate BOX discards non-bound files"} {
					if !strings.Contains(note, want) {
						t.Errorf("missing cache remedy %q: %s", want, note)
					}
				}
			}
			if tc.warn && (!strings.Contains(note, "19.0 GiB free") || !strings.Contains(note, "needs 20.0 GiB") || !strings.Contains(note, "yoloctl space storage")) {
				t.Fatal(note)
			}
		})
	}
}

func TestControllerDiskWarningSkipsJSONAndInternalReads(t *testing.T) {
	old := storageWarningDisk
	t.Cleanup(func() { storageWarningDisk = old })
	for _, tc := range []struct {
		method string
		json   bool
		warn   bool
	}{
		{"box.create", false, true}, {"box.recreate", false, true},
		{"box.start", false, true}, {"box.list", false, true},
		{"box.list", true, false}, {"box.inventory", false, false},
		{"control.complete", false, false}, {"box.stop", false, false},
	} {
		t.Run(tc.method+map[bool]string{true: "-json", false: ""}[tc.json], func(t *testing.T) {
			var out bytes.Buffer
			defer bindCLITestLogger(t, &out)()
			reads := 0
			storageWarningDisk = func() (storagegc.Disk, error) {
				reads++
				return storagegc.Disk{Total: 100 * testGiB, Available: 19 * testGiB}, nil
			}
			req := controller.Request{Method: tc.method, Flags: map[string][]string{}}
			if tc.json {
				req.Flags["json"] = []string{"true"}
			}
			warnControllerDisk(req)
			if strings.Contains(out.String(), "VM system disk:") != tc.warn || reads != map[bool]int{true: 1, false: 0}[tc.warn] || strings.Count(out.String(), "VM system disk:") > 1 {
				t.Fatalf("reads=%d output=%q", reads, out.String())
			}
		})
	}
}

func TestHostDiskWarningIsAnOptionalExistingAdminPing(t *testing.T) {
	old := diskWarningCall
	t.Cleanup(func() { diskWarningCall = old })
	for _, failed := range []bool{false, true} {
		var out bytes.Buffer
		restore := bindCLITestLogger(t, &out)
		calls := 0
		diskWarningCall = func(_ context.Context, _ *spaceconfig.Bootstrap, req controller.Request, streams controller.ClientIO) error {
			calls++
			if req.Method != "control.ping" || req.Flags[controller.StorageWarningFlag][0] != "true" {
				t.Fatal(req)
			}
			if failed {
				return errors.New("disk observation unavailable")
			}
			return json.NewEncoder(streams.Out).Encode("VM system disk: 19.0 GiB free; image preparation needs 20.0 GiB")
		}
		warnHostDisk(t.Context(), &spaceconfig.Bootstrap{Name: "dev"})
		restore()
		if calls != 1 || strings.Contains(out.String(), "VM system disk:") == failed {
			t.Fatalf("failed=%v calls=%d out=%q", failed, calls, out.String())
		}
	}
}
