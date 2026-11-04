package termio

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestOSC52ScannerSplitAndPreserveOtherOutput(t *testing.T) {
	var scanner OSC52Scanner
	chunks := []string{
		"before\x1b]2;ti",
		"tle\a middle \x1b]5",
		"2;c;Y29w",
		"aWVk\x1b\\ after",
	}
	var output []byte
	var events []OSC52
	for _, chunk := range chunks {
		out, got := scanner.Feed([]byte(chunk))
		output = append(output, out...)
		events = append(events, got...)
	}
	output = append(output, scanner.Flush()...)

	if got, want := string(output), "before\x1b]2;title\a middle  after"; got != want {
		t.Fatalf("filtered output = %q, want %q", got, want)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one", events)
	}
	if got, want := events[0], (OSC52{Selection: "c", Payload: "Y29waWVk"}); got != want {
		t.Fatalf("event = %#v, want %#v", got, want)
	}
}

func TestOSC52ScannerTerminators(t *testing.T) {
	var scanner OSC52Scanner
	input := []byte("\x1b]52;c;YQ==\a\x9d52;c;Yg==\x9c")
	output, events := scanner.Feed(input)
	if len(output) != 0 {
		t.Fatalf("output = %q, want empty", output)
	}
	if len(events) != 2 || events[0].Payload != "YQ==" || events[1].Payload != "Yg==" {
		t.Fatalf("events = %#v", events)
	}
}

func TestOSC52ScannerPreservesUTF8ContainingC1Bytes(t *testing.T) {
	// U+276F (❯) is E2 9D AF in UTF-8. The middle byte is also the raw
	// 8-bit OSC control, but must not be recognized while it is a continuation
	// byte. Feed one byte at a time to cover a PTY read split inside the rune.
	input := []byte("workdir ❯ yoloctl box recreate")
	var scanner OSC52Scanner
	var output []byte
	var events []OSC52
	for _, b := range input {
		out, got := scanner.Feed([]byte{b})
		output = append(output, out...)
		events = append(events, got...)
	}
	if trailing := scanner.Flush(); len(trailing) != 0 {
		t.Fatalf("scanner retained ordinary UTF-8 until Flush: %q", trailing)
	}
	if got := string(output); got != string(input) {
		t.Fatalf("output = %q, want byte-exact %q", got, input)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestOSC52ScannerFlushesIncompleteSequence(t *testing.T) {
	var scanner OSC52Scanner
	input := "\x1b]52;c;YQ"
	output, events := scanner.Feed([]byte(input))
	if len(output) != 0 || len(events) != 0 {
		t.Fatalf("feed = output %q events %#v", output, events)
	}
	if got := string(scanner.Flush()); got != input {
		t.Fatalf("flush = %q, want %q", got, input)
	}
}

func TestOSC52ScannerPendingPeeksWithoutConsuming(t *testing.T) {
	var scanner OSC52Scanner
	input := "\x1b]52;c;YQ"
	if output, events := scanner.Feed([]byte(input)); len(output) != 0 || len(events) != 0 {
		t.Fatalf("feed = output %q events %#v", output, events)
	}
	if got := string(scanner.Pending()); got != input {
		t.Fatalf("pending = %q, want %q", got, input)
	}
	// Peeking must not consume: completing the sequence still yields the event.
	output, events := scanner.Feed([]byte("==\x1b\\"))
	if len(output) != 0 || len(events) != 1 || events[0].Payload != "YQ==" {
		t.Fatalf("after pending peek, feed = output %q events %#v", output, events)
	}
	if got := scanner.Pending(); len(got) != 0 {
		t.Fatalf("pending after completion = %q, want empty", got)
	}
}

func TestEvaluateOSC52Policy(t *testing.T) {
	valid := OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString([]byte("copy me"))}
	if sequence, notice := EvaluateOSC52(valid, false); sequence != "" || notice != OSC52DisabledNotice {
		t.Fatalf("disabled = (%q, %q)", sequence, notice)
	}
	if sequence, notice := EvaluateOSC52(valid, true); sequence != valid.Sequence() || notice != "" {
		t.Fatalf("enabled = (%q, %q)", sequence, notice)
	}

	cases := []struct {
		name  string
		event OSC52
		want  string
	}{
		{"read", OSC52{Selection: "c", Payload: "?"}, "never exposes"},
		{"selection", OSC52{Selection: "p", Payload: "YQ=="}, "unsupported selection"},
		{"base64", OSC52{Selection: "c", Payload: "not-base64!"}, "valid base64"},
		{"malformed", OSC52{Selection: "c", Malformed: true}, "malformed"},
		{
			"oversize",
			OSC52{Selection: "c", Payload: base64.StdEncoding.EncodeToString(make([]byte, MaxOSC52DecodedBytes+1))},
			"exceeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sequence, notice := EvaluateOSC52(tc.event, true)
			if sequence != "" || !strings.Contains(notice, tc.want) {
				t.Fatalf("EvaluateOSC52 = (%q, %q), want notice containing %q", sequence, notice, tc.want)
			}
		})
	}
}

func TestOSC52FilterRoutesEventsOutsideDownstream(t *testing.T) {
	var dst bytes.Buffer
	var events []OSC52
	filter := NewOSC52Filter(&dst, func(event OSC52) {
		events = append(events, event)
	})
	input := []byte("left\x1b]52;c;YQ==\aright")
	if n, err := filter.Write(input); err != nil || n != len(input) {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	if err := filter.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != "leftright" {
		t.Fatalf("downstream = %q", got)
	}
	if len(events) != 1 || events[0].Payload != "YQ==" {
		t.Fatalf("events = %#v", events)
	}
}
