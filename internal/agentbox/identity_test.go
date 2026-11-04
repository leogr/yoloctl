package agentbox

import (
	"os"
	"strings"
	"testing"
)

func TestDeclarationIdentitySurvivesEditsButNotNameReuse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "coder", Space: "work", Image: "img:1", ID: NewIdentity()}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	id := c.ID
	if id == "" {
		t.Fatal("missing declaration identity")
	}
	c.ID = "" // removing the field in an editor cannot reset membership
	if err := c.Save(); err != nil || c.ID != id {
		t.Fatal("edit reset identity", err)
	}
	c.ID = NewIdentity()
	if err := c.Save(); err == nil {
		t.Fatal("identity was editable")
	}
	if err := c.Delete(); err != nil {
		t.Fatal(err)
	}
	replacement := &Config{Name: "coder", Space: "work", Image: "img:1", ID: NewIdentity()}
	if err := replacement.Save(); err != nil || replacement.ID == id {
		t.Fatal("name reuse inherited identity", err)
	}
}

func TestOrdinaryLegacySavesDoNotMintDeclarationIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := &Config{Name: "legacy", Space: "work", Image: "img:1"}
	for range 2 {
		if err := c.Save(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(c.Path())
		if err != nil || c.ID != "" || strings.Contains(string(raw), "\nid:") {
			t.Fatal("ordinary save introduced an identity field", err)
		}
		c, err = Load("work", "legacy")
		if err != nil {
			t.Fatal(err)
		}
		c.Image = "img:2"
	}
	// The enrollment service, rather than Save, explicitly requests identity.
	c.ID = NewIdentity()
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	id := c.ID
	c.ID = ""
	if err := c.Save(); err != nil || c.ID != id {
		t.Fatal("ordinary edit lost explicitly enrolled identity", err)
	}
}
