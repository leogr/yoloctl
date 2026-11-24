package markdownview

import "testing"

func TestInlineDelimiterMissesAreLocalToEachLine(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"[a [b [c\n[next](https://example.test)", "[a [b [c\nnext (https://example.test)"},
		{"left <!-- a <b>bold</b> <!-- b\nright <!-- hidden --> end", "left <!-- a bold <!-- b\nright  end"},
	} {
		if got := RenderText(Parse(tc.input, documentOptions())); got != tc.want {
			t.Errorf("render %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}
