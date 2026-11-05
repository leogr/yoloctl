package installmigration

import (
	"strings"
	"testing"
)

func TestSnapshotValidateRefusesIncompleteObservation(t *testing.T) {
	ok := Snapshot{Space: "s", Bundle: "b", State: "Running", DiskID: "d", DiskUUID: "u", Generation: "g", Bootstrap: strings.Repeat("a", 64)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []func(*Snapshot){
		func(s *Snapshot) { s.Space = "" },
		func(s *Snapshot) { s.State = "Paused" },
		func(s *Snapshot) { s.Bootstrap = "short" },
	} {
		s := ok
		bad(&s)
		if s.Validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
