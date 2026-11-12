package chatimage

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi/kitty"
)

func tile(r, c int) string { return ph + string(kitty.Diacritic(r)) + string(kitty.Diacritic(c)) }

func TestFilterLineAdmitsOnlyTheSessionsPlacement(t *testing.T) {
	p := Placement{ID: 0x010203, Rows: 2, Cols: 3}
	link := "\x1b]8;;https://example.org\x1b\\"
	good := "x " + rgb(p.ID) + tile(0, 0) + tile(0, 2) + "\x1b[0m " + link + "y\x1b]8;;\x1b\\"
	if got := FilterLine(good, p, true); got != good {
		t.Fatalf("valid cells changed:\n%q\n%q", got, good)
	}
	for name, bad := range map[string]string{
		"other id":         rgb(0x0a0b0c) + tile(0, 0),
		"no colour":        tile(0, 0),
		"palette colour":   "\x1b[38;5;196m" + tile(0, 0),
		"row outside":      rgb(p.ID) + tile(2, 0),
		"column outside":   rgb(p.ID) + tile(0, 3),
		"missing marks":    rgb(p.ID) + ph,
		"one mark":         rgb(p.ID) + ph + string(kitty.Diacritic(0)),
		"id byte mark":     rgb(p.ID) + tile(0, 0) + string(kitty.Diacritic(1)),
		"unknown mark":     rgb(p.ID) + ph + "̀̅",
		"reset colour":     rgb(p.ID) + "\x1b[0m" + tile(0, 0),
		"default fg after": rgb(p.ID) + "\x1b[39m" + tile(0, 0),
	} {
		got := FilterLine("a"+bad+"b", p, true)
		if strings.ContainsRune(got, Placeholder) || !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, " b") {
			t.Errorf("%s: %q", name, got)
		}
	}
	// A session without a registered preview shows no placeholder at all.
	if got := FilterLine(good, p, false); strings.ContainsRune(got, Placeholder) {
		t.Fatalf("unregistered session kept a placeholder: %q", got)
	}
	// Width is kept: one inert cell per placeholder cell.
	if got := FilterLine("|"+tile(0, 0)+tile(0, 1)+"|", p, false); got != "|  |" {
		t.Fatalf("width changed: %q", got)
	}
}

func TestRegistryIsPerOwnerAndRecordsChips(t *testing.T) {
	l := NewLedger()
	r := NewRegistry(l)
	a, b := new(int), new(int)
	r.Activate(a, Placement{ID: 7, Rows: 1, Cols: 1}, "[file a.png · 1 KiB]")
	if _, ok := r.Active(b); ok {
		t.Fatal("another owner inherited the preview")
	}
	if p, ok := r.Active(a); !ok || p.ID != 7 {
		t.Fatalf("active %v %v", p, ok)
	}
	if l.Chip(7) != "[file a.png · 1 KiB]" {
		t.Fatal("chip not recorded")
	}
	r.Activate(a, Placement{ID: 8, Rows: 1, Cols: 1}, "[file b.png · 1 KiB]")
	if p, _ := r.Active(a); p.ID != 8 {
		t.Fatal("one preview per owner")
	}
	r.Release(a)
	if _, ok := r.Active(a); ok {
		t.Fatal("released preview still active")
	}
	if l.Chip(8) != "[file b.png · 1 KiB]" {
		t.Fatal("copy provenance lost on release")
	}
	for _, bad := range []Placement{{ID: 0, Rows: 1, Cols: 1}, {ID: 1 << 24, Rows: 1, Cols: 1}, {ID: 9}} {
		r.Activate(b, bad, "x")
		if _, ok := r.Active(b); ok {
			t.Fatalf("invalid placement %+v accepted", bad)
		}
	}
}

// Malformed escapes cannot hide placeholder cells (review step 3 F2).
func TestFilterLineScansEscapesStrictly(t *testing.T) {
	p := Placement{ID: 0x010203, Rows: 1, Cols: 1}
	cell := rgb(p.ID) + tile(0, 0)
	for name, row := range map[string]string{
		"control":           "a" + tile(0, 0) + "Z",
		"bare ESC":          "a\x1b" + tile(0, 0) + "Z",
		"unfinished CSI":    "a\x1b[" + tile(0, 0) + "Z",
		"CSI params":        "a\x1b[38;2" + tile(0, 0) + "Z",
		"unterminated OSC":  "a\x1b]8;;x" + tile(0, 0) + "Z",
		"OSC ESC not ST":    "a\x1b]8;;x\x1b" + tile(0, 0) + "Z",
		"ESC then non-text": "a\x1b\x01" + tile(0, 0) + "Z",
	} {
		if got := FilterLine(row, p, false); strings.ContainsRune(got, Placeholder) {
			t.Errorf("%s: %q", name, got)
		}
	}
	// Well-formed sequences still pass through and still set the colour.
	ok := "\x1b]8;;https://example.org\x1b\\" + cell + "\x1b]8;;\x07"
	if got := FilterLine(ok, p, true); got != ok {
		t.Fatalf("well-formed row changed: %q", got)
	}
}

func TestNeutralizeTextForChrome(t *testing.T) {
	for in, want := range map[string]string{
		"plain title":                       "plain title",
		"T" + tile(0, 0) + tile(0, 1) + "Z": "T  Z",
		"bare" + ph:                         "bare ",
		"url/" + tile(1, 2) + "?q":          "url/ ?q",
	} {
		if got := NeutralizeText(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
