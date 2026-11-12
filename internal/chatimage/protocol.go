package chatimage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/termio"
)

// ChannelFDEnv names the private image control channel a TUI gives only to a
// native chat pane. It carries small typed controls, never image bytes,
// paths, URLs, argv or terminal sequences; the host fetches and draws.
const ChannelFDEnv = "YOLOCTL_TUI_IMAGE_FD"

// ProtocolVersion is the only accepted frame version.
const ProtocolVersion = 1

// MaxFrame bounds one encoded control frame.
const MaxFrame = 16 << 10

// Bounds an image request may name.
const (
	MaxArtifactBytes = 4 << 20
	maxTokenBytes    = 64
)

// Operations. Host → child: capability, ready, error. Child → host: preview,
// release.
const (
	OpCapability = "capability"
	OpReady      = "ready"
	OpError      = "error"
	OpPreview    = "preview"
	OpRelease    = "release"
)

// Error codes a host may report for a preview request.
var errorCodes = map[string]bool{
	"unsupported": true, "busy": true, "not_png": true, "too_large": true,
	"unavailable": true, "denied": true, "failed": true, "stale": true,
}

// Frame is one control message. Only the fields of its operation may be set;
// Validate checks that and every field's format.
type Frame struct {
	V          int    `json:"v"`
	Op         string `json:"op"`
	Generation uint64 `json:"generation,omitempty"`
	Request    uint64 `json:"request,omitempty"`

	// capability
	Supported bool `json:"supported,omitempty"`
	MaxCols   int  `json:"max_cols,omitempty"`
	MaxRows   int  `json:"max_rows,omitempty"`

	// preview: the authenticated message's identities and artifact tuple.
	RoomID    string `json:"room_id,omitempty"`
	DiskID    string `json:"disk_id,omitempty"`
	HumanID   string `json:"human_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Artifact  string `json:"artifact,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	// Chip is the attachment chip text the pane shows, recorded for copy.
	Chip string `json:"chip,omitempty"`

	// ready / release
	Token string `json:"token,omitempty"`
	ID    uint32 `json:"id,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`

	// error
	Code string `json:"code,omitempty"`
}

// FromHost reports whether op is sent by the host.
func FromHost(op string) bool { return op == OpCapability || op == OpReady || op == OpError }

// Validate checks the frame's version, operation and fields. Fields that do
// not belong to the operation must be empty.
func (f Frame) Validate() error {
	if f.V != ProtocolVersion {
		return fmt.Errorf("image control version %d", f.V)
	}
	var allowed map[string]bool
	switch f.Op {
	case OpCapability:
		allowed = set("Generation", "Supported", "MaxCols", "MaxRows")
		if f.Generation == 0 || f.Supported && (f.MaxCols < 1 || f.MaxCols > 512 || f.MaxRows < 1 || f.MaxRows > 256) {
			return errors.New("invalid image capability")
		}
	case OpPreview:
		allowed = set("Generation", "Request", "RoomID", "DiskID", "HumanID", "ChannelID", "Channel", "MessageID", "Artifact", "Bytes", "SHA256", "Chip")
		if f.Generation == 0 || f.Request == 0 {
			return errors.New("image preview needs a generation and a request")
		}
		for _, id := range []string{f.RoomID, f.DiskID, f.HumanID, f.ChannelID} {
			if layout.ValidateIdentity(id) != nil {
				return errors.New("image preview needs valid room, disk, human and channel identities")
			}
		}
		if managedchat.ValidateChannelName(f.Channel) != nil || !chatstream.ValidArtifactID(f.MessageID) {
			return errors.New("image preview needs a valid channel and message")
		}
		if !chatstream.ValidArtifactID(f.Artifact) || !chatstream.ValidArtifactHash(f.SHA256) || f.Bytes < 1 || f.Bytes > MaxArtifactBytes {
			return errors.New("image preview needs a valid artifact within the size limit")
		}
		if !validChip(f.Chip) {
			return errors.New("image preview needs a printable chip of at most 512 bytes")
		}
	case OpReady:
		allowed = set("Generation", "Request", "Token", "ID", "Cols", "Rows")
		if f.Generation == 0 || f.Request == 0 || !validToken(f.Token) || f.ID == 0 || f.ID > 0xffffff ||
			f.Cols < 1 || f.Cols > 512 || f.Rows < 1 || f.Rows > 256 {
			return errors.New("invalid image ready")
		}
	case OpError:
		allowed = set("Generation", "Request", "Code")
		if f.Generation == 0 || f.Request == 0 || !errorCodes[f.Code] {
			return errors.New("invalid image error")
		}
	case OpRelease:
		allowed = set("Token")
		if !validToken(f.Token) {
			return errors.New("invalid image release")
		}
	default:
		return fmt.Errorf("unknown image control operation %q", f.Op)
	}
	for name, set := range f.present() {
		if set && !allowed[name] {
			return fmt.Errorf("image control %s carries %s", f.Op, name)
		}
	}
	return nil
}

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func (f Frame) present() map[string]bool {
	return map[string]bool{
		"Generation": f.Generation != 0, "Request": f.Request != 0,
		"Supported": f.Supported, "MaxCols": f.MaxCols != 0, "MaxRows": f.MaxRows != 0,
		"RoomID": f.RoomID != "", "DiskID": f.DiskID != "", "HumanID": f.HumanID != "", "ChannelID": f.ChannelID != "",
		"Channel": f.Channel != "", "MessageID": f.MessageID != "", "Artifact": f.Artifact != "",
		"Bytes": f.Bytes != 0, "SHA256": f.SHA256 != "", "Chip": f.Chip != "",
		"Token": f.Token != "", "ID": f.ID != 0, "Cols": f.Cols != 0, "Rows": f.Rows != 0,
		"Code": f.Code != "",
	}
}

