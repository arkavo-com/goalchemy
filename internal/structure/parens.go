package structure

import "strings"

// Open and Close delimit an expression that needs parentheses unless its
// context already separates it, such as an argument list, an index, the
// right side of an assignment, a return value, or a condition. Resolve turns
// them into parentheses or removes them.
const (
	Open  = "\x02"
	Close = "\x03"
)

// Wrap delimits a non-atomic expression for Resolve.
func Wrap(expr string) string {
	if Atomic(expr) {
		return expr
	}
	return Open + expr + Close
}

// Atomic reports whether expr is an operand that binds at least as tightly as
// any postfix operator: an identifier, literal or parenthesized expression,
// optionally followed by member accesses, calls and indexes.
func Atomic(expr string) bool {
	if expr == "" {
		return false
	}
	i := 0
	group := func() bool {
		depth := 0
		for i < len(expr) {
			c := expr[i]
			switch c {
			case '"':
				i = skipString(expr, i)
				continue
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
				if depth == 0 {
					i++
					return true
				}
			case Open[0], Close[0]:
			}
			i++
		}
		return false
	}
	switch c := expr[0]; {
	case c == '(' || c == '[':
		if !group() {
			return false
		}
	case c == '"':
		i = skipString(expr, 0)
	case c == 'b' && len(expr) > 1 && expr[1] == '"':
		i = skipString(expr, 1)
	case ident(c) || c == '.' || c == '$':
		for i < len(expr) && (ident(expr[i]) || expr[i] == '.' || expr[i] == '$' || expr[i] == ':') {
			i++
		}
	default:
		return false
	}
	for i < len(expr) {
		switch c := expr[i]; {
		case c == '(' || c == '[':
			if !group() {
				return false
			}
		case c == '.':
			i++
			for i < len(expr) && (ident(expr[i]) || expr[i] == '$') {
				i++
			}
		default:
			return false
		}
	}
	return true
}

func ident(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func skipString(s string, i int) int {
	q := s[i]
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		}
		i++
	}
	return i
}

// Resolve replaces each Open/Close pair with parentheses, or drops it where
// the surrounding text already delimits the expression.
func Resolve(s string) string {
	if !strings.Contains(s, Open) {
		return s
	}
	type pair struct{ open, close int }
	var stack []int
	var pairs []pair
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case Open[0]:
			stack = append(stack, i)
		case Close[0]:
			o := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pairs = append(pairs, pair{o, i})
		}
	}
	keep := map[int]bool{}
	for _, p := range pairs {
		if !delimited(s, p.open, p.close) {
			keep[p.open], keep[p.close] = true, true
		}
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case Open[0]:
			if keep[i] {
				b.WriteByte('(')
			}
		case Close[0]:
			if keep[i] {
				b.WriteByte(')')
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func delimited(s string, open, close int) bool {
	before := strings.TrimRight(strings.TrimRight(s[:open], Open), " \t")
	after := strings.TrimLeft(strings.TrimLeft(s[close+1:], Close), " \t")
	next := byte('\n')
	if after != "" {
		next = after[0]
	}
	if strings.HasPrefix(after, "\x00@") {
		next = '\n'
	}
	if before == "" || strings.HasSuffix(before, "\n") || strings.HasSuffix(before, "\x00") {
		return next == '\n' || next == ';'
	}
	prev := before[len(before)-1]
	switch prev {
	case '(', '[', ',', '{':
		return next == ')' || next == ']' || next == ',' || next == '}'
	case '=':
		if len(before) >= 2 {
			switch before[len(before)-2] {
			case '=', '!', '<', '>', '+', '-', '*', '/', '%', '&', '|', '^', ':':
				return false
			}
		}
		return next == ';' || next == '\n'
	}
	for _, kw := range []string{"return", "if", "elif", "while", "for"} {
		if strings.HasSuffix(before, kw) && (len(before) == len(kw) || !ident(before[len(before)-len(kw)-1])) {
			return next == ';' || next == '\n' || next == '{' || next == ':'
		}
	}
	return false
}
