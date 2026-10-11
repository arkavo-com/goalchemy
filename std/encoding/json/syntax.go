package json

import (
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
	"github.com/eugenioenko/goalchemy/std/unicode"
	"github.com/eugenioenko/goalchemy/std/unicode/utf8"
)

const (
	faultEOF = iota + 1
	faultText
	faultDepth
)

type fault struct {
	kind  int
	label string
	what  string
	where string
}

var (
	eofFault   = &fault{kind: faultEOF}
	depthFault = &fault{kind: faultDepth}
)

func invalidCharacter(b []byte, where string) *fault {
	_, n := utf8.DecodeRune(b)
	return &fault{kind: faultText, label: "character", what: string(b[:n]), where: where}
}

func invalidEscape(what []byte) *fault {
	if len(what) > 6 {
		return &fault{kind: faultText, label: "surrogate pair", what: string(what), where: "in string"}
	}
	return &fault{kind: faultText, label: "escape sequence", what: string(what), where: "in string"}
}

func (f *fault) syntaxError(pos int) *SyntaxError {
	switch f.kind {
	case faultEOF:
		return &SyntaxError{"unexpected end of JSON input", int64(pos)}
	case faultDepth:
		return &SyntaxError{"exceeded max depth", int64(pos + 1)}
	}
	msg := strings.TrimSuffix("invalid "+f.label+" "+quoteWhat(f.what)+" "+f.where, " ")
	return &SyntaxError{legacyMessage(msg), int64(pos + len(f.what))}
}

// legacyMessage rewords an encoding/json/jsontext error message the way
// encoding/json reports it.
func legacyMessage(msg string) string {
	if i := strings.Index(msg, " (expecting"); i >= 0 && !strings.Contains(msg, " in literal") {
		msg = msg[:i]
	}
	for _, r := range [][2]string{{"object name", "object key"}, {"at start of value", "looking for beginning of value"},
		{"at start of string", "looking for beginning of object key string"}, {"after object value", "after object key:value pair"},
		{"in number", "in numeric literal"}} {
		msg = strings.ReplaceAll(msg, r[0], r[1])
	}
	return msg
}

func (f *fault) atStart() error {
	e := f.syntaxError(0)
	e.Offset = 0
	return e
}

func quoteWhat(what string) string {
	if utf8.RuneCountInString(what) == 1 {
		return quoteRune(what)
	}
	for _, r := range what {
		if r == '`' || r == utf8.RuneError || unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return strconv.Quote(what)
		}
	}
	return "`" + what + "`"
}

func quoteRune(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && n == 1 {
		return `'\x` + strconv.FormatUint(uint64(s[0]), 16) + `'`
	}
	return strconv.QuoteRune(r)
}

func consumeWhitespace(b []byte) int {
	n := 0
	for n < len(b) && (b[n] == ' ' || b[n] == '\t' || b[n] == '\r' || b[n] == '\n') {
		n++
	}
	return n
}

// checkTopLevel validates src as one JSON value surrounded by optional
// whitespace, reporting errors the way encoding/json reports them for
// Compact, Indent and the output of MarshalJSON methods. base is the output
// offset at which the value starts and depth the current nesting depth.
func checkTopLevel(src []byte, base, depth int) *SyntaxError {
	return checkInput(src, base, depth, false)
}

// checkDecodeInput validates src the way encoding/json's Unmarshal does,
// which words a few errors differently from Compact and Indent.
func checkDecodeInput(src []byte) *SyntaxError { return checkInput(src, 0, 1, true) }

func checkInput(src []byte, base, depth int, dec bool) *SyntaxError {
	n := consumeWhitespace(src)
	m, f := checkValue(src[n:], depth, dec)
	if f != nil {
		return f.syntaxError(base + n + m)
	}
	n += m
	n += consumeWhitespace(src[n:])
	if n < len(src) {
		return invalidCharacter(src[n:], "after top-level value").syntaxError(base + n)
	}
	return nil
}

func checkValue(src []byte, depth int, dec bool) (int, *fault) {
	if len(src) == 0 {
		return 0, eofFault
	}
	switch c := src[0]; {
	case c == 'n':
		return checkLiteral(src, "null")
	case c == 'f':
		return checkLiteral(src, "false")
	case c == 't':
		return checkLiteral(src, "true")
	case c == '"':
		return checkString(src)
	case c == '-' || '0' <= c && c <= '9':
		n, f := checkNumber(src)
		if f == eofFault && dec {
			return 0, f
		}
		return n, f
	case c == '{':
		return checkObject(src, depth, dec)
	case c == '[':
		return checkArray(src, depth, dec)
	}
	return 0, invalidCharacter(src, "at start of value")
}

func checkLiteral(b []byte, lit string) (int, *fault) {
	for i := 0; i < len(b) && i < len(lit); i++ {
		if b[i] != lit[i] {
			return i, invalidCharacter(b[i:], "in literal "+lit+" (expecting "+strconv.QuoteRune(rune(lit[i]))+")")
		}
	}
	if len(b) < len(lit) {
		return len(b), eofFault
	}
	return len(lit), nil
}

