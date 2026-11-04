package chatui

// ImageRequest asks the host to read one PNG from its clipboard after an
// explicit local Ctrl+V in Channel's composer. ID correlates the answer, so a
// stale or duplicate read cannot land in another draft.
type ImageRequest struct {
	ID, Channel string
}

// ImagePreparedMsg answers Options.ReadImage. The host has validated Data as
// a PNG within the managed artifact cap and bound Token to the room, disk and
// channel it was read for; Name is generated and carries no host path. Width
// and Height are 0 when unknown. Err wraps hostintegration.ErrNoImage when
// the clipboard holds no image.
type ImagePreparedMsg struct {
	ID, Channel, Token, Name string
	Data                     []byte
	Width, Height            int
	Err                      error
}

// ImageUpload publishes one prepared image to Channel as an AIRC artifact,
// or with Stage stores it unannounced for one later message to publish.
// The host consumes Token on the attempt; it is never retried.
type ImageUpload struct {
	Channel, Token string
	Data           []byte
	Stage          bool
}

// ImageUploadedMsg answers Options.UploadImage: Confirmed with the server's
// artifact and announcement identities, Rejected, or Uncertain when the
// outcome is unknown (look for the announcement before trying again).
type ImageUploadedMsg struct {
	Channel, Token, RequestID, ArtifactID, SHA256, MsgID string
	Bytes                                                int64
	State                                                Delivery
	Err                                                  error
	// Staged: a confirmed stage, with no announcement message.
	Staged bool
}

// ArtifactRef is a message's artifact, taken only from validated
// server-reserved tags, never from message text. It does not prove a media
// type: the view labels it a file.
type ArtifactRef struct {
	ID, Name, SHA256 string
	Bytes            int64
}

// AttachRef sends one staged image at its marker's byte offset.
type AttachRef struct {
	ID string
	At int
}

// AttachmentRef is one image a message published, at its marker's offset.
type AttachmentRef struct {
	ArtifactRef
	At int
}

// AttachmentRefusal is a definite refusal of a message with images. Index
// (1-based) names the refused image; MessageID is only a lookup hint for the
// message that already published it.
type AttachmentRefusal struct {
	Reason    string // invalid, unavailable, unknown, consumed, too-long, store, channel-only
	Index     int
	MessageID string
}
