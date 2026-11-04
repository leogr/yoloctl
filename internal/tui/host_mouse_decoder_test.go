package tui

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// Ghostty's captured mode-1016 reports include both zero and negative pixel
// positions. UV's cell decoder convention subtracts one; the pixel adapter
// must add it back, but the decoder must first preserve the whole report.
func TestHostDecodesSignedSGRMouse(t *testing.T) {
	for _, encoded := range []string{"\x1b[<0;0;7M", "\x1b[<0;-1;16M", "\x1b[<0;-1;16m", "\x1b[<32;12;-4M"} {
		var d uv.EventDecoder
		n, event := d.Decode([]byte(encoded))
		mouse, ok := event.(uv.MouseEvent)
		if !ok || n != len(encoded) {
			t.Fatalf("report %q split into %T %+v (%d bytes)", encoded, event, event, n)
		}
		if strings.Contains(encoded, ";-1;") && mouse.Mouse().X != -2 {
			t.Fatalf("signed coordinate changed: %+v", mouse)
		}
	}
}

func TestHostRejectsMalformedSGRWithoutSuffixKeys(t *testing.T) {
	for _, report := range []string{
		"\x1b[<0;--1;2M", "\x1b[<0;1;-2147483648m", "\x1b[<256;1;2M",
		"\x1b[<0;99999999999999999999999;2M", "\x1b[<0;1;2;3M",
	} {
		var d uv.EventDecoder
		n, event := d.Decode([]byte(report + "x"))
		if _, ok := event.(uv.UnknownCsiEvent); !ok || n != len(report) {
			t.Fatalf("malformed mouse report leaked its suffix: %T %v, bytes=%d", event, event, n)
		}
	}
}

type mouseDecoderModel struct{ events []string }

func (m *mouseDecoderModel) Init() tea.Cmd  { return nil }
func (m *mouseDecoderModel) View() tea.View { return tea.NewView("") }
func (m *mouseDecoderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		mo := msg.Mouse()
		m.events = append(m.events, fmt.Sprintf("%T %d %d", msg, mo.X, mo.Y))
	case tea.KeyPressMsg:
		if msg.Code == tea.KeyF12 {
			return m, tea.Quit
		}
		m.events = append(m.events, "key:"+msg.String())
	}
	return m, nil
}

type bytewiseReader struct{ io.Reader }

func (r bytewiseReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(len(p), 1)]) }

func TestSignedMouseThroughFragmentedHostInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m := &mouseDecoderModel{}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(io.Discard),
		tea.WithInput(bytewiseReader{strings.NewReader("\x1b[<0;-1;16M\x1b[<0;-1;16m\x1b[<32;12;-4M" + "x\x1b[24~")}),
		tea.WithWindowSize(80, 24), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignals())
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	want := []string{"tea.MouseClickMsg -2 15", "tea.MouseReleaseMsg -2 15", "tea.MouseMotionMsg 11 -5", "key:x"}
	if !reflect.DeepEqual(m.events, want) {
		t.Fatalf("signed mouse leaked keyboard input: %q; want %q", m.events, want)
	}
}