func checkString(b []byte) (int, *fault) {
	if len(b) == 0 {
		return 0, eofFault
	}
	if b[0] != '"' {
		return 0, invalidCharacter(b, `at start of string (expecting '"')`)
	}
	n := 1
	for n < len(b) {
		for n < len(b) && b[n] < utf8.RuneSelf && ' ' <= b[n] && b[n] != '\\' && b[n] != '"' {
			n++
		}
		if n >= len(b) {
			return n, eofFault
		}
		if b[n] == '"' {
			return n + 1, nil
		}
		r, rn := utf8.DecodeRune(b[n:])
		switch {
		case rn > 1:
			n += rn
		case r == '\\':
			if len(b) < n+2 {
				return n, eofFault
			}
			switch b[n+1] {
			case '/', '"', '\\', 'b', 'f', 'n', 'r', 't':
				n += 2
			case 'u':
				if len(b) < n+6 {
					if hasEscapedUTF16Prefix(b[n:]) {
						return n, eofFault
					}
					return n, invalidEscape(b[n:])
				}
				if !isHex4(b[n+2 : n+6]) {
					return n, invalidEscape(b[n : n+6])
				}
				n += 6
			default:
				return n, invalidEscape(b[n : n+2])
			}
		case r == utf8.RuneError:
			if !utf8.FullRune(b[n:]) {
				return n, eofFault
			}
			n++
		default:
			return n, invalidCharacter(b[n:], "in string (expecting non-control character)")
		}
	}
	return n, eofFault
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func isHex4(b []byte) bool {
	for _, c := range b {
		if !isHexDigit(c) {
			return false
		}
	}
	return true
}

func hasEscapedUTF16Prefix(b []byte) bool {
	for i, c := range b {
		switch {
		case i == 0 && c != '\\':
			return false
		case i == 1 && c != 'u':
			return false
		case i >= 2 && i < 6 && !isHexDigit(c):
			return false
		}
	}
	return true
}

func checkNumber(b []byte) (int, *fault) {
	n := 0
	if b[0] == '-' {
		n++
	}
	switch {
	case n >= len(b):
		return 0, eofFault
	case b[n] == '0':
		n++
	case '1' <= b[n] && b[n] <= '9':
		n++
		for n < len(b) && '0' <= b[n] && b[n] <= '9' {
			n++
		}
	default:
		return n, invalidCharacter(b[n:], "in number (expecting digit)")
	}
	if n < len(b) && b[n] == '.' {
		resume := n
		n++
		switch {
		case n >= len(b):
			return resume, eofFault
		case '0' <= b[n] && b[n] <= '9':
			n++
		default:
			return n, invalidCharacter(b[n:], "in number (expecting digit)")
		}
		for n < len(b) && '0' <= b[n] && b[n] <= '9' {
			n++
		}
	}
	if n < len(b) && (b[n] == 'e' || b[n] == 'E') {
		resume := n
		n++
		if n < len(b) && (b[n] == '-' || b[n] == '+') {
			n++
		}
		switch {
		case n >= len(b):
			return resume, eofFault
		case '0' <= b[n] && b[n] <= '9':
			n++
		default:
			return n, invalidCharacter(b[n:], "in number (expecting digit)")
		}
		for n < len(b) && '0' <= b[n] && b[n] <= '9' {
			n++
		}
	}
	return n, nil
}

func checkObject(src []byte, depth int, dec bool) (int, *fault) {
	if depth == maxNestingDepth+1 {
		return 0, depthFault
	}
	n := 1
	n += consumeWhitespace(src[n:])
	if n >= len(src) {
		return n, eofFault
	}
	if src[n] == '}' {
		return n + 1, nil
	}
	depth++
	for {
		n += consumeWhitespace(src[n:])
		if n >= len(src) {
			return n, eofFault
		}
		m, f := checkString(src[n:])
		if f != nil {
			return n + m, f
		}
		n += m
		n += consumeWhitespace(src[n:])
		if n >= len(src) {
			return n, eofFault
		}
		if src[n] != ':' {
			return n, invalidCharacter(src[n:], "after object name (expecting ':')")
		}
		n++
		n += consumeWhitespace(src[n:])
		if n >= len(src) {
			return n, eofFault
		}
		m, f = checkValue(src[n:], depth, dec)
		if f != nil {
			return n + m, f
		}
		n += m
		n += consumeWhitespace(src[n:])
		if n >= len(src) {
			return n, eofFault
		}
		switch src[n] {
		case ',':
			n++
		case '}':
			return n + 1, nil
		default:
			return n, invalidCharacter(src[n:], "after object value (expecting ',' or '}')")
		}
	}
}

func checkArray(src []byte, depth int, dec bool) (int, *fault) {
	if depth == maxNestingDepth+1 {
		return 0, depthFault
	}
	n := 1
	n += consumeWhitespace(src[n:])
	if n >= len(src) {
		return n, eofFault
	}
	if src[n] == ']' {
		return n + 1, nil
	}
	depth++
	for {
		n += consumeWhitespace(src[n:])
		if n >= len(src) {
			return n, eofFault
		}
		m, f := checkValue(src[n:], depth, dec)
		if f != nil {
			return n + m, f
		}
		n += m
		n += consumeWhitespace(src[n:])
		if n >= len(src) {
			return n, eofFault
		}
		switch src[n] {
		case ',':
			n++
		case ']':
			return n + 1, nil
		default:
			if dec {
				return n, invalidCharacter(src[n:], "after array element (expecting ',' or ']')")
			}
			return n, invalidCharacter(src[n:], "after array value (expecting ',' or ']')")
		}
	}
}
