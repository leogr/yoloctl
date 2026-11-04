package chatui

import (
	"strings"

	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Syntax highlighting for fenced code: a deliberately small, dependency-free
// lexer for common languages. It colors keywords, strings, comments, numbers
// and well-known types or constants, carries block comments and multi-line
// strings across a block's lines, and never changes, adds or drops a byte:
// it only styles the line's text, so display, wrapping and copy are as
// before. An unknown or missing language stays plain.

// tokenClass is what a highlighted span is.
type tokenClass int

const (
	tokPlain tokenClass = iota
	tokKeyword
	tokString
	tokComment
	tokNumber
	tokType // types, builtins and constants
	tokKey  // a JSON/YAML/TOML key or section
	tokAdded
	tokRemoved
	tokMeta // a diff hunk or file header, a shell prompt
)

// syntax describes one language for the lexer.
type syntax struct {
	line     []string // line comment openers
	block    [2]string
	quotes   string // single-line string delimiters
	multi    string // delimiters whose strings may span lines (Go/JS backticks)
	triple   bool   // Python triple-quoted strings
	hashLine bool   // '#' comments only at a word boundary (shells)
	keywords map[string]bool
	types    map[string]bool
	fold     bool // keywords are case-insensitive (SQL)
	dollar   bool // $NAME and ${NAME} are variables (shells, PHP)
	mode     syntaxMode
}

type syntaxMode int

const (
	modeCode syntaxMode = iota
	modeJSON
	modeYAML
	modeTOML
	modeDiff
)

// hlState carries what continues onto the next line of the same block.
type hlState struct {
	comment bool   // inside a block comment
	str     string // the open multi-line string's delimiter
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var (
	cTypes = "bool char double float int long short signed unsigned void size_t int8_t int16_t int32_t int64_t uint8_t uint16_t uint32_t uint64_t NULL true false nullptr"
	cKeys  = "auto break case const continue default do else enum extern for goto if inline register restrict return sizeof static struct switch typedef union volatile while #include #define #ifdef #ifndef #endif #if #else #elif #pragma #undef"

	goSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`, multi: "`",
		keywords: words("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var"),
		types:    words("bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr any comparable true false nil iota append cap clear close copy delete len make max min new panic print println recover")}
	pySyntax = &syntax{line: []string{"#"}, quotes: `"'`, triple: true,
		keywords: words("and as assert async await break class continue def del elif else except finally for from global if import in is lambda match case nonlocal not or pass raise return try while with yield"),
		types:    words("True False None self cls int float str bytes bool list dict set tuple object type len print range open isinstance super Exception ValueError TypeError KeyError")}
	jsSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`, multi: "`",
		keywords: words("async await break case catch class const continue debugger default delete do else export extends finally for from function if import in instanceof let new of return static super switch this throw try typeof var void while with yield"),
		types:    words("true false null undefined NaN Infinity console window document Promise Array Object String Number Boolean Map Set Error JSON Math")}
	tsSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`, multi: "`",
		keywords: words("abstract as async await break case catch class const continue declare default delete do else enum export extends finally for from function if implements import in instanceof interface keyof let namespace new of private protected public readonly return satisfies static super switch this throw try type typeof var void while yield"),
		types:    words("true false null undefined any unknown never string number boolean bigint symbol object void Promise Array Record Partial Readonly Map Set Error")}
	rustSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"`,
		keywords: words("as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while"),
		types:    words("bool char f32 f64 i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize str String Vec Option Result Box Some None Ok Err true false")}
	cSyntax   = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`, keywords: words(cKeys), types: words(cTypes)}
	cppSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`,
		keywords: words(cKeys + " catch class constexpr delete explicit friend mutable namespace new noexcept operator private protected public template this throw try typename using virtual override final"),
		types:    words(cTypes + " std string vector map auto")}
	javaSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`,
		keywords: words("abstract assert break case catch class continue default do else enum extends final finally for if implements import instanceof interface native new package private protected public record return static super switch synchronized this throw throws try var void volatile while yield"),
		types:    words("boolean byte char double float int long short String Object Integer List Map true false null")}
	csSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`,
		keywords: words("abstract as async await base break case catch class const continue default delegate do else enum event explicit extern finally fixed for foreach if implicit in interface internal is lock namespace new operator out override params private protected public readonly ref return sealed sizeof static struct switch this throw try typeof using var virtual void volatile while"),
		types:    words("bool byte char decimal double float int long object sbyte short string uint ulong ushort true false null")}
	kotlinSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"'`,
		keywords: words("as break class continue data do else enum for fun if import in interface is object override package private public return sealed super this throw try typealias val var when while"),
		types:    words("Any Boolean Char Double Float Int Long String Unit List Map true false null")}
	swiftSyntax = &syntax{line: []string{"//"}, block: [2]string{"/*", "*/"}, quotes: `"`,
		keywords: words("as break case catch class continue default defer do else enum extension fileprivate for func guard if import in init inout internal let open private protocol public repeat return self static struct switch throw throws try var where while"),
		types:    words("Bool Character Double Float Int String Array Dictionary Optional true false nil")}
	rubySyntax = &syntax{line: []string{"#"}, quotes: `"'`,
		keywords: words("alias and begin break case class def defined? do else elsif end ensure for if in module next not or redo rescue retry return self super then unless until when while yield require attr_accessor"),
		types:    words("true false nil")}
	phpSyntax = &syntax{line: []string{"//", "#"}, block: [2]string{"/*", "*/"}, quotes: `"'`, dollar: true,
		keywords: words("abstract as break case catch class const continue declare default do echo else elseif extends final finally fn for foreach function global if implements include interface namespace new private protected public require return static switch throw trait try use while yield"),
		types:    words("true false null array string int float bool self parent")}
	luaSyntax = &syntax{line: []string{"--"}, quotes: `"'`,
		keywords: words("and break do else elseif end for function goto if in local not or repeat return then until while"),
		types:    words("true false nil self print pairs ipairs require table string math")}
	shSyntax = &syntax{line: []string{"#"}, hashLine: true, quotes: `"'`, dollar: true,
		keywords: words("if then else elif fi for while until do done case esac in function return break continue export local readonly unset set shift exit source alias select"),
		types:    words("echo printf cd ls cat grep sed awk find xargs sudo git make go npm docker curl test true false read eval exec trap")}
	sqlSyntax = &syntax{line: []string{"--"}, block: [2]string{"/*", "*/"}, quotes: `'"`, fold: true,
		keywords: words("select from where and or not insert into values update set delete create table drop alter add column index view as join inner left right outer full on group by order having limit offset union all distinct case when then else end is null in like between exists primary key foreign references default begin commit rollback transaction with returning"),
		types:    words("int integer bigint smallint text varchar char boolean bool date timestamp numeric real serial true false")}
	dockerSyntax = &syntax{line: []string{"#"}, hashLine: true, quotes: `"'`, dollar: true, fold: true,
		keywords: words("from run cmd label maintainer expose env add copy entrypoint volume user workdir arg onbuild stopsignal healthcheck shell as")}
	makeSyntax = &syntax{line: []string{"#"}, hashLine: true, quotes: `"'`, dollar: true,
		keywords: words("ifeq ifneq ifdef ifndef else endif include define endef export override .PHONY")}
	jsonSyntax = &syntax{quotes: `"`, mode: modeJSON, types: words("true false null")}
	yamlSyntax = &syntax{line: []string{"#"}, hashLine: true, quotes: `"'`, mode: modeYAML, types: words("true false null yes no on off ~")}
	tomlSyntax = &syntax{line: []string{"#"}, quotes: `"'`, mode: modeTOML, types: words("true false")}
	diffSyntax = &syntax{mode: modeDiff}
)

