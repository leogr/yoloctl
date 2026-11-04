package session

import (
	"bytes"
	"fmt"
	"strings"
)

// titleRewriter composes the box identity into the window/icon titles the
// agent sets, so a client — a TUI tab or a direct `yoloctl box attach` in
// any terminal — shows e.g. "<agentbox>: <agent title>" instead of the
// agent's bare title. The composition lives where the title is set (the
// holder owns the agent's PTY stream and knows the agentbox), so every
// attach path gets it for free and the TUI keeps showing whatever title
// the stream carries.
//
// The shape comes from tui.yaml `session.box_title_template`, expanded
// host-side by ExpandBoxTitleTemplate into literal text around at most
// one {title}. The rewriter decomposes that into a prefix and suffix
// around the agent's own title text and picks the cheapest mechanism:
//
//   - "{title}" alone: identity — no rewriting at all;
//   - suffix empty (the default "<box>: {title}"): a streaming filter
//     that rewrites ONLY the OSC icon/window-title introducers
//     (ESC ] 0/1/2 ;) by inserting the prefix right after the ';' and
//     letting the title text stream through byte-for-byte;
//   - a suffix, or no {title} at all (replacement): the title text is
//     buffered to its terminator (BEL or ST) so literal text can land
//     after — or instead of — it. The buffer is bounded by trMaxTitle;
//     on overflow, stream end, or a malformed sequence the rewrite is
//     abandoned and the held bytes flush raw. Bytes are never dropped.
//
// A template with {agent-icon} also passes that icon as strip: every
// title that starts with it loses the icon and the spaces after it, so
// the icon does not show twice (Claude Code prefixes its own titles with
// "✳"). Stripping inspects the title text, so it uses buffered mode.
//
// Every other byte — CSI, other OSC (hyperlinks, clipboard, working-dir),
// plain output — passes through byte-for-byte. A sequence split across
// PTY reads is handled by carrying pending state between Rewrite calls.
// Used single-threaded from the holder's output pump.
type titleRewriter struct {
	prefix   []byte // template text before {title}, or the whole template when replacing
	suffix   []byte // template text after {title} (buffered mode only)
	strip    []byte // leading text removed from each agent title (the agent icon)
	dropBody bool   // no {title} in the template: the agent's text is replaced
	buffered bool   // suffix, strip or dropBody: hold the title text to its terminator
	identity bool   // template is exactly "{title}": pass everything through

	state   trState
	pending []byte // bytes held mid-introducer: ESC, then ']', then the numeric code
	body    []byte // title text held to its terminator (buffered mode)
}

type trState int

const (
	trNormal   trState = iota // passing bytes through
	trEsc                     // saw ESC, awaiting ']'
	trOSC                     // saw "ESC ]", reading the numeric code up to ';'
	trTitle                   // buffering a title's text up to its terminator
	trTitleEsc                // saw ESC inside a buffered title: ST ("ESC \") ends it
)

// trMaxCode caps the code run we'll buffer before giving up (an introducer
// is "ESC ] <digits> ;"; real OSC codes are 1-4 digits). Guards a stray
// "ESC ]" that never reaches a ';'.
const trMaxCode = 8

// trMaxTitle caps a buffered title's text. Real titles are far smaller; a
// pathological or unterminated one is flushed raw at the cap and streams
// untouched from there (that title just loses the composition).
const trMaxTitle = 1024

// newTitleRewriter is the default composition: "<agentbox>: <title>".
func newTitleRewriter(agentbox string) *titleRewriter {
	return newTemplateTitleRewriter(agentbox+": {title}", "")
}

