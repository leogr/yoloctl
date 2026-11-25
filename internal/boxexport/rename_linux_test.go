package boxexport

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRenameExclusiveInvalidMoveExplainsNoOverwrite(t *testing.T) {
	parent, err := openHostDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.close() }()
	source, err := parent.mkdir("source")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.close() }()

	// Moving a directory into itself returns EINVAL on a local filesystem.
	err = renameExclusive(parent.fd(), "source", source.fd(), "nested")
	if !errors.Is(err, unix.EINVAL) {
		t.Fatalf("exclusive rename error = %v, want EINVAL", err)
	}
	if got := err.Error(); !strings.Contains(got, "without overwriting") || !strings.Contains(got, "outside any SSHFS mount") {
		t.Fatalf("exclusive rename error lacks the publication context or remedy: %v", err)
	}
}
