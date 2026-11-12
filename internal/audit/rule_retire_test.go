package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

func TestRuleRetirementProtectsSelectedPriorAndService(t *testing.T) {
	store := RuleStore{Root: t.TempDir()}
	accept := func(context.Context, string, string) (Validation, error) { return Validation{}, nil }
	var sets []RuleSet
	generation := ""
	for i := 0; i < 7; i++ {
		next, _, _, err := store.Prepare(t.Context(), generation, RuleChange{Kind: "import", Name: "test.yaml", Body: strings.NewReader(strings.Repeat(string(rune('a'+i)), 1<<10))}, "/tools", "/metadata", accept)
		if err != nil {
			t.Fatal(err)
		}
		sets = append(sets, next)
		generation = next.Generation
		// The current operation's prior selection survives completion. The service
		// may still refer to an older generation, including while audit is disabled.
		prior := ""
		if i > 0 {
			prior = sets[i-1].Generation
		}
		if err := store.Retire(t.Context(), next.Generation, prior, sets[0].Generation); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(layout.AuditRulesDir(store.Root))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{next.Generation: true, sets[0].Generation: true}
		if prior != "" {
			want[prior] = true
		}
		if len(entries) != len(want) {
			t.Fatal("unreferenced generations accumulated", len(entries), len(want))
		}
		for _, entry := range entries {
			if !want[entry.Name()] {
				t.Fatal("unexpected retained generation")
			}
		}
	}
	for _, set := range []RuleSet{sets[0], sets[len(sets)-2], sets[len(sets)-1]} {
		if _, err := store.Read(set.Generation); err != nil {
			t.Fatal("removed protected input", err)
		}
	}
	// An unexpected child is not adopted/deleted as generated state.
	dir, _ := layout.AuditRulesGeneration(store.Root, sets[0].Generation)
	if err := os.WriteFile(filepath.Join(dir, "unrelated"), []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Discard(sets[0]); err == nil {
		t.Fatal("deleted unlisted entry")
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated")); err != nil {
		t.Fatal("unlisted data removed", err)
	}
}
