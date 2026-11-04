package chatstream

import (
	"regexp"
	"strconv"
	"strings"
)

// Staged attachments (AIRC feature message-attachments): one message
// publishes artifacts staged beforehand. Its text holds the marker
// [Image #k] at each attachment's byte offset; AIRC verifies ownership and
// positions and sets the reserved airc-artifacts tag. Nothing here is
// authority: the server repeats every check.

// AttachmentsFeature is the AIRC feature that allows staging and attach.
const AttachmentsFeature = "message-attachments"

// MaxAttachments bounds the staged artifacts one message publishes.
const MaxAttachments = 8

// Attach references one staged artifact at its marker's byte offset.
type Attach struct {
	ID string `json:"id"`
	At int    `json:"at"`
}

// Attachment is a published artifact of a message and its marker offset.
type Attachment struct {
	Artifact
	At int
}

// Marker is the text of the k-th (1-based) attachment.
func Marker(k int) string { return "[Image #" + strconv.Itoa(k) + "]" }

// ValidAttach accepts 1–8 unique canonical IDs whose markers sit at their
// offsets in text, in order and without overlap.
func ValidAttach(text string, attach []Attach) bool {
	if len(attach) == 0 || len(attach) > MaxAttachments {
		return false
	}
	free, seen := 0, map[string]bool{}
	for i, a := range attach {
		marker := Marker(i + 1)
		if !ValidArtifactID(a.ID) || seen[a.ID] || a.At < free || a.At > len(text) || !strings.HasPrefix(text[a.At:], marker) {
			return false
		}
		seen[a.ID] = true
		free = a.At + len(marker)
	}
	return true
}

// attachmentsFromTags accepts the complete reserved tag only when every entry
// is valid and its marker sits at its offset in text; otherwise none.
func attachmentsFromTags(tags map[string]string, text string) []Attachment {
	raw, ok := tags["airc-artifacts"]
	if !ok {
		return nil
	}
	var out []Attachment
	var refs []Attach
	for _, item := range strings.Split(raw, ",") {
		parts := strings.SplitN(item, "/", 5)
		if len(parts) != 5 {
			return nil
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		at, atErr := strconv.Atoi(parts[3])
		if err != nil || atErr != nil || size < 0 || strconv.FormatInt(size, 10) != parts[1] || strconv.Itoa(at) != parts[3] || !ValidArtifactHash(parts[2]) || !validArtifactName(parts[4]) {
			return nil
		}
		out = append(out, Attachment{Artifact: Artifact{ID: parts[0], Bytes: size, SHA256: parts[2], Name: parts[4]}, At: at})
		refs = append(refs, Attach{ID: parts[0], At: at})
	}
	if !ValidAttach(text, refs) {
		return nil
	}
	return out
}

// SameAttachments reports whether a published message carries exactly the
// staged references of a submission, in order.
func SameAttachments(published []Attachment, attach []Attach) bool {
	if len(published) != len(attach) {
		return false
	}
	for i, a := range attach {
		if published[i].ID != a.ID || published[i].At != a.At {
			return false
		}
	}
	return true
}

// AttachmentRefusal is a definite AIRC refusal of a send with attachments.
// Index (1-based) names the refused entry for unknown and consumed; MessageID
// is only a lookup hint naming the message that already published it.
type AttachmentRefusal struct {
	Reason    string // invalid, unavailable, unknown, consumed, too-long, store, channel-only
	Index     int
	MessageID string
}

var attachmentRefusal = regexp.MustCompile(`^FAIL (?:PRIVMSG ATTACHMENTS \S+|BATCH ATTACHMENTS) attachments: (?:(invalid|unavailable|too-long|store|channel-only)|(unknown) k=([1-8])|(consumed) k=([1-8]) msgid=(\S{1,128}))$`)

// ParseAttachmentRefusal reads the receipt reason AIRC reports for a refused
// send with attachments; anything else is not an attachment refusal.
func ParseAttachmentRefusal(reason string) *AttachmentRefusal {
	m := attachmentRefusal.FindStringSubmatch(reason)
	switch {
	case m == nil:
		return nil
	case m[1] != "":
		return &AttachmentRefusal{Reason: m[1]}
	case m[2] != "":
		k, _ := strconv.Atoi(m[3])
		return &AttachmentRefusal{Reason: m[2], Index: k}
	}
	k, _ := strconv.Atoi(m[5])
	return &AttachmentRefusal{Reason: m[4], Index: k, MessageID: m[6]}
}
