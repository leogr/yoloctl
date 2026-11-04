package chatstream

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	attachA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	attachB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

func TestValidAttachNeedsOrderedMarkersAtTheirOffsets(t *testing.T) {
	text := "look [Image #1]\nand [Image #2] é"
	at := func(s string) int { return strings.Index(text, s) }
	two := func(a, b int) []Attach { return []Attach{{ID: attachA, At: a}, {ID: attachB, At: b}} }
	if !ValidAttach(text, two(at("[Image #1]"), at("[Image #2]"))) {
		t.Fatal("ordered markers at their offsets are valid")
	}
	for name, attach := range map[string][]Attach{
		"none":       nil,
		"swapped":    two(at("[Image #2]"), at("[Image #1]")),
		"overlap":    two(at("[Image #1]"), at("[Image #1]")+2),
		"past end":   two(at("[Image #1]"), len(text)+1),
		"negative":   two(-1, at("[Image #2]")),
		"duplicate":  {{ID: attachA, At: at("[Image #1]")}, {ID: attachA, At: at("[Image #2]")}},
		"bad id":     {{ID: strings.ToUpper(attachA), At: at("[Image #1]")}},
		"too many":   make([]Attach, MaxAttachments+1),
		"wrong mark": {{ID: attachA, At: at("[Image #2]")}},
	} {
		if ValidAttach(text, attach) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReservedAttachmentsAreAllOrNothing(t *testing.T) {
	text := "x [Image #1] y [Image #2]"
	item := func(id string, at int, name string) string {
		return id + "/12/" + strings.Repeat("a", 64) + "/" + itoa(at) + "/" + name
	}
	good := item(attachA, 2, "paste-a.png") + "," + item(attachB, 15, "paste-b.png")
	got := attachmentsFromTags(map[string]string{"airc-artifacts": good}, text)
	if len(got) != 2 || got[1].ID != attachB || got[1].At != 15 || got[0].Bytes != 12 || got[0].Name != "paste-a.png" {
		t.Fatalf("valid reserved tag: %+v", got)
	}
	for name, raw := range map[string]string{
		"misplaced":   item(attachA, 3, "a.png"),
		"bad name":    item(attachA, 2, "a/b.png"),
		"bad hash":    attachA + "/12/xyz/2/a.png",
		"leading 0":   attachA + "/012/" + strings.Repeat("a", 64) + "/2/a.png",
		"short":       attachA + "/12",
		"one invalid": item(attachA, 2, "a.png") + "," + item(attachB, 99, "b.png"),
	} {
		if got := attachmentsFromTags(map[string]string{"airc-artifacts": raw}, text); got != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if attachmentsFromTags(map[string]string{"+airc-artifacts": good}, text) != nil {
		t.Fatal("a client-only lookalike is never metadata")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestAttachmentRefusalsAreParsedOnlyFromTheExactForm(t *testing.T) {
	for reason, want := range map[string]AttachmentRefusal{
		"FAIL PRIVMSG ATTACHMENTS #space attachments: unknown k=2":          {Reason: "unknown", Index: 2},
		"FAIL BATCH ATTACHMENTS attachments: consumed k=1 msgid=" + attachB: {Reason: "consumed", Index: 1, MessageID: attachB},
		"FAIL PRIVMSG ATTACHMENTS #space attachments: unavailable":          {Reason: "unavailable"},
		"FAIL PRIVMSG ATTACHMENTS #space attachments: too-long":             {Reason: "too-long"},
		"FAIL BATCH ATTACHMENTS attachments: invalid":                       {Reason: "invalid"},
	} {
		got := ParseAttachmentRefusal(reason)
		if got == nil || *got != want {
			t.Errorf("%q: %+v", reason, got)
		}
	}
	for _, reason := range []string{
		"404 human #space Cannot send to channel",
		"FAIL PRIVMSG ATTACHMENTS #space attachments: unknown k=9",
		"FAIL PRIVMSG ATTACHMENTS #space attachments: consumed k=1 msgid=",
		"FAIL PRIVMSG OTHER #space attachments: invalid",
		"FAIL PRIVMSG ATTACHMENTS #space attachments: invalid extra",
	} {
		if got := ParseAttachmentRefusal(reason); got != nil {
			t.Errorf("%q parsed as %+v", reason, got)
		}
	}
}

func TestAttachIsForwardedOnlyWhenTheClientHasTheFeature(t *testing.T) {
	line := `{"id":"s","op":"send","target":"#space","text":"see [Image #1]","attach":[{"id":"` + attachA + `","at":4,"extra":1}]}`
	if _, err := decodeChannelRequest([]byte(line), func(string) bool { return true }, false); err == nil {
		t.Fatal("attach without the feature would be an unknown field to AIRC")
	}
	r, err := decodeChannelRequest([]byte(line), func(string) bool { return true }, true)
	if err != nil || len(r.Attach) != 1 || r.Attach[0].At != 4 {
		t.Fatalf("a valid attach is accepted: %v %+v", err, r)
	}
	var out bytes.Buffer
	if err := FilterChannels(strings.NewReader(line+"\n"), &out, func(string) error { return nil }, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "extra") || !strings.Contains(out.String(), `"attach":[{"id":"`+attachA+`","at":4}]`) {
		t.Fatalf("the controller re-encodes attach canonically: %s", out.String())
	}
	for _, bad := range []string{
		strings.Replace(line, `"at":4`, `"at":5`, 1),
		strings.Replace(line, `[{`, `null,[{`, 1),
		`{"id":"s","op":"send","target":"#space","text":"x","attach":null}`,
		`{"id":"s","op":"send","target":"#space","text":"x","attach":[]}`,
	} {
		out.Reset()
		if err := FilterChannels(strings.NewReader(bad+"\n"), &out, func(string) error { return nil }, true); err == nil || out.Len() != 0 {
			t.Errorf("forwarded %s", bad)
		}
	}
	if err := FilterChannels(strings.NewReader(line+"\n"), &out, func(string) error { return nil }, false); err == nil {
		t.Fatal("without the feature the controller refuses attach")
	}
}

func TestEchoConfirmsOnlyTheSameAttachments(t *testing.T) {
	h := testHeader(t)
	h.Attachments = true
	text := "see [Image #1]"
	tag := attachA + "/3/" + strings.Repeat("b", 64) + "/4/paste-a.png"
	echo := func(tag string) record {
		return record{Command: "PRIVMSG", Prefix: h.Nick + "!user@server", Params: []string{"#space", text},
			Tags: map[string]string{"msgid": "stable", "account": h.SelfAccount, "+airc-send": "r", "airc-artifacts": tag}}
	}
	for name, tc := range map[string]struct {
		tag     string
		confirm bool
	}{
		"same":      {tag, true},
		"other id":  {strings.Replace(tag, attachA, attachB, 1), false},
		"no images": {"", false},
	} {
		var events []Event
		p := &pendingSend{Send: Send{ID: "local", Text: text, Attach: []Attach{{ID: attachA, At: 4}}}, Receipt: "r"}
		d := &driver{header: h, pending: p, batches: map[string]*historyBatch{}, emit: func(e Event) { events = append(events, e) }}
		r := echo(tc.tag)
		if tc.tag == "" {
			delete(r.Tags, "airc-artifacts")
		}
		if err := d.record(r); err != nil {
			t.Fatal(name, err)
		}
		confirmed := len(events) == 2 && events[0].State == "confirmed" && events[1].Message.ClientID == "local"
		if confirmed != tc.confirm {
			t.Errorf("%s: confirmed=%v %+v", name, confirmed, events)
		}
		if name == "same" && (len(events[1].Message.Attachments) != 1 || events[1].Message.Attachments[0].At != 4) {
			t.Errorf("the echo carries its attachments: %+v", events[1].Message)
		}
	}
	var events []Event
	p := &pendingSend{Send: Send{ID: "local", Text: text, Attach: []Attach{{ID: attachA, At: 4}}}, Receipt: "r"}
	d := &driver{header: h, pending: p, batches: map[string]*historyBatch{}, emit: func(e Event) { events = append(events, e) }}
	reason := "FAIL PRIVMSG ATTACHMENTS #space attachments: unknown k=1"
	if err := d.record(record{Event: "receipt", Receipt: "r", Target: "#space", State: "rejected", Reason: reason}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Refusal == nil || *events[0].Refusal != (AttachmentRefusal{Reason: "unknown", Index: 1}) {
		t.Fatalf("a refused send with attachments carries its parsed refusal: %+v", events)
	}
	// An echo with other images leaves the send pending; a later confirmed
	// receipt alone is not evidence that these images were published.
	events = nil
	p = &pendingSend{Send: Send{ID: "local", Text: text, Attach: []Attach{{ID: attachA, At: 4}}}, Receipt: "r"}
	d = &driver{header: h, pending: p, batches: map[string]*historyBatch{}, emit: func(e Event) { events = append(events, e) }}
	if err := d.record(echo(strings.Replace(tag, attachA, attachB, 1))); err != nil || d.pending == nil {
		t.Fatal("a mismatched echo must not resolve the send", err)
	}
	if err := d.record(record{Event: "receipt", Receipt: "r", Target: "#space", State: "confirmed", MsgID: "stable"}); err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != "delivery" || last.State != "uncertain" || d.pending != nil {
		t.Fatalf("an unmatched receipt is uncertain, never confirmed: %+v", events)
	}
}
