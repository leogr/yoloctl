package term

import "testing"

// ICH/DCH/ECH must clip at the right margin. Besides text, exercising rendering
// after editing checks that the format runs still fit the physical row.
func TestCharacterEditingClipsAtMargin(t *testing.T) {
	for _, tc := range []struct{ name, edit, want string }{
		{"insert", "\x1b[1;7H\x1b[100@", "abcdef"},
		{"delete", "\x1b[1;4H\x1b[100P", "abc"},
		{"erase", "\x1b[1;7H\x1b[5X", "abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEmulator(t, 1, 8, nil, 0)
			if _, err := e.Write([]byte("abcdefgh" + tc.edit)); err != nil {
				t.Fatal(err)
			}
			if got := e.GridText(); got != tc.want+"\n" {
				t.Errorf("grid=%q, want %q", got, tc.want)
			}
			_ = e.RenderLine(0)
		})
	}
}
