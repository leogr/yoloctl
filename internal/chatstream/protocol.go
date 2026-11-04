// Package chatstream adapts the confined AIRC JSONL v2 client. It does not
// implement IRC, retain credentials, reconnect, or replay messages.
package chatstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/managedchat"
)

const MaxMessageBytes = 64 << 10
const RequestLimit = 512 << 10

// AIRC can receive 256 KiB logical messages from other clients. JSON escaping
// can expand those bytes sixfold; a single event still fits this bound.
const ResponseLimit = 2 << 20

type Header struct {
	Event   string `json:"event"`
	Channel string `json:"channel"`
	Nick    string `json:"nick"`
	// Attachments: the controller's confined client has the AIRC
	// message-attachments feature, so sends may carry attach.
	Attachments bool `json:"attachments,omitempty"`
	managedchat.Bootstrap
}

func RoomHeader(r *managedchat.Room) (Header, error) {
	account, err := managedchat.AuthAccount(r.Human.ID)
	if err != nil {
		return Header{}, err
	}
	nicks, err := managedchat.RoomNicknames(r)
	if err != nil {
		return Header{}, err
	}
	h := Header{Event: "managed-room", Channel: r.Channel, Nick: nicks[r.Human.ID], Bootstrap: managedchat.Bootstrap{
		Schema: 1, RoomID: r.ID, DiskID: r.DiskID, Generation: r.Generation, HumanAccount: account, SelfAccount: account,
	}}
	if r.Schema == 2 {
		h.Schema, h.DefaultChannel, h.Channels = 2, r.Channel, r.ChannelCatalog()
		h.DeletedChannels = slices.Clone(r.DeletedChannels)
	}
	for _, m := range append([]managedchat.Member{r.Human}, r.Members...) {
		a, err := managedchat.AuthAccount(m.ID)
		if err != nil {
			return Header{}, err
		}
		role := "member"
		if m.ID == r.Human.ID {
			role = "human"
		}
		h.Identities = append(h.Identities, managedchat.BootstrapIdentity{ID: m.ID, Account: a, Name: m.Name, Nick: nicks[m.ID], BoxID: m.BoxID, Role: role, Revoked: m.Revoked})
	}
	return h, nil
}

