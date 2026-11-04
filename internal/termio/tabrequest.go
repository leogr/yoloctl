package termio

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/leogr/yoloctl/internal/layout"
)

// TabRequestFDEnv names the private channel a TUI gives only to `space view`
// and native `chat` tabs. The view uses it to ask for a new attach tab
// instead of taking over its own tab. Requests are typed: they carry a space
// and box name (plus, from chat, the participant's immutable identities),
// never a command line, and the TUI validates them before opening anything.
const TabRequestFDEnv = "YOLOCTL_TUI_REQUEST_FD"

// maxTabRequest bounds one request line.
const maxTabRequest = 1024

type TabRequest struct {
	Op    string `json:"op"` // "attach"
	Space string `json:"space"`
	Box   string `json:"box"`
	// A chat participant attach names the room, member and box identities
	// the click resolved; `box attach` re-validates them under the native
	// lock, so revocation or name reuse cannot redirect it. ChatAllowStart
	// records the human's confirmation to start a stopped box.
	ChatRoomID     string `json:"chat_room_id,omitempty"`
	ChatMemberID   string `json:"chat_member_id,omitempty"`
	ChatBoxID      string `json:"chat_box_id,omitempty"`
	ChatAllowStart bool   `json:"chat_allow_start,omitempty"`
}

// Chat reports whether the request carries any chat attachment field.
func (r TabRequest) Chat() bool {
	return r.ChatRoomID != "" || r.ChatMemberID != "" || r.ChatBoxID != "" || r.ChatAllowStart
}

// ValidateChat accepts only a complete guarded chat attach: valid names and
// all three identities. A partial guard is never downgraded to a plain attach.
func (r TabRequest) ValidateChat() error {
	if r.Op != "attach" || layout.ValidateName("space", r.Space) != nil || layout.ValidateName("agentbox", r.Box) != nil {
		return errors.New("invalid chat attach target")
	}
	for _, id := range []string{r.ChatRoomID, r.ChatMemberID, r.ChatBoxID} {
		if layout.ValidateIdentity(id) != nil {
			return errors.New("chat attach requires valid room, member and box identities")
		}
	}
	return nil
}

// ChatAttachArgs is the guarded `box attach` argv for a validated request.
func (r TabRequest) ChatAttachArgs() []string {
	args := []string{"box", "attach", r.Box, "--space", r.Space, "--chat-room-id", r.ChatRoomID, "--chat-member-id", r.ChatMemberID, "--chat-box-id", r.ChatBoxID}
	if r.ChatAllowStart {
		args = append(args, "--chat-allow-start")
	}
	return args
}

// TabRequestPair returns the TUI end and the child end of a new channel.
func TabRequestPair() (*os.File, *os.File, error) { return ViewPair() }

// InheritedTabRequests opens the channel named by TabRequestFDEnv, or returns
// nil when the process was not started by a TUI tab that offers one.
func InheritedTabRequests() (*os.File, error) {
	value := os.Getenv(TabRequestFDEnv)
	if value == "" {
		return nil, nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 || fd > 1024 {
		return nil, errors.New("invalid tab request descriptor")
	}
	return ViewFile(fd)
}

func WriteTabRequest(w io.Writer, r TabRequest) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) >= maxTabRequest {
		return errors.New("tab request too large")
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// ReadTabRequests delivers requests until the channel closes or a line is
// malformed or oversized.
func ReadTabRequests(r io.Reader, handle func(TabRequest)) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, maxTabRequest), maxTabRequest)
	for s.Scan() {
		var req TabRequest
		if err := json.Unmarshal(s.Bytes(), &req); err != nil {
			return err
		}
		handle(req)
	}
	return s.Err()
}
