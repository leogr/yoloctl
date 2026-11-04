package chatstream

import (
	"strconv"
	"strings"
)

// Artifact describes a server-authenticated attachment, not necessarily an
// image. A filename or message body never establishes its type or origin.
// Downloads use ID on the fixed private artifact origin, never a message URL.
type Artifact struct {
	ID, Name, SHA256 string
	Bytes            int64
}

// ValidArtifactID accepts the canonical UUID representation emitted by AIRC.
func ValidArtifactID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range []byte(id) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !lowerHex(c) {
			return false
		}
	}
	return true
}

func ValidArtifactHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range []byte(hash) {
		if !lowerHex(c) {
			return false
		}
	}
	return true
}

func lowerHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }

func validArtifactName(name string) bool {
	if name == "" || len(name) > 100 || name == "." || name == ".." {
		return false
	}
	for _, c := range []byte(name) {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if !letter && !digit && !strings.ContainsRune("._-", rune(c)) {
			return false
		}
	}
	return true
}

// artifactFromTags accepts only the complete reserved server tag set. AIRC
// strips inbound non-client tags before publication and persists these tags
// with history. Client-only lookalikes (+airc-artifact) are never considered.
func artifactFromTags(tags map[string]string) *Artifact {
	a := Artifact{ID: tags["airc-artifact"], Name: tags["airc-artifact-name"], SHA256: tags["airc-artifact-sha256"]}
	size := tags["airc-artifact-bytes"]
	n, err := strconv.ParseInt(size, 10, 64)
	if !ValidArtifactID(a.ID) || !validArtifactName(a.Name) || !ValidArtifactHash(a.SHA256) || err != nil || n < 0 || strconv.FormatInt(n, 10) != size {
		return nil
	}
	a.Bytes = n
	return &a
}