type Request struct {
	ID        string   `json:"id"`
	Op        string   `json:"op"`
	After     uint64   `json:"after,omitempty"`
	Limit     uint64   `json:"limit,omitempty"`
	WaitMS    uint64   `json:"wait_ms,omitempty"`
	Target    string   `json:"target,omitempty"`
	Text      string   `json:"text,omitempty"`
	ReplyTo   string   `json:"reply_to,omitempty"`
	Channel   string   `json:"channel,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Reference string   `json:"reference,omitempty"`
	Active    *bool    `json:"active,omitempty"`
	Topic     *string  `json:"-"` // topic missing queries; pointer to empty clears
	Attach    []Attach `json:"attach,omitempty"`
}

func (r Request) MarshalJSON() ([]byte, error) {
	type wire Request
	if r.Op != "topic" {
		return json.Marshal(wire(r))
	}
	return json.Marshal(struct {
		ID      string  `json:"id"`
		Op      string  `json:"op"`
		Channel string  `json:"channel"`
		Text    *string `json:"text,omitempty"`
	}{r.ID, r.Op, r.Channel, r.Topic})
}

func validReference(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// DecodeRequest is the controller's allowlist. Canonical re-encoding means
// unknown fields, file paths, arbitrary IRC operations and other targets never
// reach a human-authenticated process with access to the server's private data.
func DecodeRequest(line []byte, channel string) (Request, error) {
	return decodeChannelRequest(line, func(name string) bool { return name == channel }, false)
}

// attachments allows a send's attach list only when the confined client
// advertised the feature; an unknown field would make its outcome uncertain.
func decodeChannelRequest(line []byte, allowedChannel func(string) bool, attachments bool) (Request, error) {
	var r Request
	bad := errors.New("invalid or unsupported managed chat stream request")
	if len(line) > RequestLimit || !utf8.Valid(line) {
		return r, bad
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil || json.Unmarshal(line, &r) != nil || !validReference(r.ID, 80) {
		return r, bad
	}
	allowed := "id op"
	allowedTarget := func(name string) bool {
		return managedchat.ValidateChannelName(name) == nil && allowedChannel(name)
	}
	switch r.Op {
	case "read":
		allowed += " after limit wait_ms"
		if r.Limit != 1 || r.WaitMS > 1000 {
			return r, bad
		}
	case "send":
		allowed += " target text reply_to"
		if attachments {
			allowed += " attach"
		}
		if !allowedTarget(r.Target) || len(r.Text) == 0 || len(r.Text) > MaxMessageBytes || strings.ContainsAny(r.Text, "\x00\r") || (r.ReplyTo != "" && !validReference(r.ReplyTo, 128)) {
			return r, bad
		}
		if _, present := fields["attach"]; present && !ValidAttach(r.Text, r.Attach) {
			return r, bad
		}
	case "history":
		allowed += " channel direction reference limit"
		if !allowedTarget(r.Channel) || r.Limit < 1 || r.Limit > 100 || !validReference(r.Reference, 160) {
			return r, bad
		}
		if r.Direction != "LATEST" && r.Direction != "BEFORE" && r.Direction != "AFTER" && r.Direction != "AROUND" {
			return r, bad
		}
		if r.Reference != "*" && !strings.HasPrefix(r.Reference, "msgid=") && !strings.HasPrefix(r.Reference, "timestamp=") {
			return r, bad
		}
	case "members", "who", "join", "part":
		allowed += " channel"
		if !allowedTarget(r.Channel) {
			return r, bad
		}
	case "typing":
		allowed += " channel active"
		if !allowedTarget(r.Channel) || r.Active == nil {
			return r, bad
		}
	case "topic":
		allowed += " channel text"
		if !allowedTarget(r.Channel) {
			return r, bad
		}
		if text, present := fields["text"]; present {
			if json.Unmarshal(text, &r.Text) != nil || len(r.Text) > 200 || strings.ContainsAny(r.Text, "\x00\r\n") {
				return r, bad
			}
			r.Topic = &r.Text
		}
	case "quit":
	default:
		return r, bad
	}
	for key, value := range fields {
		if !strings.Contains(" "+allowed+" ", " "+key+" ") || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return r, bad
		}
	}
	return r, nil
}

// Filter forwards only complete canonical requests. A partial or oversized
// request closes the client stream; it is never forwarded or retried.
func Filter(in io.Reader, out io.Writer, channel string) error {
	return FilterChannels(in, out, func(name string) error {
		if name != channel {
			return errors.New("channel is outside the managed room")
		}
		return nil
	}, false)
}

// FilterChannels revalidates current native membership for each channel-bearing
// request. The callback releases its bounded native read lock before forwarding;
// reads/quit carry no channel and do not take a per-poll storage lock.
// attachments forwards a send's attach list only when the confined client has
// the AIRC message-attachments feature.
func FilterChannels(in io.Reader, out io.Writer, authorize func(string) error, attachments bool) error {
	if authorize == nil {
		return errors.New("managed chat channel authorization is unavailable")
	}
	r := bufio.NewReaderSize(in, RequestLimit+1)
	for {
		line, err := r.ReadSlice('\n')
		if err == io.EOF && len(line) == 0 {
			return nil
		}
		if err != nil || len(line) > RequestLimit {
			return errors.New("incomplete or oversized managed chat request")
		}
		request, err := decodeChannelRequest(bytes.TrimSpace(line), func(string) bool { return true }, attachments)
		if err != nil {
			return err
		}
		channel := request.Channel
		if request.Op == "send" {
			channel = request.Target
		}
		if channel != "" {
			if err := authorize(channel); err != nil {
				return err
			}
		}
		if err := json.NewEncoder(out).Encode(request); err != nil {
			return err
		}
	}
}
