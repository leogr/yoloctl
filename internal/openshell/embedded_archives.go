package openshell

import (
	"errors"
	"regexp"
	"strings"
	"sync"
)

// The CLI registers the release archives it embeds once at startup. This
// package cannot import them itself: the helper links this package, and its
// own archive must never become an input to its next build.
var (
	embeddedArchivesOnce sync.Once
	embeddedArchives     func(name string) ([]byte, error)
)

// UseEmbeddedArchives makes installation read qualified release archives from
// source. The archive, member and binary hashes are verified against the
// qualification as before; nothing is downloaded. Only the first
// registration takes effect.
func UseEmbeddedArchives(source func(name string) ([]byte, error)) {
	embeddedArchivesOnce.Do(func() { embeddedArchives = source })
}

// EmbeddedArchivePrefix marks a qualified artifact location as an archive the
// CLI embeds, by name. It is a logical key, never a network address.
const EmbeddedArchivePrefix = "embedded:"

var embeddedArchivePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,126}\.tar\.gz$`)

// embeddedArchiveName is the archive name of a qualified "embedded:NAME"
// location; any other location is refused.
func embeddedArchiveName(location string) (string, error) {
	name, ok := strings.CutPrefix(location, EmbeddedArchivePrefix)
	if !ok || !embeddedArchivePattern.MatchString(name) {
		return "", errors.New("qualified artifact location must be an embedded archive name")
	}
	return name, nil
}
