package chatstream

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFilterRejectsForeignAuthorityAndIncompleteFrames(t *testing.T) {
	for _, line := range []string{
		`{"id":"x","op":"upload","channel":"#space","path":"/run/yoloctl/chat-server/human/token"}`,
		`{"id":"x","op":"download","artifact":"a","path":"/tmp/out"}`,
		`{"id":"x","op":"send","target":"#foreign","text":"test"}`,
		`{"id":"x","op":"send","target":"#space","text":"test","path":"a"}`,
		`{"id":"x","op":"read","limit":200}`,
		`{"id":"x","op":"read","limit":1,"wait_ms":60000}`,
		`{"id":"x","op":"mode","channel":"#space","modes":"+o","nick":"someone"}`,
		`{"id":"x","op":"quit","active":false}`,
		`{"id":"x","op":"send","target":"#space","text":"a\rb"}`,
	} {
		var out bytes.Buffer
		if err := Filter(strings.NewReader(line+"\n"), &out, "#space"); err == nil || out.Len() != 0 {
			t.Fatalf("forwarded unauthorized request: %s, %v", line, err)
		}
	}
	for _, raw := range []string{`{"id":"x","op":"quit"}`, strings.Repeat("x", RequestLimit) + "\n"} {
		var out bytes.Buffer
		if err := Filter(strings.NewReader(raw), &out, "#space"); err == nil || out.Len() != 0 {
			t.Fatal("partial/oversized request forwarded", err)
		}
	}
}

func TestFilterPreservesWholeMultilineAndFalseTyping(t *testing.T) {
	body := strings.Repeat("é\n\t", MaxMessageBytes/4)
	raw, _ := json.Marshal(map[string]any{"id": "message", "op": "send", "target": "#space", "text": body})
	var out bytes.Buffer
	if err := Filter(bytes.NewReader(append(raw, '\n')), &out, "#space"); err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Text != body {
		t.Fatal("multiline changed", err)
	}
	out.Reset()
	if err := Filter(strings.NewReader("{\"id\":\"t\",\"op\":\"typing\",\"channel\":\"#space\",\"active\":false}\n"), &out, "#space"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"active":false`) {
		t.Fatal("lost explicit false typing field")
	}
}

func TestFilterWhoIsRoomScopedAndRejectsExtraFields(t *testing.T) {
	var out bytes.Buffer
	if err := Filter(strings.NewReader("{\"channel\":\"#space\",\"op\":\"who\",\"id\":\"roster\"}\n"), &out, "#space"); err != nil {
		t.Fatal(err)
	}
	if want := "{\"id\":\"roster\",\"op\":\"who\",\"channel\":\"#space\"}\n"; out.String() != want {
		t.Fatalf("WHO request changed or gained fields: %q", out.String())
	}
	for _, tc := range []struct {
		name string
		line string
	}{
		{"foreign-channel", `{"id":"r","op":"who","channel":"#foreign"}`},
		{"channel-list", `{"id":"r","op":"who","channel":"#space,#foreign"}`},
		{"wildcard", `{"id":"r","op":"who","channel":"*"}`},
		{"missing-channel", `{"id":"r","op":"who"}`},
		{"empty-channel", `{"id":"r","op":"who","channel":""}`},
		{"null-channel", `{"id":"r","op":"who","channel":null}`},
		{"missing-id", `{"op":"who","channel":"#space"}`},
		{"empty-target-field", `{"id":"r","op":"who","channel":"#space","target":""}`},
		{"zero-after-field", `{"id":"r","op":"who","channel":"#space","after":0}`},
		{"read-limit-field", `{"id":"r","op":"who","channel":"#space","limit":1}`},
		{"typing-field", `{"id":"r","op":"who","channel":"#space","active":false}`},
		{"raw-command-field", `{"id":"r","op":"who","channel":"#space","raw":"WHO #foreign"}`},
		{"noncanonical-field", `{"id":"r","op":"who","Channel":"#space"}`},
		{"raw-command-op", `{"id":"r","op":"WHO","channel":"#space"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Filter(strings.NewReader(tc.line+"\n"), &out, "#space"); err == nil || out.Len() != 0 {
				t.Fatalf("forwarded unauthorized WHO request: %s, %v", tc.line, err)
			}
		})
	}
}