// syntaxes maps a fence's language (its info string's first word, folded)
// to its lexer.
var syntaxes = map[string]*syntax{
	"go": goSyntax, "golang": goSyntax,
	"python": pySyntax, "py": pySyntax, "python3": pySyntax,
	"javascript": jsSyntax, "js": jsSyntax, "jsx": jsSyntax, "mjs": jsSyntax, "cjs": jsSyntax, "node": jsSyntax,
	"typescript": tsSyntax, "ts": tsSyntax, "tsx": tsSyntax,
	"rust": rustSyntax, "rs": rustSyntax,
	"c": cSyntax, "h": cSyntax,
	"cpp": cppSyntax, "c++": cppSyntax, "cc": cppSyntax, "cxx": cppSyntax, "hpp": cppSyntax,
	"java": javaSyntax, "csharp": csSyntax, "cs": csSyntax, "c#": csSyntax,
	"kotlin": kotlinSyntax, "kt": kotlinSyntax, "swift": swiftSyntax,
	"ruby": rubySyntax, "rb": rubySyntax, "php": phpSyntax, "lua": luaSyntax,
	"sh": shSyntax, "bash": shSyntax, "zsh": shSyntax, "shell": shSyntax, "console": shSyntax, "shell-session": shSyntax, "fish": shSyntax, "ksh": shSyntax,
	"sql": sqlSyntax, "postgresql": sqlSyntax, "postgres": sqlSyntax, "mysql": sqlSyntax, "sqlite": sqlSyntax,
	"dockerfile": dockerSyntax, "docker": dockerSyntax, "containerfile": dockerSyntax,
	"make": makeSyntax, "makefile": makeSyntax, "mk": makeSyntax,
	"json": jsonSyntax, "jsonc": jsonSyntax, "json5": jsonSyntax,
	"yaml": yamlSyntax, "yml": yamlSyntax, "toml": tomlSyntax,
	"diff": diffSyntax, "patch": diffSyntax,
}

