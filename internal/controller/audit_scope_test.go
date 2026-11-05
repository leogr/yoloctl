package controller

import "testing"

func TestAuditScopeAndMethodAuthority(t *testing.T) {
	for _, scope := range []*Scope{{Writes: []Resource{{Kind: ResourceAudit}}}, {Reads: []Resource{{Kind: ResourceAudit}}, Writes: []Resource{{Kind: ResourceBox, Name: "worker"}}}} {
		if err := scope.validate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Resource{Kind: ResourceAudit, Name: "caller-selected"}).validate(); err == nil {
		t.Fatal("audit domain accepted a caller-selected subresource")
	}
	if err := FootprintAudit.allows([]Resource{{Kind: ResourceBox, Name: "worker"}}); err == nil {
		t.Fatal("audit operation widened its scope")
	}
}

func TestAuditUncertaintyDoesNotHoldUnrelatedBoxes(t *testing.T) {
	receipt := Receipt{State: "uncertain", Scope: &Scope{Writes: []Resource{{Kind: ResourceAudit}}}}
	h := holds{writes: map[Resource][]Receipt{{Kind: ResourceAudit}: {receipt}}}
	_, conflicts, _ := h.conflict(&Scope{Writes: []Resource{{Kind: ResourceBox, Name: "worker"}}})
	if len(conflicts) != 0 {
		t.Fatal("audit uncertainty held an unrelated workload")
	}
	_, conflicts, _ = h.conflict(&Scope{Writes: []Resource{{Kind: ResourceAudit}}})
	if len(conflicts) != 1 {
		t.Fatal("audit uncertainty did not hold audit changes")
	}
}
