package layout

import (
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// ValidateIdentity validates an opaque, random 128-bit native identity.
func ValidateIdentity(id string) error {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 16 || hex.EncodeToString(b) != id {
		return errors.New("invalid native identity")
	}
	return nil
}

// Chat paths are relative to a verified native disk, never a host config root.
const ChatManifest = "config/chat.json"
const ChatState = "chat-state"

func ChatRoomRelative(id string) (string, error) {
	if err := ValidateIdentity(id); err != nil {
		return "", err
	}
	return filepath.Join(ChatState, id), nil
}

func ChatMemberRelative(room, member string) (string, error) {
	base, err := ChatRoomRelative(room)
	if err != nil {
		return "", err
	}
	if err := ValidateIdentity(member); err != nil {
		return "", err
	}
	return filepath.Join(base, "members", member), nil
}

// ChatEndpointDir is a directory bind, never an individual socket inode. Its
// lifetime must span server container recreation. It grants no operator access.
func ChatEndpointDir(room string) (string, error) {
	if err := ValidateIdentity(room); err != nil {
		return "", err
	}
	// The reserved controller namespace avoids the sandbox socket directory of
	// the default box named "chat", which box teardown is allowed to remove.
	return filepath.Join(spaceenv.RunRoot, "chat", room), nil
}

// ChatControlDir is never projected to participants. Reload/status authority is
// separated from the IRC and artifact sockets even though both are Unix-only.
func ChatControlDir(room string) (string, error) {
	if err := ValidateIdentity(room); err != nil {
		return "", err
	}
	return filepath.Join(spaceenv.RunRoot, "chat-control", room), nil
}

// ChatPresenceDir holds the controller's shared, disposable presence
// snapshot for one room and the lease that elects its single refresher.
func ChatPresenceDir(room string) (string, error) {
	if err := ValidateIdentity(room); err != nil {
		return "", err
	}
	return filepath.Join(spaceenv.RunRoot, "chat-presence", room), nil
}
