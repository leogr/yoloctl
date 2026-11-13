package chatexport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
)

func at(clock string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-11-13T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// sample covers a multi-line message, replies inside and outside the
// export, a standalone published file, staged attachments, a control
// character and HTML-looking text.
func sample() []chatstream.Message {
	return []chatstream.Message{
		{Channel: "#ux", ID: "m1", Time: at("12:00:01"), Account: "acct-leogr", Nick: "leogr", Text: "first line\nsecond line"},
		{Channel: "#ux", ID: "m2", Time: at("12:00:02"), Account: "acct-opus", Nick: "opus", Text: "agreed <b>&</b>", ReplyTo: "m1"},
		{Channel: "#ux", ID: "m3", Time: at("12:00:03"), Account: "acct-opus", Nick: "opus", Text: "older context", ReplyTo: "gone"},
		{Channel: "#ux", ID: "m4", Time: at("12:00:04"), Account: "acct-opus", Nick: "opus", Text: "Artifact plot.png (83000 bytes): http://airc.local/x",
			Artifact: &chatstream.Artifact{ID: "a1", Name: "plot.png", Bytes: 83000, SHA256: strings.Repeat("a", 64)}},
		{Channel: "#ux", ID: "m5", Time: at("12:00:05"), Account: "acct-leogr", Nick: "leogr", Text: "see [Image #1] and [Image #2]\x1b[31m",
			Attachments: []chatstream.Attachment{
				{Artifact: chatstream.Artifact{ID: "b1", Name: "a.png", Bytes: 512, SHA256: strings.Repeat("b", 64)}, At: 4},
				{Artifact: chatstream.Artifact{ID: "b2", Name: "b.png", Bytes: 3 << 20, SHA256: strings.Repeat("c", 64)}, At: 19},
			}},
	}
}

func TestSelectByTimeIgnoresServerOrder(t *testing.T) {
	messages := []chatstream.Message{{ID: "a", Time: at("12:05:00")}, {ID: "b", Time: at("11:59:00")}, {ID: "c", Time: at("12:06:00")}}
	got := Select(messages, at("12:00:00"), time.Time{})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("since 12:00 selected %+v", got)
	}
	if got := Select(messages, time.Time{}, at("12:05:00")); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("until 12:05 (exclusive) selected %+v", got)
	}
	oldest, newest := Span(messages)
	if !oldest.Equal(at("11:59:00")) || !newest.Equal(at("12:06:00")) {
		t.Fatalf("span %v %v", oldest, newest)
	}
}

func TestTextTranscript(t *testing.T) {
	var out bytes.Buffer
	messages := sample()
	meta := Meta{Space: "dev", Channel: "#ux", Exported: at("13:00:00"), Received: messages}
	if err := WriteText(&out, meta, messages); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"# Space: dev\n# Channel: #ux\n# Exported: 2026-11-13 13:00:00Z\n",
		"# Received: 5 messages, oldest 2026-11-13 12:00:01Z, newest 2026-11-13 12:00:05Z\n# In this file: 5 messages\n# " + Retention + "\n\n",
		"2026-11-13 12:00:01Z  leogr: first line\n    second line\n",
		`2026-11-13 12:00:02Z  opus: (reply to leogr: "first line") agreed <b>&</b>` + "\n",
		"2026-11-13 12:00:03Z  opus: (reply to gone) older context\n",
		"2026-11-13 12:00:04Z  opus: [file plot.png, 81.1 KiB]\n",
		"2026-11-13 12:00:05Z  leogr: see [file a.png, 512 B] and [file b.png, 3.0 MiB]\uFFFD[31m\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("transcript lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "airc.local") {
		t.Fatalf("transcript keeps a control character or the announcement URL:\n%s", text)
	}
}

func TestTextHeaderNamesTheTimeRange(t *testing.T) {
	var out bytes.Buffer
	if err := WriteText(&out, Meta{Space: "dev", Channel: "#ux", Since: at("12:00:00")}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "# Received: 0 messages, oldest -, newest -\n# In this file: 0 messages (since 2026-11-13 12:00:00Z, until -)\n") {
		t.Fatalf("header:\n%s", out.String())
	}
}

func TestJSONLRoundTripsEveryField(t *testing.T) {
	var out bytes.Buffer
	messages := sample()
	if err := WriteJSONL(&out, messages); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `\u003c`) {
		t.Fatal("text was HTML-escaped")
	}
	scan := bufio.NewScanner(&out)
	var got []chatstream.Message
	for scan.Scan() {
		var line jsonMessage
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(line.Time, "Z") {
			t.Fatalf("time without its zone: %s", line.Time)
		}
		when, err := time.Parse(time.RFC3339Nano, line.Time)
		if err != nil {
			t.Fatal(err)
		}
		m := chatstream.Message{Channel: line.Channel, ID: line.ID, Time: when, Account: line.Account, Nick: line.Nick, Text: line.Text, ReplyTo: line.ReplyTo}
		if a := line.Artifact; a != nil {
			m.Artifact = &chatstream.Artifact{ID: a.ID, Name: a.Name, Bytes: a.Bytes, SHA256: a.SHA256}
		}
		for _, a := range line.Attachments {
			m.Attachments = append(m.Attachments, chatstream.Attachment{Artifact: chatstream.Artifact{ID: a.ID, Name: a.Name, Bytes: a.Bytes, SHA256: a.SHA256}, At: a.At})
		}
		got = append(got, m)
	}
	if !reflect.DeepEqual(got, messages) {
		t.Fatalf("round trip differs:\n%+v\n%+v", got, messages)
	}
}

func TestBothFormatsHoldTheSameMessages(t *testing.T) {
	messages := sample()
	var text, lines bytes.Buffer
	if err := Write(&text, FormatText, Meta{Received: messages}, messages); err != nil {
		t.Fatal(err)
	}
	if err := Write(&lines, FormatJSONL, Meta{Received: messages}, messages); err != nil {
		t.Fatal(err)
	}
	entries := 0
	for _, line := range strings.Split(text.String(), "\n") {
		if strings.HasPrefix(line, "2026-") {
			entries++
		}
	}
	if entries != len(messages) || strings.Count(lines.String(), "\n") != len(messages) {
		t.Fatalf("text %d entries, jsonl %d lines, want %d", entries, strings.Count(lines.String(), "\n"), len(messages))
	}
	if err := Write(&text, "csv", Meta{}, nil); err == nil {
		t.Fatal("unknown format accepted")
	}
}
