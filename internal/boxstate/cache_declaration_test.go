package boxstate

import (
	"context"
	"os"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
)

func TestCacheOverlapLegacyDeclarationIsInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := saveBox(t, "default", "coder")
	raw, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\nmounts:\n  - source: /tmp/old-cache\n    target: /home/sandbox/.cache\n")...)
	if err := os.WriteFile(c.Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	boxes, err := testResolver([]container.Container{{Name: "coder", Status: "Up 1 minute", Image: "img"}}).ResolveSpace(context.Background(), "default", RuntimeSummary)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 || boxes[0].Declaration != DeclarationInvalid {
		t.Fatalf("existing overlap was not declaration_invalid: %+v", boxes)
	}
	if boxes[0].Runtime != RuntimeRunning {
		t.Fatal("invalid declaration hid the actual running container", boxes[0])
	}
	if _, err := agentbox.Load("default", "coder"); err != nil {
		t.Fatal("recovery cannot read old declaration", err)
	}
}