// syntaxFor picks the lexer for a fence's info string, nil when unknown.
func syntaxFor(info string) *syntax {
	lang, _, _ := strings.Cut(strings.TrimSpace(info), " ")
	lang = strings.ToLower(strings.TrimPrefix(strings.Trim(lang, "{}"), "."))
	return syntaxes[lang]
}

// tokenStyle colors a class over the code card, from the theme's existing
// palette so both themes keep their contrast.
func tokenStyle(c tokenClass, base spaceview.Style, t spaceview.Theme) spaceview.Style {
	st := base
	switch c {
	case tokKeyword:
		st.FG = t.Account
	case tokString:
		st.FG = t.OK
	case tokComment:
		st.FG, st.Italic = t.Dim, true
	case tokNumber:
		st.FG = t.Workdir
	case tokType:
		st.FG = t.Port
	case tokKey:
		st.FG = t.Label
	case tokAdded:
		st.FG = t.OK
	case tokRemoved:
		st.FG = t.Bad
	case tokMeta:
		st.FG = t.Brand
	}
	return st
}

// highlightLine styles one code line. The pieces cover text exactly, in
// order; the returned state continues onto the block's next line.
func highlightLine(sx *syntax, text string, state hlState, base spaceview.Style, t spaceview.Theme) ([]piece, hlState) {
	if sx == nil {
		return []piece{{end: len(text), style: base, code: true}}, state
	}
	var pieces []piece
	emit := func(start, end int, c tokenClass) {
		if start >= end {
			return
		}
		st := tokenStyle(c, base, t)
		if n := len(pieces); n > 0 && pieces[n-1].style == st && pieces[n-1].end == start {
			pieces[n-1].end = end
			return
		}
		pieces = append(pieces, piece{start: start, end: end, style: st, code: true})
	}
	if sx.mode == modeDiff {
		emit(0, len(text), diffClass(text))
		return pieces, state
	}
	i := 0
	// Continue a block comment or multi-line string from the previous line.
	if state.comment {
		end := strings.Index(text, sx.block[1])
		if end < 0 {
			emit(0, len(text), tokComment)
			return pieces, state
		}
		i = end + len(sx.block[1])
		emit(0, i, tokComment)
		state.comment = false
	} else if state.str != "" {
		end := closingQuote(text, 0, state.str)
		if end < 0 {
			emit(0, len(text), tokString)
			return pieces, state
		}
		i = end
		emit(0, i, tokString)
		state.str = ""
	}
	// Only a line's first token can be a YAML/TOML key or section.
	keyAt := -1
	if sx.mode == modeYAML || sx.mode == modeTOML {
		keyAt = firstNonSpace(text, i)
	}
	plainFrom := i
	flush := func(to int) { emit(plainFrom, to, tokPlain) }
	for i < len(text) {
		rest := text[i:]
		c := text[i]
		switch {
		case i == keyAt && yamlKeyEnd(sx, text, i) > i:
			flush(i)
			end := yamlKeyEnd(sx, text, i)
			emit(i, end, tokKey)
			i, plainFrom = end, end
			continue
		case sx.block[0] != "" && strings.HasPrefix(rest, sx.block[0]):
			flush(i)
			end := strings.Index(text[i+len(sx.block[0]):], sx.block[1])
			if end < 0 {
				emit(i, len(text), tokComment)
				state.comment = true
				return pieces, state
			}
			end += i + len(sx.block[0]) + len(sx.block[1])
			emit(i, end, tokComment)
			i, plainFrom = end, end
			continue
		case lineComment(sx, text, i):
			flush(i)
			emit(i, len(text), tokComment)
			return pieces, state
		case sx.triple && (strings.HasPrefix(rest, `"""`) || strings.HasPrefix(rest, "'''")):
			flush(i)
			delim := rest[:3]
			end := closingQuote(text, i+3, delim)
			if end < 0 {
				emit(i, len(text), tokString)
				state.str = delim
				return pieces, state
			}
			emit(i, end, tokString)
			i, plainFrom = end, end
			continue
		case strings.IndexByte(sx.multi, c) >= 0:
			flush(i)
			end := closingQuote(text, i+1, string(c))
			if end < 0 {
				emit(i, len(text), tokString)
				state.str = string(c)
				return pieces, state
			}
			emit(i, end, tokString)
			i, plainFrom = end, end
			continue
		case strings.IndexByte(sx.quotes, c) >= 0 && (c != '\'' || sx != rustSyntax):
			flush(i)
			end := closingQuote(text, i+1, string(c))
			if end < 0 {
				end = len(text) // an unclosed single-line string ends the line
			}
			class := tokString
			if sx.mode == modeJSON && jsonKey(text, end) {
				class = tokKey
			}
			emit(i, end, class)
			i, plainFrom = end, end
			continue
		case sx.dollar && c == '$' && i+1 < len(text) && (text[i+1] == '{' || text[i+1] == '(' || identStart(text[i+1])):
			flush(i)
			end := i + 1
			if text[end] == '{' || text[end] == '(' {
				closer := byte('}')
				if text[end] == '(' {
					closer = ')'
				}
				if k := strings.IndexByte(text[end:], closer); k >= 0 {
					end += k + 1
				} else {
					end = len(text)
				}
			} else {
				for end < len(text) && identByte(text[end]) {
					end++
				}
			}
			emit(i, end, tokType)
			i, plainFrom = end, end
			continue
		case digit(c) && (i == 0 || !identByte(text[i-1])):
			flush(i)
			end := i + 1
			for end < len(text) && (identByte(text[end]) || text[end] == '.' && end+1 < len(text) && digit(text[end+1])) {
				end++
			}
			emit(i, end, tokNumber)
			i, plainFrom = end, end
			continue
		case identStart(c) || c == '#' && sx.keywords["#include"]:
			end := i + 1
			for end < len(text) && (identByte(text[end]) || text[end] == '?' && sx == rubySyntax) {
				end++
			}
			if i > 0 && identByte(text[i-1]) {
				i = end
				continue
			}
			word := text[i:end]
			if sx.fold {
				word = strings.ToLower(word)
			}
			switch {
			case sx.keywords[word]:
				flush(i)
				emit(i, end, tokKeyword)
				plainFrom = end
			case sx.types[word] || sx.fold && sx.types[strings.ToLower(word)]:
				flush(i)
				emit(i, end, tokType)
				plainFrom = end
			}
			i = end
			continue
		}
		i++
	}
	flush(len(text))
	if len(pieces) == 0 {
		pieces = []piece{{end: len(text), style: base, code: true}}
	}
	return pieces, state
}

