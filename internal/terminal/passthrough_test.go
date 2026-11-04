package terminal_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

func TestPassthroughNeverInvitesAnotherTerminalReply(t *testing.T) {
	// Exercise queries through a real receiving parser, including strings
	// containing query-looking bytes and every split of each sequence.
	queries := []string{
		"\x1bZ", "\x1b[c", "\x1b[>c", "\x1b[=c", "\x1b[5n", "\x1b[6n", "\x1b[?6n",
		"\x1b[?u", "\x1b[?0u", "\x1b[?2004$p", "\x1b[>0q", "\x1b[18t", "\x1b[14t",
		"\x1bP$qm\x1b\\", "\x1bP+q544e\x1b\\", "\x1b]4;1;?\a", "\x1b]10;?;?\x1b\\",
		"\x1b]52;c;?\a", "\x1b_Ga=q,i=1;AAAA\x1b\\", "\x9b6n",
		"\x1bPtmux;\x1b\x1b[6n\x1b\\",
		"\x1b[16t", "\x1b[?2048h", "\x1b[?1004;2048h",
	}
	vt, err := terminal.New(24, 80, 10, func(p []byte) { t.Errorf("receiving terminal answered %q", p) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	for _, q := range queries {
		for cut := 0; cut <= len(q); cut++ {
			filter, err := terminal.New(1, 1, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			a, err := filter.FilterPassthrough([]byte(q[:cut]))
			if err != nil {
				t.Fatal(err)
			}
			b, err := filter.FilterPassthrough([]byte(q[cut:]))
			if err != nil {
				t.Fatal(err)
			}
			out := append(a, b...)
			if _, err := vt.Write(out); err != nil {
				t.Fatal(err)
			}
			_ = filter.Close()
		}
	}
}

func TestPassthroughPreservesOrdinaryDisplayAndInputModes(t *testing.T) {
	stream := "héllo 👩‍💻\r\n\x1b[2;3H\x1b[38;2;1;2;3mcolor\x1b[0m\x1b[?2004h\x1b[>3u\x1b]2;title\x1b\\\x1b]52;c;Y29weQ==\x1b\\"
	filter, err := terminal.New(1, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = filter.Close() }()
	var out []byte
	for _, b := range []byte(stream) {
		part, err := filter.FilterPassthrough([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, part...)
	}
	if !bytes.Equal(out, []byte(stream)) {
		t.Fatalf("output %q, want %q", out, stream)
	}
	if strings.Contains(string(out), "?") && !bytes.Contains(out, []byte("\x1b[?2004h")) {
		t.Fatal("lost input mode")
	}
}

func TestPassthroughRemovesSizeNotificationsFromMixedModes(t *testing.T) {
	vt, err := terminal.New(1, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	for _, final := range []string{"h", "s", "r", "l"} {
		out, err := vt.FilterPassthrough([]byte("\x1b[?1004;2048;2004" + final))
		want := "\x1b[?1004" + final + "\x1b[?2004" + final
		if err != nil || string(out) != want {
			t.Fatalf("mixed modes: %q, %v; want %q", out, err, want)
		}
	}
}

func TestPassthroughBellTitle(t *testing.T) {
	f, err := terminal.New(1, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	const stream = "before\a\x1b]2;Ü title\x1b\\after"
	out, err := f.FilterPassthrough([]byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != stream {
		t.Fatalf("got %q want %q", out, stream)
	}
}
