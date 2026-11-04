package openshell

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Installation reads qualified archives only from the CLI's embedded
// artifacts: never a network address, and a refusal when none are embedded.
func TestFetchReleaseArchiveIsEmbeddedOnly(t *testing.T) {
	saved := embeddedArchives
	t.Cleanup(func() { embeddedArchives = saved })
	embeddedArchives = nil
	if _, err := fetchReleaseArchive(context.Background(), EmbeddedArchivePrefix+"openshell-sandbox-amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "built without its embedded OpenShell artifacts") {
		t.Fatalf("no embedded artifacts: %v", err)
	}
	var asked string
	embeddedArchives = func(name string) ([]byte, error) {
		asked = name
		if name == "absent.tar.gz" {
			return nil, errors.New("absent")
		}
		return []byte("archive bytes"), nil
	}
	r, err := fetchReleaseArchive(context.Background(), EmbeddedArchivePrefix+"openshell-sandbox-amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(r); string(got) != "archive bytes" || asked != "openshell-sandbox-amd64.tar.gz" {
		t.Fatalf("%q from %q", got, asked)
	}
	if _, err := fetchReleaseArchive(context.Background(), EmbeddedArchivePrefix+"absent.tar.gz"); err == nil {
		t.Fatal("an absent embedded archive refuses")
	}
	for _, location := range []string{
		"https://example.invalid/openshell-sandbox-amd64.tar.gz",
		"embedded:../x.tar.gz", "embedded:x.tgz", "embedded:", "x.tar.gz", "embedded:A.tar.gz",
	} {
		asked = ""
		if _, err := fetchReleaseArchive(context.Background(), location); err == nil || asked != "" {
			t.Errorf("%q is not an embedded archive name: %v (asked %q)", location, err, asked)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchReleaseArchive(ctx, EmbeddedArchivePrefix+"openshell-sandbox-amd64.tar.gz"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
