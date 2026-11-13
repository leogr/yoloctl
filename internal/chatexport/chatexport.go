// Package chatexport writes a channel's exported chat history as a readable
// transcript or as JSON lines. Both come from the same message records.
package chatexport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/chatstream"
)

// Formats an export can be written in.
const (
	FormatText  = "text"
	FormatJSONL = "jsonl"
)

// Retention describes the packaged chat server's history policy. It is a
// policy, not a finding about the exported room.
const Retention = "The chat server keeps at most 10,000 messages and 30 days per channel (packaged default); nothing older can be exported."

// Meta describes one channel's export for the transcript header.
type Meta struct {
	Space, Channel string
	Exported       time.Time
	// Received are all messages the server returned; the file holds those
	// within Since and Until.
	Received     []chatstream.Message
	Since, Until time.Time
}

// Select returns the messages with since ≤ time < until, in their order. A
// zero bound is open. Times need not follow the server's order, so every
// message is checked.
func Select(messages []chatstream.Message, since, until time.Time) []chatstream.Message {
	var out []chatstream.Message
	for _, m := range messages {
		if !since.IsZero() && m.Time.Before(since) || !until.IsZero() && !m.Time.Before(until) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Span returns the oldest and newest time among messages, zero when empty.
func Span(messages []chatstream.Message) (oldest, newest time.Time) {
	for _, m := range messages {
		if oldest.IsZero() || m.Time.Before(oldest) {
			oldest = m.Time
		}
		if m.Time.After(newest) {
			newest = m.Time
		}
	}
	return oldest, newest
}

// Write writes messages in format.
func Write(w io.Writer, format string, meta Meta, messages []chatstream.Message) error {
	switch format {
	case FormatText:
		return WriteText(w, meta, messages)
	case FormatJSONL:
		return WriteJSONL(w, messages)
	}
	return fmt.Errorf("unknown export format %q (use text or jsonl)", format)
}

const timeLayout = "2006-01-02 15:04:05Z"

func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(timeLayout)
}

// WriteText writes a transcript a person can read: a header, then one entry
// per message, its continuation lines indented. Control characters show as
// U+FFFD so the file is safe to print in a terminal; JSON lines keep the
// exact text.
func WriteText(w io.Writer, meta Meta, messages []chatstream.Message) error {
	b := bufio.NewWriter(w)
	oldest, newest := Span(meta.Received)
	// bufio keeps the first write error; Flush reports it.
	_, _ = fmt.Fprintf(b, "# Chat export\n# Space: %s\n# Channel: %s\n# Exported: %s\n", clean(meta.Space), clean(meta.Channel), stamp(meta.Exported))
	_, _ = fmt.Fprintf(b, "# Received: %d messages, oldest %s, newest %s\n", len(meta.Received), stamp(oldest), stamp(newest))
	selected := fmt.Sprintf("# In this file: %d messages", len(messages))
	if !meta.Since.IsZero() || !meta.Until.IsZero() {
		selected += fmt.Sprintf(" (since %s, until %s)", stamp(meta.Since), stamp(meta.Until))
	}
	_, _ = fmt.Fprintf(b, "%s\n# %s\n\n", selected, Retention)
	byID := map[string]*chatstream.Message{}
	for i := range messages {
		byID[messages[i].ID] = &messages[i]
	}
	for _, m := range messages {
		text := body(m)
		if m.ReplyTo != "" {
			text = replyLabel(byID[m.ReplyTo], m.ReplyTo) + " " + text
		}
		lines := strings.Split(clean(text), "\n")
		_, _ = fmt.Fprintf(b, "%s  %s: %s\n", stamp(m.Time), cleanLine(m.Nick), lines[0])
		for _, line := range lines[1:] {
			_, _ = fmt.Fprintf(b, "    %s\n", line)
		}
	}
	return b.Flush()
}

// body is a message's text with its published files as [file NAME, SIZE]:
// a standalone artifact replaces the announcement text, and each staged
// attachment its marker, at its recorded offset.
func body(m chatstream.Message) string {
	if m.Artifact != nil {
		return fileLabel(m.Artifact.Name, m.Artifact.Bytes)
	}
	if len(m.Attachments) == 0 {
		return m.Text
	}
	var b strings.Builder
	last := 0
	for k, a := range m.Attachments {
		marker := chatstream.Marker(k + 1)
		if a.At < last || a.At > len(m.Text) || !strings.HasPrefix(m.Text[a.At:], marker) {
			return m.Text // not where the record says: keep the text as sent
		}
		b.WriteString(m.Text[last:a.At])
		b.WriteString(fileLabel(a.Name, a.Bytes))
		last = a.At + len(marker)
	}
	b.WriteString(m.Text[last:])
	return b.String()
}

func fileLabel(name string, bytes int64) string {
	return "[file " + cleanLine(name) + ", " + size(bytes) + "]"
}

func size(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
}

// replyLabel names the answered message: its sender and first words when it
// is in this export, otherwise only its ID. Nothing else is fetched for it.
func replyLabel(parent *chatstream.Message, id string) string {
	if parent == nil {
		return "(reply to " + cleanLine(id) + ")"
	}
	first, _, _ := strings.Cut(body(*parent), "\n")
	first = cleanLine(first)
	if utf8.RuneCountInString(first) > 40 {
		first = string([]rune(first)[:40]) + "…"
	}
	return fmt.Sprintf("(reply to %s: %q)", cleanLine(parent.Nick), first)
}

// clean replaces control characters other than newline and tab.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}

// cleanLine is clean for text that must stay on one line.
func cleanLine(s string) string {
	return strings.ReplaceAll(clean(s), "\n", " ")
}

type jsonFile struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type jsonAttachment struct {
	At int `json:"at"`
	jsonFile
}

type jsonMessage struct {
	Channel     string           `json:"channel"`
	ID          string           `json:"id"`
	Time        string           `json:"time"`
	Account     string           `json:"account"`
	Nick        string           `json:"nick"`
	Text        string           `json:"text"`
	ReplyTo     string           `json:"reply_to,omitempty"`
	Artifact    *jsonFile        `json:"artifact,omitempty"`
	Attachments []jsonAttachment `json:"attachments,omitempty"`
}

// WriteJSONL writes one JSON object per message with every field of its
// record, the time in RFC 3339 UTC, and the text exactly as sent.
func WriteJSONL(w io.Writer, messages []chatstream.Message) error {
	b := bufio.NewWriter(w)
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	for _, m := range messages {
		out := jsonMessage{Channel: m.Channel, ID: m.ID, Time: m.Time.UTC().Format(time.RFC3339Nano), Account: m.Account, Nick: m.Nick, Text: m.Text, ReplyTo: m.ReplyTo}
		if a := m.Artifact; a != nil {
			out.Artifact = &jsonFile{ID: a.ID, Name: a.Name, Bytes: a.Bytes, SHA256: a.SHA256}
		}
		for _, a := range m.Attachments {
			out.Attachments = append(out.Attachments, jsonAttachment{At: a.At, jsonFile: jsonFile{ID: a.ID, Name: a.Name, Bytes: a.Bytes, SHA256: a.SHA256}})
		}
		if err := enc.Encode(out); err != nil {
			return err
		}
	}
	return b.Flush()
}