// newTemplateTitleRewriter builds a rewriter from an EXPANDED template —
// literal text plus at most one {title}, ExpandBoxTitleTemplate's output
// — and the text to strip from the front of each agent title ("" for
// none). Callers own validation; a stray unexpanded placeholder renders
// literally.
func newTemplateTitleRewriter(tpl, strip string) *titleRewriter {
	before, after, found := strings.Cut(tpl, "{title}")
	t := &titleRewriter{prefix: []byte(before)}
	switch {
	case !found: // literal template replaces the agent's text entirely
		t.dropBody, t.buffered = true, true
	case after != "" || strip != "": // the whole title is needed first
		t.suffix = []byte(after)
		t.strip = []byte(strip)
		t.buffered = true
	case before == "": // "{title}": composition disabled
		t.identity = true
	}
	return t
}

// InitialTitle publishes the template with an empty application title before
// reading PTY output. Agents that never send OSC titles still identify their
// box in every view. Literal template text is preserved, including separators;
// {title} alone opts out. This does not advance the streaming rewriter.
func (t *titleRewriter) InitialTitle() []byte {
	if t.identity {
		return nil
	}
	out := append([]byte("\x1b]2;"), t.prefix...)
	out = append(out, t.suffix...)
	return append(out, '\x1b', '\\')
}

// Rewrite returns p with the template applied to any OSC 0/1/2 title it
// finds, carrying incomplete sequences to the next call. The returned
// slice is freshly allocated (safe to hand to broadcast even though p
// aliases the pump's reused read buffer).
func (t *titleRewriter) Rewrite(p []byte) []byte {
	out := make([]byte, 0, len(p)+len(t.prefix)+len(t.suffix))
	if t.identity {
		return append(out, p...)
	}
	for _, b := range p {
		switch t.state {
		case trNormal:
			if b == 0x1b { // ESC: maybe an escape/OSC sequence
				t.pending = append(t.pending[:0], b)
				t.state = trEsc
			} else {
				out = append(out, b)
			}
		case trEsc:
			if b == ']' { // ESC ] -> OSC introducer
				t.pending = append(t.pending, b)
				t.state = trOSC
			} else { // some other escape (CSI, etc.): flush verbatim
				out = append(out, t.pending...)
				out = append(out, b)
				t.reset()
			}
		case trOSC:
			switch {
			case b >= '0' && b <= '9' && len(t.pending) < trMaxCode:
				t.pending = append(t.pending, b)
			case b == ';':
				t.pending = append(t.pending, b)
				out = append(out, t.pending...)
				isTitle := isTitleIntroducer(t.pending)
				t.reset()
				switch {
				case isTitle && t.buffered:
					t.state = trTitle // hold the text; compose at the terminator
				case isTitle:
					out = append(out, t.prefix...) // inject; the text streams through
				}
			default: // not a numeric introducer: flush verbatim
				out = append(out, t.pending...)
				out = append(out, b)
				t.reset()
			}
		case trTitle:
			switch {
			case b == 0x07: // BEL terminator: compose
				out = t.compose(out, b)
			case b == 0x1b: // maybe ST (ESC \)
				t.state = trTitleEsc
			case len(t.body) >= trMaxTitle: // oversized: abandon, stream raw
				out = append(out, t.body...)
				out = append(out, b)
				t.reset()
			default:
				t.body = append(t.body, b)
			}
		case trTitleEsc:
			if b == '\\' { // ST terminator: compose
				out = t.compose(out, 0x1b, '\\')
			} else { // ESC inside a title is malformed: abandon, stream raw
				out = append(out, t.body...)
				out = append(out, 0x1b, b)
				t.reset()
			}
		}
	}
	return out
}

// compose emits a completed buffered title in template shape — prefix,
// the agent's text (unless the template replaces it) minus a leading
// strip and the spaces after it, suffix — plus the terminator bytes, and
// returns to normal streaming.
func (t *titleRewriter) compose(out []byte, terminator ...byte) []byte {
	out = append(out, t.prefix...)
	if !t.dropBody {
		body := t.body
		if len(t.strip) > 0 && bytes.HasPrefix(body, t.strip) {
			body = bytes.TrimLeft(body[len(t.strip):], " ")
		}
		out = append(out, body...)
	}
	out = append(out, t.suffix...)
	out = append(out, terminator...)
	t.reset()
	return out
}