func diffClass(line string) tokenClass {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index "):
		return tokMeta
	case strings.HasPrefix(line, "+"):
		return tokAdded
	case strings.HasPrefix(line, "-"):
		return tokRemoved
	}
	return tokPlain
}

// lineComment reports whether a line comment starts at i.
func lineComment(sx *syntax, text string, i int) bool {
	for _, open := range sx.line {
		if strings.HasPrefix(text[i:], open) {
			if open == "#" && sx.hashLine && i > 0 && text[i-1] != ' ' && text[i-1] != '\t' {
				continue // a#b or ${#x} in a shell is not a comment
			}
			return true
		}
	}
	return false
}

// closingQuote is the offset just past the delimiter that closes a string
// whose body starts at from, honoring backslash escapes except in raw
// backtick strings; -1 when the line does not close it.
func closingQuote(text string, from int, delim string) int {
	for i := from; i < len(text); i++ {
		if text[i] == '\\' && delim != "`" {
			i++
			continue
		}
		if strings.HasPrefix(text[i:], delim) {
			return i + len(delim)
		}
	}
	return -1
}

// jsonKey reports whether the string ending at end is an object key.
func jsonKey(text string, end int) bool {
	rest := strings.TrimLeft(text[end:], " \t")
	return strings.HasPrefix(rest, ":")
}

// yamlKeyEnd is where a YAML "key:" or a TOML "key =" / "[section]" that
// starts at i ends, or i when the line has none there.
func yamlKeyEnd(sx *syntax, text string, i int) int {
	if sx.mode == modeYAML && strings.HasPrefix(text[i:], "- ") {
		return i // the item marker stays plain; its key follows
	}
	if sx.mode == modeTOML && text[i] == '[' {
		if k := strings.IndexByte(text[i:], ']'); k > 0 {
			return i + k + 1
		}
		return i
	}
	end := i
	for end < len(text) && (identByte(text[end]) || text[end] == '-' || text[end] == '.') {
		end++
	}
	if end == i {
		return i
	}
	switch rest := text[end:]; {
	case sx.mode == modeYAML && (rest == ":" || strings.HasPrefix(rest, ": ") || strings.HasPrefix(rest, ":\t")):
		return end
	case sx.mode == modeTOML && strings.HasPrefix(strings.TrimLeft(rest, " \t"), "="):
		return end
	}
	return i
}

// firstNonSpace is the first offset at or after from that is not a space
// or tab, or a YAML list marker's key position.
func firstNonSpace(text string, from int) int {
	i := from
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if strings.HasPrefix(text[i:], "- ") {
		i += 2
	}
	return i
}

func digit(c byte) bool      { return c >= '0' && c <= '9' }
func identStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func identByte(c byte) bool  { return identStart(c) || digit(c) }
