// Package controller implements the VM-owned space control protocol. Public
// methods are explicitly registered; the wire protocol is not a shell or a
// filesystem proxy.
package controller

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/termio"
)

const ProtocolVersion = 2
const MaxFrame = 4 << 20
const MaxDocument = 1 << 20

// Request identifies one registered operation. Flags preserve repeated values
// and the distinction between omitted and explicitly empty CLI options.
type Request struct {
	ID       string              `json:"id"`
	Method   string              `json:"method"`
	Space    string              `json:"space"`
	Args     []string            `json:"args,omitempty"`
	Flags    map[string][]string `json:"flags,omitempty"`
	Cwd      string              `json:"cwd,omitempty"`
	Terminal bool                `json:"terminal,omitempty"`
	View     bool                `json:"view,omitempty"`
	Cols     uint16              `json:"cols,omitempty"`
	Rows     uint16              `json:"rows,omitempty"`
	Term     string              `json:"term,omitempty"`
	Payload  json.RawMessage     `json:"payload,omitempty"`
	Session  *SessionPreferences `json:"session,omitempty"`
	// Dash preserves the positional boundary for box exec and space ssh.
	Dash int `json:"dash"`
}

// Holder tuning and terminal identity travel to the VM. Host integration policy
// stays host-owned; these fields grant no clipboard or browser access.
type SessionPreferences struct {
	Terminal      termio.TerminalIdentity `json:"terminal,omitempty"`
	RingBytes     int                     `json:"ring_bytes,omitempty"`
	TitleTemplate string                  `json:"title_template,omitempty"`
	// AgentIcons maps agent name to the {agent-icon} title text.
	AgentIcons map[string]string `json:"agent_icons,omitempty"`
}

type Hello struct {
	Protocol   int    `json:"protocol"`
	Bundle     string `json:"bundle"`
	Space      string `json:"space"`
	Generation string `json:"generation"`
}

type Frame struct {
	ImageRetryJob string   `json:"image_retry_job,omitempty"`
	Type          string   `json:"type"`
	Hello         *Hello   `json:"hello,omitempty"`
	Request       *Request `json:"request,omitempty"`
	Data          []byte   `json:"data,omitempty"`
	Status        int      `json:"status,omitempty"`
	Error         string   `json:"error,omitempty"`
	Cols          uint16   `json:"cols,omitempty"`
	Rows          uint16   `json:"rows,omitempty"`
	Signal        int      `json:"signal,omitempty"`
	// Owned marks a "detached" frame whose authorized setup continues.
	Owned bool `json:"owned,omitempty"`
}

type Wire struct {
	r  io.Reader
	w  io.Writer
	mu sync.Mutex
}

func NewWire(r io.Reader, w io.Writer) *Wire { return &Wire{r: r, w: w} }