// validChip accepts one printable line of 1–512 bytes without placeholders.
func validChip(s string) bool {
	if s == "" || len(s) > maxChipBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == Placeholder {
			return false
		}
	}
	return true
}

// validToken accepts a host-issued opaque token: 1–64 bytes of [A-Za-z0-9_-].
func validToken(s string) bool {
	if len(s) < 1 || len(s) > maxTokenBytes {
		return false
	}
	for _, c := range []byte(s) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// ErrFraming means the peer sent a malformed, oversized or invalid frame. The
// channel must then be closed: previews stop working, chat does not.
var ErrFraming = errors.New("chatimage: invalid image control frame")

// Conn is one end of the control channel. Writes are serialized; reads come
// from one reader.
type Conn struct {
	rw     io.ReadWriter
	host   bool // this end is the host
	mu     sync.Mutex
	closer io.Closer
}

// NewConn wraps an end of the channel. host is true on the TUI's end.
func NewConn(rw io.ReadWriter, host bool) *Conn {
	c := &Conn{rw: rw, host: host}
	if cl, ok := rw.(io.Closer); ok {
		c.closer = cl
	}
	return c
}

// Send validates and writes one frame this end may send.
func (c *Conn) Send(f Frame) error {
	f.V = ProtocolVersion
	if FromHost(f.Op) != c.host {
		return fmt.Errorf("chatimage: %s is not sent by this end", f.Op)
	}
	if err := f.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(raw) > MaxFrame {
		return errors.New("chatimage: control frame too large")
	}
	buf := make([]byte, 4, 4+len(raw))
	binary.BigEndian.PutUint32(buf, uint32(len(raw)))
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.rw.Write(append(buf, raw...))
	return err
}

// Receive reads the next frame sent by the other end: io.EOF at a clean end,
// ErrFraming for anything malformed, oversized, of the wrong direction or
// invalid.
func (c *Conn) Receive() (Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Frame{}, io.EOF
		}
		return Frame{}, ErrFraming
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrame {
		return Frame{}, ErrFraming
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(c.rw, raw); err != nil {
		return Frame{}, ErrFraming
	}
	var f Frame
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&f) != nil {
		return Frame{}, ErrFraming
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Frame{}, ErrFraming
	}
	if FromHost(f.Op) == c.host || f.Validate() != nil {
		return Frame{}, ErrFraming
	}
	return f, nil
}

// Close closes the underlying channel end, if it is closable.
func (c *Conn) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

// ChannelPair returns the host end and the child end of a new channel. Both
// are close-on-exec; the host passes the child end explicitly.
func ChannelPair() (*os.File, *os.File, error) { return termio.ViewPair() }

// InheritedChannel claims the channel named by ChannelFDEnv in a native chat
// child and removes the variable, so no helper the child starts learns or
// inherits it. It returns nil when the process was not given one.
func InheritedChannel() (*os.File, error) {
	value, ok := os.LookupEnv(ChannelFDEnv)
	if !ok {
		return nil, nil
	}
	_ = os.Unsetenv(ChannelFDEnv)
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 || fd > 1024 {
		return nil, errors.New("invalid image channel descriptor")
	}
	return termio.ViewFile(fd)
}