// Flush returns any bytes still held when the stream ends — a truncated
// introducer or a buffered, never-terminated title; call once after the
// pump loop so nothing is silently dropped.
func (t *titleRewriter) Flush() []byte {
	if len(t.pending) == 0 && len(t.body) == 0 && t.state != trTitleEsc {
		return nil
	}
	out := append([]byte(nil), t.body...)
	if t.state == trTitleEsc {
		out = append(out, 0x1b)
	}
	out = append(out, t.pending...)
	t.reset()
	return out
}

func (t *titleRewriter) reset() {
	t.pending = t.pending[:0]
	t.body = t.body[:0]
	t.state = trNormal
}

// isTitleIntroducer reports whether pending is "ESC ] (0|1|2) ;" — the
// introducer for an icon name (1), window title (2), or both (0). Multi-digit
// codes (52 clipboard, 7 cwd, 8 hyperlink, …) are longer than 4 bytes and so
// are never treated as titles.
func isTitleIntroducer(pending []byte) bool {
	return len(pending) == 4 && pending[0] == 0x1b && pending[1] == ']' &&
		(pending[2] == '0' || pending[2] == '1' || pending[2] == '2') && pending[3] == ';'
}

// ExpandBoxTitleTemplate validates a tui.yaml `session.box_title_template`
// and expands every placeholder except {title}, returning literal text
// plus at most one {title} — the form newTemplateTitleRewriter consumes
// and StartHolderUnit forwards to the VM-side holder. Valid placeholders:
// {title} (the agent-set title, at most once), {box}, {space}, {agent},
// {account}, {agent-icon}. A generic box's empty agent/account render as
// "none", the same sentinel the manifest uses. An empty icon renders as
// nothing and also drops the spaces after its placeholder. strip is the
// text the holder removes from the front of agent titles: the icon when
// {agent-icon} is used, otherwise "". Control characters are rejected:
// the result is spliced into an OSC title sequence, where a stray BEL or
// ESC would terminate or corrupt it. Literal expansions must not introduce
// additional {title} tokens, including across placeholder boundaries.
func ExpandBoxTitleTemplate(tpl, box, space, agent, account, icon string) (expanded, strip string, err error) {
	for _, r := range tpl + icon {
		if r < 0x20 || r == 0x7f {
			return "", "", fmt.Errorf("template or agent icon contains a control character (%q)", r)
		}
	}
	if agent == "" {
		agent = "none"
	}
	if account == "" {
		account = "none"
	}
	var out strings.Builder
	titles := 0
	for i := 0; i < len(tpl); {
		if tpl[i] != '{' {
			out.WriteByte(tpl[i])
			i++
			continue
		}
		end := strings.IndexByte(tpl[i:], '}')
		if end < 0 {
			return "", "", fmt.Errorf("unclosed placeholder at %q", tpl[i:])
		}
		name := tpl[i+1 : i+end]
		i += end + 1
		switch name {
		case "title":
			titles++
			out.WriteString("{title}")
		case "box":
			out.WriteString(box)
		case "space":
			out.WriteString(space)
		case "agent":
			out.WriteString(agent)
		case "account":
			out.WriteString(account)
		case "agent-icon":
			out.WriteString(icon)
			strip = icon
			for icon == "" && i < len(tpl) && tpl[i] == ' ' {
				i++
			}
		default:
			return "", "", fmt.Errorf("unknown placeholder {%s} (valid: {title}, {box}, {space}, {agent}, {account}, {agent-icon})", name)
		}
	}
	if titles > 1 {
		return "", "", fmt.Errorf("{title} may appear at most once (found %d)", titles)
	}
	expanded = out.String()
	// The rewriter splits on {title}; tokens introduced by literal values
	// would move that split or turn a replacement template into composition.
	if strings.Count(expanded, "{title}") != titles {
		return "", "", fmt.Errorf("literal title text must not introduce a {title} placeholder")
	}
	return expanded, strip, nil
}