func (w *Wire) Read() (Frame, error) {
	var f Frame
	var header [4]byte
	if _, err := io.ReadFull(w.r, header[:]); err != nil {
		return f, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > MaxFrame {
		return f, errors.New("invalid controller frame size")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(w.r, raw); err != nil {
		return f, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return f, errors.New("invalid controller frame")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return f, errors.New("trailing controller frame data")
	}
	return f, nil
}

func (w *Wire) Write(f Frame) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(raw) > MaxFrame {
		return errors.New("controller frame exceeds limit")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	if err := writeAll(w.w, header[:]); err != nil {
		return err
	}
	return writeAll(w.w, raw)
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func MatchHello(local, remote Hello) error {
	if local.Protocol != ProtocolVersion || remote.Protocol != ProtocolVersion {
		return fmt.Errorf("controller protocol mismatch (client %d, controller %d)", local.Protocol, remote.Protocol)
	}
	if local.Space == "" || local.Space != remote.Space {
		return errors.New("controller space identity mismatch")
	}
	if local.Bundle == "" || local.Bundle != remote.Bundle {
		return fmt.Errorf("controller bundle mismatch (client %s, controller %s); use the selected host yoloctl build to update the controller", local.Bundle, remote.Bundle)
	}
	if local.Generation != "" && local.Generation != remote.Generation {
		return errors.New("controller generation changed; reconnect before retrying")
	}
	return nil
}

type Principal struct {
	Box         string `json:"box,omitempty"`
	Incarnation string `json:"incarnation,omitempty"`
}

func (p Principal) Admin() bool { return p.Box == "" }

// Operation is registered by the shared CLI/service layer. No prefix wildcard
// gives future commands authority automatically.
type Operation struct {
	BoxAllowed bool
	Mutates    bool
	// EffectGated requires a durable private checkpoint before any effects.
	// Unconverted operations retain conservative interrupted-outcome handling.
	EffectGated bool
	// OwnedSetup lets an effect-gated setup outlive its client: once effects
	// are authorized, a vanishing client detaches instead of canceling it.
	OwnedSetup bool
	// Footprint is the method's policy for the resource kinds a worker may
	// declare it writes. Zero accepts any kind; a global scope always fits.
	Footprint Footprint
}

// Footprint is a set of resource kinds.
type Footprint uint8

const (
	FootprintBox Footprint = 1 << iota
	FootprintAccount
	FootprintChat
	FootprintImages
	FootprintPorts
	FootprintAudit
)

var footprintKinds = map[string]Footprint{
	ResourceBox: FootprintBox, ResourceAccount: FootprintAccount, ResourceChat: FootprintChat,
	ResourceImages: FootprintImages, ResourcePorts: FootprintPorts, ResourceAudit: FootprintAudit,
}

// allows checks declared writes against the method policy. A worker cannot
// widen what a method may change by declaring an unrelated resource kind.
func (f Footprint) allows(writes []Resource) error {
	if f == 0 {
		return nil
	}
	for _, w := range writes {
		if footprintKinds[w.Kind]&f == 0 {
			return fmt.Errorf("operation cannot declare a write to %s", w.Kind)
		}
	}
	return nil
}

func Authorize(p Principal, req Request, space string, operations map[string]Operation) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if req.Space != space {
		return errors.New("request targets a different space")
	}
	op, ok := operations[req.Method]
	if !ok {
		return fmt.Errorf("unsupported controller operation %q", req.Method)
	}
	if op.EffectGated && !op.Mutates {
		return errors.New("invalid controller effect-gate registration")
	}
	if !p.Admin() {
		if p.Incarnation == "" || !op.BoxAllowed {
			return errors.New("box operator is not authorized for this operation")
		}
		for _, flag := range []string{"space", "from-space"} {
			for _, value := range req.Flags[flag] {
				if value != "" && value != space {
					return errors.New("box operator cannot access another space")
				}
			}
		}
		if values := req.Flags["all-spaces"]; len(values) > 0 && values[len(values)-1] != "false" {
			return errors.New("box operator cannot access all spaces")
		}
	}
	return nil
}

func (r Request) Validate() error {
	if r.View && !ManagedSessionMethod(r.Method) {
		return errors.New("session view requires a managed attach operation")
	}
	if len(r.ID) < 16 || len(r.ID) > 80 {
		return errors.New("invalid controller request ID")
	}
	for _, ch := range r.ID {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-':
		default:
			return errors.New("invalid controller request ID")
		}
	}
	if len(r.Method) > 128 || len(r.Args) > 1024 || len(r.Flags) > 128 ||
		len(r.Payload) > 2*MaxDocument || len(r.Term) > 128 || r.Dash < -1 || r.Dash > len(r.Args) {
		return errors.New("controller request exceeds limits")
	}
	if len(r.Cwd) > 4096 || !utf8.ValidString(r.Cwd) || bytes.ContainsRune([]byte(r.Cwd), 0) ||
		!utf8.ValidString(r.Term) || bytes.ContainsRune([]byte(r.Term), 0) {
		return errors.New("invalid controller working directory or terminal")
	}
	if r.Session != nil && (r.Session.RingBytes < 0 || r.Session.RingBytes > 64<<20 || len(r.Session.TitleTemplate) > 4096) {
		return errors.New("session tuning exceeds limits (replay buffer maximum: 64 MiB)")
	}
	if r.Session != nil {
		if !r.Session.Terminal.Valid() {
			return errors.New("invalid session terminal identity")
		}
		for agent, icon := range r.Session.AgentIcons {
			if len(r.Session.AgentIcons) > 256 || len(agent) > 128 || len(icon) > 64 {
				return errors.New("session agent icons exceed limits")
			}
		}
	}
	for _, arg := range r.Args {
		if len(arg) > 65536 || !utf8.ValidString(arg) || bytes.ContainsRune([]byte(arg), 0) {
			return errors.New("invalid controller argument")
		}
	}
	for name, values := range r.Flags {
		if len(name) > 128 || len(values) > 1024 {
			return errors.New("controller flags exceed limits")
		}
		for _, value := range values {
			if len(value) > 65536 || !utf8.ValidString(value) || bytes.ContainsRune([]byte(value), 0) {
				return errors.New("invalid controller flag")
			}
		}
	}
	return nil
}
