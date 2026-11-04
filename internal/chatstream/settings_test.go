package chatstream

import (
	"testing"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestRoomHeaderKeepsNicknameSeparateFromAttachIdentity(t *testing.T) {
	r := &managedchat.Room{ID: managedchat.NewID(), DiskID: managedchat.NewID(), Generation: 4, Channel: "#space",
		Human:   managedchat.Member{ID: managedchat.NewID(), Name: "person", Nickname: "newnick"},
		Members: []managedchat.Member{{ID: managedchat.NewID(), BoxID: managedchat.NewID(), Name: "coder"}},
	}
	h, err := RoomHeader(r)
	if err != nil || h.Nick != "newnick" || h.Identities[0].Nick != "newnick" || h.Identities[0].Name != "person" || h.Identities[0].BoxID != "" || h.Identities[1].BoxID != r.Members[0].BoxID {
		t.Fatal("trusted header lost nickname/box identity distinction", err)
	}
	// The packaged JSONL client has no NICK command. Persistence goes through
	// a separate admitted RPC, followed by an explicit view reconnect.
	if _, err := DecodeRequest([]byte(`{"id":"rename","op":"nick","nick":"newnick"}`), "#space"); err == nil {
		t.Fatal("unsupported live nickname operation escaped allowlist")
	}
}
