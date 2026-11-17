package termio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/layout"
)

// TabRequestFDEnv names the private channel a TUI gives only to `space view`
// and native `chat`/`follow` tabs, or a lifecycle tab recording its resume target.
// The view uses it to ask for a new attach tab
// instead of taking over its own tab. Requests are typed: they carry a space
// and box name (plus, from chat, the participant's immutable identities),
// or a pinned file mode/path/root,
// never a command line, and the TUI validates them before opening anything.
const TabRequestFDEnv = "YOLOCTL_TUI_REQUEST_FD"

// maxTabRequest bounds one request line.
const maxTabRequest = 1024

type TabRequest struct {
	FollowBounded bool   `json:"follow_bounded,omitempty"`
	FollowMode    string `json:"follow_mode,omitempty"`
	FollowPath    string `json:"follow_path,omitempty"`
	FollowRoot    string `json:"follow_root,omitempty"`
	FollowBase    string `json:"follow_base,omitempty"`
	Op            string `json:"op"` // attach, resume-target, follow-context or follow
	Space         string `json:"space"`
	Box           string `json:"box"`
	// A chat participant attach names the room, member and box identities
	// the click resolved; `box attach` re-validates them under the native
	// lock, so revocation or name reuse cannot redirect it. ChatAllowStart
	// records the human's confirmation to start a stopped box.
	ChatRoomID     string `json:"chat_room_id,omitempty"`
	ChatMemberID   string `json:"chat_member_id,omitempty"`
	ChatBoxID      string `json:"chat_box_id,omitempty"`
	ChatAllowStart bool   `json:"chat_allow_start,omitempty"`
	NoAutoupdate   bool   `json:"no_autoupdate,omitempty"`
	Passthrough    bool   `json:"passthrough,omitempty"`
}

// ResumeTargetKind is selected by the host from the original command class;
// a message can supply names, never select a command or carry argv.
type ResumeTargetKind uint8

const (
	ResumeTargetNone ResumeTargetKind = iota
	ResumeTargetBox
	ResumeTargetSpace
)

func (r TabRequest) ValidateResumeTarget(kind ResumeTargetKind) error {
	if r.Op != "resume-target" || r.Follow() || r.Chat() || layout.ValidateExistingSpaceName(r.Space) != nil {
		return errors.New("invalid resume target")
	}
	switch kind {
	case ResumeTargetBox:
		if layout.ValidateName("agentbox", r.Box) == nil {
			return nil
		}
	case ResumeTargetSpace:
		if r.Box == "" && !r.NoAutoupdate && !r.Passthrough {
			return nil
		}
	}
	return errors.New("resume target does not match command class")
}

// Chat reports whether the request carries any chat attachment field.
func (r TabRequest) Chat() bool {
	return r.ChatRoomID != "" || r.ChatMemberID != "" || r.ChatBoxID != "" || r.ChatAllowStart
}

// ValidateChat accepts only a complete guarded chat attach: valid names and
// all three identities. A partial guard is never downgraded to a plain attach.
func (r TabRequest) ValidateChat() error {
	if r.Op != "attach" || r.Follow() || r.NoAutoupdate || r.Passthrough || layout.ValidateExistingSpaceName(r.Space) != nil || layout.ValidateName("agentbox", r.Box) != nil {
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
	fd, err := inheritedTabRequestFD()
	if err != nil || fd == 0 {
		return nil, err
	}
	return ViewFile(fd)
}

func inheritedTabRequestFD() (int, error) {
	value := os.Getenv(TabRequestFDEnv)
	if value == "" {
		return 0, nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 || fd > 1024 {
		return 0, errors.New("invalid tab request descriptor")
	}
	return fd, nil
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
		d := json.NewDecoder(bytes.NewReader(s.Bytes()))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			return err
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return errors.New("invalid tab request framing")
		}
		handle(req)
	}
	return s.Err()
}

// Follow fields are never downgraded to attach or resume metadata.
func (r TabRequest) Follow() bool {
	return r.FollowBounded || r.FollowMode != "" || r.FollowPath != "" || r.FollowRoot != "" || r.FollowBase != ""
}
func (r TabRequest) NavigationContext() (filelink.Context, error) {
	c := filelink.Context{Mode: r.FollowMode, Space: r.Space, Root: r.FollowRoot, Base: r.FollowBase}
	if r.Op != "follow-context" || r.FollowBounded && r.FollowPath == "" || r.Box != "" || r.Chat() || r.NoAutoupdate || r.Passthrough {
		return c, errors.New("invalid navigation context")
	}
	if r.FollowPath != "" && (!filelink.CleanAbsolute(r.FollowPath) || !filelink.Within(r.FollowPath, c.Root) || filepath.Dir(r.FollowPath) != c.Base) {
		return c, errors.New("invalid source file context")
	}
	return c, c.Validate()
}
func (r TabRequest) FollowTarget() (filelink.Target, error) {
	t := filelink.Target{Mode: r.FollowMode, Space: r.Space, Root: r.FollowRoot, Path: r.FollowPath}
	if r.Op != "follow" || r.FollowBounded || r.FollowBase != "" || r.Box != "" || r.Chat() || r.NoAutoupdate || r.Passthrough || !filelink.CleanAbsolute(t.Path) {
		return t, errors.New("invalid follow request")
	}
	c := filelink.Context{Mode: t.Mode, Space: t.Space, Root: t.Root, Base: t.Root}
	if !c.Accept(t) {
		return t, errors.New("invalid follow boundary")
	}
	return t, nil
}
func NavigationRegistration(c filelink.Context) TabRequest {
	return TabRequest{Op: "follow-context", Space: c.Space, FollowMode: c.Mode, FollowRoot: c.Root, FollowBase: c.Base}
}
func FollowRequest(t filelink.Target) TabRequest {
	return TabRequest{Op: "follow", Space: t.Space, FollowMode: t.Mode, FollowRoot: t.Root, FollowPath: t.Path}
}
