// Package jcs implements the JSON Canonicalization Scheme of RFC 8785.
//
// Transform parses JSON text and returns its canonical encoding: object
// members sorted by the UTF-16 code units of their names, no insignificant
// whitespace, strings with only the escapes RFC 8785 requires, and numbers
// written as ECMAScript's Number::toString writes them. Invalid JSON,
// duplicate member names, and numbers that are not finite doubles are errors.
package jcs

import (
	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
	"github.com/eugenioenko/goalchemy/std/sort"
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/unicode/utf8"
)

// NumberError reports a JSON number that has no finite IEEE 754 double.
type NumberError struct {
	Text string
}

func (e *NumberError) Error() string { return "jcs: number " + e.Text + " is not a finite double" }

// Transform returns the RFC 8785 canonical encoding of the JSON text data,
// parsed with jsonvalue.DefaultLimits.
func Transform(data []byte) ([]byte, error) {
	return TransformWithLimits(data, jsonvalue.DefaultLimits())
}

// TransformWithLimits is Transform with explicit parsing limits.
func TransformWithLimits(data []byte, l jsonvalue.Limits) ([]byte, error) {
	v, err := jsonvalue.ParseWithLimits(data, l)
	if err != nil {
		return nil, err
	}
	return Encode(v)
}

// Encode returns the RFC 8785 canonical encoding of v. Numbers are read from
// their source text; a number outside the finite double range is a
// NumberError.
func Encode(v jsonvalue.Value) ([]byte, error) {
	var out []byte
	return value(out, v)
}

func value(out []byte, v jsonvalue.Value) ([]byte, error) {
	switch v.Kind() {
	case jsonvalue.KindNull:
		return append(out, "null"...), nil
	case jsonvalue.KindBool:
		if v.Bool() {
			return append(out, "true"...), nil
		}
		return append(out, "false"...), nil
	case jsonvalue.KindNumber:
		return number(out, v.Number())
	case jsonvalue.KindString:
		return str(out, v.String()), nil
	case jsonvalue.KindArray:
		out = append(out, '[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			out, err = value(out, v.Index(i))
			if err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil
	case jsonvalue.KindObject:
		keys := v.Keys()
		sort.Sort(utf16Order(keys))
		out = append(out, '{')
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = str(out, k)
			out = append(out, ':')
			var err error
			out, err = value(out, v.Get(k))
			if err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil
	}
	return nil, &jsonvalue.InvalidValueError{Msg: "cannot encode an invalid Value"}
}

// utf16Order sorts strings by their UTF-16 code units, as RFC 8785 §3.2.3
// requires; supplementary characters sort as surrogate pairs.
type utf16Order []string

func (s utf16Order) Len() int      { return len(s) }
func (s utf16Order) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s utf16Order) Less(i, j int) bool {
	a, b := s[i], s[j]
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		ua, ub := units(ra), units(rb)
		if ua[0] != ub[0] {
			return ua[0] < ub[0]
		}
		if ua[1] != ub[1] {
			return ua[1] < ub[1]
		}
		a, b = a[na:], b[nb:]
	}
	return a == "" && b != ""
}

// units returns the UTF-16 encoding of r as two code units; the second is 0
// for a character of the basic multilingual plane.
func units(r rune) [2]rune {
	if r < 0x10000 {
		return [2]rune{r, 0}
	}
	r -= 0x10000
	return [2]rune{0xD800 + (r >> 10), 0xDC00 + (r & 0x3FF)}
}

const hexDigits = "0123456789abcdef"

// str appends s as a JSON string with the escapes of RFC 8785 §3.2.2.2:
// \b \t \n \f \r \" \\, other control characters as \u00xx, everything else
// literal.
func str(out []byte, s string) []byte {
	out = append(out, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		out = append(out, s[start:i]...)
		switch c {
		case '"', '\\':
			out = append(out, '\\', c)
		case '\b':
			out = append(out, '\\', 'b')
		case '\t':
			out = append(out, '\\', 't')
		case '\n':
			out = append(out, '\\', 'n')
		case '\f':
			out = append(out, '\\', 'f')
		case '\r':
			out = append(out, '\\', 'r')
		default:
			out = append(out, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		}
		start = i + 1
	}
	out = append(out, s[start:]...)
	return append(out, '"')
}

// number appends the JSON number text as ECMAScript Number::toString
// formats its double value (RFC 8785 §3.2.2.3).
func number(out []byte, text string) ([]byte, error) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, &NumberError{text}
	}
	return appendES6(out, f), nil
}

// appendES6 appends f in the Number::toString form: the shortest digits that
// round-trip, fixed notation for exponents in [-7, 21), and otherwise an
// exponent written as e+N or e-N.
func appendES6(out []byte, f float64) []byte {
	if f == 0 {
		return append(out, '0')
	}
	if f < 0 {
		out = append(out, '-')
		f = -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exponent := e, ""
	for i := 0; i < len(e); i++ {
		if e[i] == 'e' {
			mantissa, exponent = e[:i], e[i+1:]
			break
		}
	}
	digits := mantissa[:1]
	if len(mantissa) > 2 {
		digits += mantissa[2:]
	}
	power, _ := strconv.Atoi(exponent)
	k := len(digits)
	n := power + 1
	switch {
	case k <= n && n <= 21:
		out = append(out, digits...)
		for i := k; i < n; i++ {
			out = append(out, '0')
		}
	case 0 < n && n <= 21:
		out = append(out, digits[:n]...)
		out = append(out, '.')
		out = append(out, digits[n:]...)
	case -6 < n && n <= 0:
		out = append(out, '0', '.')
		for i := n; i < 0; i++ {
			out = append(out, '0')
		}
		out = append(out, digits...)
	default:
		out = append(out, digits[0])
		if k > 1 {
			out = append(out, '.')
			out = append(out, digits[1:]...)
		}
		out = append(out, 'e')
		if n-1 >= 0 {
			out = append(out, '+')
		} else {
			out = append(out, '-')
		}
		m := n - 1
		if m < 0 {
			m = -m
		}
		out = append(out, strconv.Itoa(m)...)
	}
	return out
}
