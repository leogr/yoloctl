package managedchat

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

// Verify never creates missing storage or credentials. Once preparation has
// committed, losing a leaf is an explicit recovery error, not a new identity.
func (s Store) Verify(r *Room) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.DiskID != s.DiskID {
		return errors.New("managed chat disk identity mismatch")
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	for _, rel := range []string{layout.ChatState, base, filepath.Join(base, "members"), filepath.Join(base, "server")} {
		if err := safepath.VerifyPrivateDirectory(s.Disk, rel, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
	}
	raw, err := privateFile(s.Disk, filepath.Join(base, "owner.json"), 4096, os.Geteuid(), os.Getegid())
	if err != nil {
		return err
	}
	want, _ := json.Marshal(struct{ Room, Disk string }{r.ID, r.DiskID})
	if string(raw) != string(want) {
		return errors.New("managed chat native ownership marker mismatch")
	}
	for _, name := range []string{"config", "data"} {
		if err := safepath.VerifyPrivateDirectory(s.Disk, filepath.Join(base, "server", name), s.UID, s.GID); err != nil {
			return err
		}
	}
	for _, m := range append([]Member{r.Human}, r.Members...) {
		if m.Revoked {
			continue
		}
		rel, _ := layout.ChatMemberRelative(r.ID, m.ID)
		if err := safepath.VerifyPrivateDirectory(s.Disk, rel, s.UID, s.GID); err != nil {
			return err
		}
		raw, err := privateFile(s.Disk, filepath.Join(rel, "token"), 128, s.UID, s.GID)
		if err != nil {
			return err
		}
		if decoded, err := hex.DecodeString(string(raw)); err != nil || len(decoded) != 32 {
			return errors.New("invalid managed chat credential file")
		}
	}
	return nil
}
