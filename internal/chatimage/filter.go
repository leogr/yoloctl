package chatimage

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi/kitty"
)

// Placement is one displayed preview: its 24-bit image ID and its size in
// cells. Placeholder cells are valid only inside it.
type Placement struct {
	ID         uint32
	Rows, Cols int
	Token      string // host-issued; the child releases by it
}

// Registry records, per owning session, the one preview it may display. The
// owner is the host's session value (compared by identity), so a replaced or
// reopened session never inherits an earlier registration. IDs route cells to
// images; they are not secrets.
type Registry struct {
	mu     sync.Mutex
	active map[any]Placement
	ledger *Ledger
}

// NewRegistry returns an empty registry that records chips in ledger.
func NewRegistry(ledger *Ledger) *Registry {
	return &Registry{active: map[any]Placement{}, ledger: ledger}
}

// Activate makes p the owner's one displayed preview, replacing any earlier
// one, and records its chip for copy.
// It returns the replaced preview, whose image the caller deletes.
func (r *Registry) Activate(owner any, p Placement, chip string) (replaced Placement, ok bool) {
	if r == nil || owner == nil || p.ID == 0 || p.ID > 0xffffff || p.Rows < 1 || p.Cols < 1 {
		return Placement{}, false
	}
	r.mu.Lock()
	replaced, ok = r.active[owner]
	r.active[owner] = p
	r.mu.Unlock()
	r.ledger.Remember(p.ID, chip)
	return replaced, ok
}

// ReleaseToken forgets the owner's preview only if token names it, so a
// stale release cannot remove a newer preview.
func (r *Registry) ReleaseToken(owner any, token string) (Placement, bool) {
	if r == nil {
		return Placement{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.active[owner]; ok && token != "" && p.Token == token {
		delete(r.active, owner)
		return p, true
	}
	return Placement{}, false
}

// Release forgets the owner's preview (close, access loss) and returns it.
func (r *Registry) Release(owner any) (Placement, bool) {
	if r == nil {
		return Placement{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.active[owner]
	delete(r.active, owner)
	return p, ok
}

// All returns every registered preview, for freeing them when the host exits.
func (r *Registry) All() []Placement {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Placement, 0, len(r.active))
	for _, p := range r.active {
		out = append(out, p)
	}
	return out
}

// Active returns the owner's displayed preview, if any.
func (r *Registry) Active(owner any) (Placement, bool) {
	if r == nil || owner == nil {
		return Placement{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.active[owner]
	return p, ok
}

// inert replaces a placeholder cell that may not be shown: same width, no
// image.
const inert = " "

// diacriticIndex maps each Kitty row/column diacritic to its number.
// kitty.Diacritic returns the first entry for any out-of-range index, so the
// table ends where that entry repeats.
var diacriticIndex = func() map[rune]int {
	first := kitty.Diacritic(0)
	m := map[rune]int{first: 0}
	for i := 1; i < 4096; i++ { // the protocol defines a few hundred
		r := kitty.Diacritic(i)
		if r == first {
			break
		}
		m[r] = i
	}
	return m
}()

// FilterLine neutralizes every Kitty placeholder cell in one rendered row that
// is not a complete cell of p: exactly a row and a column mark inside p's
// size, drawn in p's 24-bit ID colour. With ok false (the session displays no
// preview) every placeholder cell is neutralized. Escape sequences and other
// text are kept byte for byte.
func FilterLine(line string, p Placement, ok bool) string {
	if !strings.ContainsRune(line, Placeholder) {
		return line
	}
	var out strings.Builder
	out.Grow(len(line))
	var fg uint32
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			n, next := escapeLen(line[i:], fg)
			fg = next
			out.WriteString(line[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if r != Placeholder {
			out.WriteString(line[i : i+size])
			i += size
			continue
		}
		j := i + size
		var marks []int
		valid := true
		for j < len(line) {
			m, msize := utf8.DecodeRuneInString(line[j:])
			if !unicode.Is(unicode.Mn, m) {
				break
			}
			idx, known := diacriticIndex[m]
			if !known {
				valid = false
			}
			marks = append(marks, idx)
			j += msize
		}
		if ok && valid && fg == p.ID && len(marks) == 2 && marks[0] < p.Rows && marks[1] < p.Cols {
			out.WriteString(line[i:j])
		} else {
			out.WriteString(inert)
		}
		i = j
	}
	return out.String()
}

// escapeLen is the length of a well-formed escape sequence at the start of s
// and the 24-bit foreground after it (only SGR changes it). It is strict, so
// malformed input cannot hide cells: a CSI is parameter and intermediate bytes
// (0x20–0x3f) and one final byte (0x40–0x7e), and ends before any other byte;
// an OSC must end with BEL or ST, else only its introducer is consumed; ESC
// with a byte outside 0x20–0x7e is consumed alone.
func escapeLen(s string, fg uint32) (int, uint32) {
	if len(s) < 2 {
		return len(s), fg
	}
	switch s[1] {
	case '[':
		for k := 2; k < len(s); k++ {
			switch c := s[k]; {
			case c >= 0x20 && c <= 0x3f:
				continue
			case c >= 0x40 && c <= 0x7e:
				if c == 'm' {
					if _, next, ok := parseSGR(s[:k+1], fg); ok {
						fg = next
					}
				}
				return k + 1, fg
			default:
				return k, fg
			}
		}
		return len(s), fg
	case ']':
		for k := 2; k < len(s); k++ {
			if s[k] == 0x07 {
				return k + 1, fg
			}
			if s[k] == 0x1b {
				if k+1 < len(s) && s[k+1] == '\\' {
					return k + 2, fg
				}
				break
			}
		}
		return 2, fg
	}
	if s[1] >= 0x20 && s[1] <= 0x7e {
		return 2, fg
	}
	return 1, fg
}

// NeutralizeText replaces every Kitty placeholder cell (U+10EEEE and its
// combining marks) in plain chrome text, such as a workload-set title or a
// link shown in a prompt, with one space. Such text never displays a preview.
func NeutralizeText(s string) string {
	if !strings.ContainsRune(s, Placeholder) {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != Placeholder {
			out.WriteString(s[i : i+size])
			i += size
			continue
		}
		i += size
		for i < len(s) {
			m, msize := utf8.DecodeRuneInString(s[i:])
			if !unicode.Is(unicode.Mn, m) {
				break
			}
			i += msize
		}
		out.WriteString(inert)
	}
	return out.String()
}
