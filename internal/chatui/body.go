package chatui

import (
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/leogr/yoloctl/internal/markdownview"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// decorateTokens marks bare http(s) URLs and, in prose (outside source
// mode), @mentions and message references. An @ followed only by digits is
// neither: it stays plain text.
func decorateTokens(text string, pieces []piece, theme spaceview.Theme, me string, prose bool) []piece {
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "https://") || strings.HasPrefix(text[i:], "http://"):
			end, link, _ := markdownview.URLAt(text, i)
			pieces = overlay(pieces, i, i+len(link), func(p piece) piece {
				p.Style.FG, p.Style.Underline = theme.Label, true
				p.Act = action{Kind: actLink, Value: link}
				return p
			})
			i = end
			continue
		case prose && strings.HasPrefix(text[i:], refPrefix) && (i == 0 || !nickByte(text[i-1])):
			// base64url's alphabet is the nickname bytes: a longer run, or
			// padding after it, is not a reference with a valid prefix.
			end := i + refLen
			if end <= len(text) && (end == len(text) || !nickByte(text[end]) && text[end] != '=') && !linked(pieces, i, end) {
				if id, ok := refID(text[i:end]); ok {
					pieces = overlay(pieces, i, end, func(p piece) piece {
						p.Style.FG, p.Style.Underline = theme.Label, true
						p.Act = action{Kind: actRef, Value: id}
						return p
					})
					i = end
					continue
				}
			}
		case prose && strings.HasPrefix(text[i:], msgidPrefix) && (i == 0 || !nickByte(text[i-1])):
			end := i + len(msgidPrefix) + len("00000000-0000-0000-0000-000000000000")
			if end <= len(text) && messageID(text[i+len(msgidPrefix):end]) && (end == len(text) || !nickByte(text[end])) && !linked(pieces, i, end) {
				id := text[i+len(msgidPrefix) : end]
				pieces = overlay(pieces, i, end, func(p piece) piece {
					p.Style.FG, p.Style.Underline = theme.Label, true
					p.Act = action{Kind: actRef, Value: id}
					return p
				})
				i = end
				continue
			}
		case prose && text[i] == '@' && (i == 0 || !nickByte(text[i-1])):
			end := i + 1
			for end < len(text) && nickByte(text[end]) {
				end++
			}
			switch {
			case end == i+1:
			case strings.Trim(text[i+1:end], "0123456789") == "":
				// A nickname starts with a letter or _; nothing resolves a
				// number such as @289 written in older messages.
			default:
				nick := text[i+1 : end]
				self := selfMention(me, nick)
				pieces = overlay(pieces, i, end, func(p piece) piece {
					p.Style.Bold = true
					p.Style.FG = theme.Label
					if self {
						p.Style.FG, p.Style.BG = mentionColors(theme)
					}
					p.Act = action{Kind: actMention, Value: nick}
					return p
				})
			}
			i = end
			continue
		}
		i++
	}
	return pieces
}

// refPrefix starts a shared message reference: m: and the unpadded base64url
// encoding of the server ID's 16 bytes in display order, the spelling AIRC
// prints for every client in the room. Copy reference writes one; a click
// shows the message when it is loaded in the same channel.
const refPrefix = "m:"

// refLen is the length of a reference: 128 bits take 22 base64url characters.
const refLen = len(refPrefix) + 22

// msgidPrefix and the full server ID are the same reference, longer.
const msgidPrefix = "msgid:"

// compactRef returns the shared reference of a server message ID.
func compactRef(id string) (string, bool) {
	var raw [16]byte
	if !messageID(id) {
		return "", false
	}
	if _, err := hex.Decode(raw[:], []byte(strings.ReplaceAll(id, "-", ""))); err != nil {
		return "", false
	}
	return refPrefix + base64.RawURLEncoding.EncodeToString(raw[:]), true
}

// refID returns the server message ID a shared reference names. Only exactly
// 22 base64url characters whose decoding re-encodes to the same text qualify:
// padding, other alphabets and nonzero unused bits are not references.
func refID(ref string) (string, bool) {
	encoded, ok := strings.CutPrefix(ref, refPrefix)
	if !ok || len(ref) != refLen {
		return "", false
	}
	for i := 0; i < len(encoded); i++ {
		if !nickByte(encoded[i]) {
			return "", false
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return "", false
	}
	h := hex.EncodeToString(raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], true
}

// messageID reports whether id has the lowercase canonical UUID form of the
// room server's message IDs. Partial or other forms are never references.
func messageID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

func nickByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// eachMention calls f with the token of each explicit @mention in text, in
// order, until f returns false: an @ that does not follow a nickname byte,
// then the nickname bytes after it.
func eachMention(text string, f func(token string) bool) {
	for i := strings.IndexByte(text, '@'); i >= 0; {
		end := i + 1
		for end < len(text) && nickByte(text[end]) {
			end++
		}
		if (i == 0 || !nickByte(text[i-1])) && end > i+1 && !f(text[i+1:end]) {
			return
		}
		next := strings.IndexByte(text[end:], '@')
		if next < 0 {
			return
		}
		i = end + next
	}
}

// mentions reports whether text addresses one of me (see selfMention) with an
// explicit @mention.
func mentions(text, me string) bool {
	found := false
	eachMention(text, func(token string) bool {
		found = selfMention(me, token)
		return !found
	})
	return found
}

// selfMention reports whether token is one of me: the local user's mention
// tokens, NUL-separated (see selfNames); a plain nick is a list of one.
func selfMention(me, token string) bool {
	for name := range strings.SplitSeq(me, "\x00") {
		if name != "" && strings.EqualFold(name, token) {
			return true
		}
	}
	return false
}

// mentionColors highlight a mention of the local user like a marker pen,
// distinct from error colors in both themes.
func mentionColors(t spaceview.Theme) (fg, bg spaceview.Color) {
	if t.Dark {
		return spaceview.PaletteColor(230), spaceview.PaletteColor(94)
	}
	return spaceview.PaletteColor(94), spaceview.PaletteColor(229)
}
