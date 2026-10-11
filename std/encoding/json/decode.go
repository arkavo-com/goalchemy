package json

import (
	libjson "github.com/eugenioenko/goalchemy/lib/json"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
	"github.com/eugenioenko/goalchemy/std/unicode/utf8"
)

// Unmarshal parses the JSON-encoded data and stores the result in the value
// pointed to by v, as encoding/json.Unmarshal does.
func Unmarshal(data []byte, v any) error {
	return nativeUnmarshalResult(libjson.Unmarshal(data, v))
}

func nativeUnmarshalResult(kind int, value, typ string, offset int64, structName, field, msg string, cause error) error {
	switch kind {
	case nativeOK:
		return nil
	case nativeUnmarshalType:
		return &UnmarshalTypeError{value, typ, offset, structName, field, cause}
	case nativeUnmarshalTypeSyntax:
		return &UnmarshalTypeError{value, typ, offset, structName, field, &SyntaxError{msg, 0}}
	case nativeSyntax:
		return &SyntaxError{msg, offset}
	case nativeInvalidUnmarshal:
		return &InvalidUnmarshalError{typ}
	}
	return cause
}

const (
	nativeUnmarshalType = iota + 10
	nativeUnmarshalTypeSyntax
	nativeSyntax
	nativeInvalidUnmarshal
)

// An UnmarshalTypeError describes a JSON value that was not appropriate for
// a value of a specific Go type. Type is the Go type name.
type UnmarshalTypeError struct {
	Value  string
	Type   string
	Offset int64
	Struct string
	Field  string
	Err    error
}

func (e *UnmarshalTypeError) Error() string {
	var s string
	if e.Struct != "" || e.Field != "" {
		intoWhat := "Go struct field "
		i := strings.LastIndexByte(e.Field, '.') + len(".")
		if len(e.Field[i:]) > 0 && strings.TrimRight(e.Field[i:], "0123456789") == "" {
			intoWhat = ""
		}
		s = "json: cannot unmarshal " + e.Value + " into " + intoWhat + e.Struct + "." + e.Field + " of type " + e.Type
	} else {
		s = "json: cannot unmarshal " + e.Value + " into Go value of type " + e.Type
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the underlying error.
func (e *UnmarshalTypeError) Unwrap() error { return e.Err }

// An InvalidUnmarshalError describes an invalid argument passed to
// Unmarshal. Type is the Go type name of the argument, or empty for nil.
type InvalidUnmarshalError struct {
	Type string
}

func (e *InvalidUnmarshalError) Error() string {
	if e.Type == "" {
		return "json: Unmarshal(nil)"
	}
	if !strings.HasPrefix(e.Type, "*") {
		return "json: Unmarshal(non-pointer " + e.Type + ")"
	}
	return "json: Unmarshal(nil " + e.Type + ")"
}

type textUnmarshaler interface {
	UnmarshalText(text []byte) error
}

var (
	errNotString     = errors.New("JSON value must be string type")
	errInvalidSyntax = errors.New("invalid syntax")
)

// UnmarshalJSON sets *n to a number literal, or to the number in a JSON
// string; null leaves *n unchanged.
func (n *Number) UnmarshalJSON(data []byte) error {
	switch data[0] {
	case 'n':
		return nil
	case '"':
		s := unquote(data)
		if !isValidNumber(s) {
			return &UnmarshalTypeError{Value: "string " + string(data), Type: "json.Number", Err: errInvalidSyntax}
		}
		*n = Number(s)
		return nil
	case 't', 'f':
		return &UnmarshalTypeError{Value: "bool", Type: "json.Number"}
	case '[':
		return &UnmarshalTypeError{Value: "array", Type: "json.Number"}
	case '{':
		return &UnmarshalTypeError{Value: "object", Type: "json.Number"}
	}
	*n = Number(data)
	return nil
}

type decodeState struct {
	data            []byte
	off             int
	err             error
	path            []string
	root            string
	useNumber       bool
	disallowUnknown bool
	checked         bool
}

func unmarshalTyped(data []byte, p any, t *typeInfo, isNil bool, root string) error {
	return (&decodeState{data: data, root: root}).typed(p, t, isNil)
}

func (d *decodeState) typed(p any, t *typeInfo, isNil bool) error {
	if isNil {
		return &InvalidUnmarshalError{"*" + t.name}
	}
	if !d.checked {
		if err := checkDecodeInput(d.data); err != nil {
			return err
		}
	}
	d.skipSpace()
	d.value(p, t, false)
	return d.err
}

func unmarshalAny(data []byte, v any) error {
	return (&decodeState{data: data}).dynamic(v)
}

func (d *decodeState) dynamic(v any) error {
	if v == nil {
		return &InvalidUnmarshalError{}
	}
	name := dynamicTypeName(v)
	if !strings.HasPrefix(name, "*") {
		return &InvalidUnmarshalError{name}
	}
	if isNilPointer(v) || isNilBuiltin(v) {
		return &InvalidUnmarshalError{name}
	}
	if !decodableDynamic(v) {
		return &UnsupportedTypeError{name}
	}
	if !d.checked {
		if err := checkDecodeInput(d.data); err != nil {
			return err
		}
	}
	root := name[1:]
	if strings.ContainsAny(root, "[]{}() *") {
		root = ""
	} else if i := strings.LastIndexByte(root, '.'); i >= 0 {
		root = root[i+1:]
	}
	d.root = root
	d.skipSpace()
	d.dynamicPointer(v)
	return d.err
}

func isNilBuiltin(v any) bool {
	switch x := v.(type) {
	case *any:
		return x == nil
	case *map[string]any:
		return x == nil
	case *[]any:
		return x == nil
	case *string:
		return x == nil
	case *bool:
		return x == nil
	case *float64:
		return x == nil
	}
	return false
}

func decodableDynamic(v any) bool {
	if _, ok := v.(Unmarshaler); ok {
		return true
	}
	if _, ok := v.(textUnmarshaler); ok {
		return true
	}
	switch v.(type) {
	case *any, *map[string]any, *[]any, *string, *bool, *float64:
		return true
	}
	return false
}

func (d *decodeState) save(err error) {
	if d.err == nil {
		d.err = err
	}
}

func (d *decodeState) skipSpace() {
	d.off += consumeWhitespace(d.data[d.off:])
}

func (d *decodeState) pushIndex(i int) { d.path = append(d.path, strconv.Itoa(i)) }

func (d *decodeState) pushName(name string) {
	if strings.ContainsAny(name, "~/") {
		name = strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
	}
	d.path = append(d.path, name)
}

func (d *decodeState) pop() { d.path = d.path[:len(d.path)-1] }

func (d *decodeState) typeError(value, typ string, offset int, err error) error {
	field := strings.Join(d.path, ".")
	root := ""
	if len(d.path) > 0 {
		root = d.root
	}
	return &UnmarshalTypeError{value, typ, int64(offset), root, field, err}
}

func kindName(c byte) string {
	switch c {
	case 'n':
		return "null"
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case '[':
		return "array"
	case '{':
		return "object"
	}
	return "number"
}

// skipValue returns the end of the valid JSON value starting at off.
func (d *decodeState) skipValue(off int) int {
	switch c := d.data[off]; {
	case c == '"':
		return d.stringEnd(off)
	case c == '{' || c == '[':
		depth := 0
		for i := off; i < len(d.data); i++ {
			switch d.data[i] {
			case '"':
				i = d.stringEnd(i) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(d.data)
	}
	n, _ := checkValue(d.data[off:], 1, true)
	return off + n
}

func (d *decodeState) stringEnd(off int) int {
	for i := off + 1; i < len(d.data); i++ {
		switch d.data[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(d.data)
}

// skip moves past the value at d.off and returns its start and end.
func (d *decodeState) skip() (int, int) {
	start := d.off
	d.off = d.skipValue(start)
	return start, d.off
}

func (d *decodeState) mismatch(t *typeInfo) {
	start, end := d.skip()
	d.kindError(t.name, start, end, nil)
}

// kindError reports a value of the wrong JSON kind for typ, at the offset
// encoding/json uses: after the opening delimiter of an array or object and
// after the whole value otherwise.
func (d *decodeState) kindError(typ string, start, end int, err error) {
	c := d.data[start]
	if c == '{' || c == '[' {
		d.save(d.typeError(kindName(c), typ, start+1, err))
		return
	}
	d.save(d.typeError(kindName(c), typ, end, err))
}

func (d *decodeState) value(p any, t *typeInfo, quoted bool) {
	c := d.data[d.off]
	if t.flags&flagUnmarshalJSON != 0 {
		start, end := d.skip()
		raw := append([]byte(nil), d.data[start:end]...)
		if err := p.(Unmarshaler).UnmarshalJSON(raw); err != nil {
			d.save(err)
		}
		return
	}
	if t.flags&flagUnmarshalText != 0 && c != 'n' {
		start, end := d.skip()
		if c != '"' {
			d.kindError(t.name, start, end, errNotString)
			return
		}
		if err := p.(textUnmarshaler).UnmarshalText([]byte(unquote(d.data[start:end]))); err != nil {
			d.save(err)
		}
		return
	}
	switch t.kind {
	case kindPointer:
		if c == 'n' || quoted && c == '"' && d.quotedNull() {
			d.skip()
			t.setNil(p)
			return
		}
		e := t.deref(p)
		if e == nil {
			e = t.alloc(p)
		}
		d.value(e, t.elem, quoted)
		return
	case kindInterface:
		d.interfaceValue(p, t)
		return
	}
	if quoted {
		d.quotedValue(p, t)
		return
	}
	switch c {
	case 'n':
		d.skip()
		switch t.kind {
		case kindSlice, kindMap, kindBytes:
			t.setNil(p)
		}
	case '"':
		start, end := d.skip()
		switch t.kind {
		case kindString:
			t.store(p, unquote(d.data[start:end]))
		case kindBytes:
			b, err := decodeBase64(unquote(d.data[start:end]))
			if err != nil {
				d.save(d.typeError("string", t.name, end, err))
				return
			}
			t.store(p, b)
		default:
			d.save(d.typeError("string", t.name, end, nil))
		}
	case 't', 'f':
		_, end := d.skip()
		if t.kind == kindBool {
			t.store(p, c == 't')
			return
		}
		d.save(d.typeError("bool", t.name, end, nil))
	case '[':
		switch t.kind {
		case kindSlice, kindBytes, kindArray:
			d.array(p, t)
		default:
			d.mismatch(t)
		}
	case '{':
		switch t.kind {
		case kindStruct:
			d.object(p, t)
		case kindMap:
			d.mapValue(p, t)
		default:
			d.mismatch(t)
		}
	default:
		start, end := d.skip()
		d.number(p, t, string(d.data[start:end]), end, "")
	}
}

// number stores the number literal s into *p. orig is the JSON text of a
// quoted number, reported in errors instead of s.
func (d *decodeState) number(p any, t *typeInfo, s string, end int, kind string) {
	if kind == "" {
		kind = "number"
	}
	switch t.kind {
	case kindInt:
		n, err := strconv.ParseInt(s, 10, t.bits)
		if err != nil {
			d.save(d.typeError("number "+s, t.name, end, nil))
			return
		}
		t.store(p, n)
	case kindUint:
		n, err := strconv.ParseUint(s, 10, t.bits)
		if err != nil {
			d.save(d.typeError("number "+s, t.name, end, nil))
			return
		}
		t.store(p, n)
	case kindFloat32, kindFloat64:
		f, err := strconv.ParseFloat(s, t.bits)
		t.store(p, f)
		if err != nil {
			d.save(d.typeError("number "+s, t.name, end, nil))
		}
	default:
		d.save(d.typeError(kind, t.name, end, nil))
	}
}

func (d *decodeState) quotedNull() bool {
	end := d.skipValue(d.off)
	return unquote(d.data[d.off:end]) == "null"
}

// quotedValue decodes a field with the string option: a JSON string holding
// the field's JSON encoding, or null.
func (d *decodeState) quotedValue(p any, t *typeInfo) {
	start, end := d.skip()
	c := d.data[start]
	if c == 'n' {
		return
	}
	if c != '"' {
		d.kindError(t.name, start, end, nil)
		return
	}
	raw := string(d.data[start:end])
	s := unquote(d.data[start:end])
	if s == "null" {
		return
	}
	switch t.kind {
	case kindString:
		if err := quotedStringError([]byte(s)); err != nil {
			d.save(d.typeError("string", t.name, end, err))
			return
		}
		t.store(p, unquote([]byte(s)))
	case kindBool:
		switch s {
		case "true":
			t.store(p, true)
		case "false":
			t.store(p, false)
		default:
			d.save(d.typeError("string "+raw, t.name, end, errInvalidSyntax))
		}
	case kindInt:
		n, err := strconv.ParseInt(s, 10, t.bits)
		if err != nil {
			d.save(d.typeError("number "+s, t.name, end, nil))
			return
		}
		t.store(p, n)
	case kindUint:
		n, err := strconv.ParseUint(s, 10, t.bits)
		if err != nil {
			d.save(d.typeError("number "+s, t.name, end, nil))
			return
		}
		t.store(p, n)
	default:
		f, err := strconv.ParseFloat(s, t.bits)
		if err != nil {
			d.save(d.typeError("number "+s, t.name, end, nil))
			return
		}
		t.store(p, f)
	}
}

var errInvalidUTF8 = errors.New("invalid UTF-8")

// quotedStringError reports why src is not exactly one JSON string, as
// encoding/json does for string fields with the string option.
func quotedStringError(src []byte) error {
	if len(src) == 0 {
		return eofFault.atStart()
	}
	if src[0] != '"' {
		return invalidCharacter(src, `at start of string (expecting '"')`).atStart()
	}
	var err error
	n := 1
	for n < len(src) {
		for n < len(src) && src[n] < utf8.RuneSelf && ' ' <= src[n] && src[n] != '\\' && src[n] != '"' {
			n++
		}
		if n >= len(src) {
			break
		}
		if src[n] == '"' {
			n++
			if n < len(src) {
				err = invalidCharacter(src[n:], "after string value").atStart()
			}
			return err
		}
		r, rn := utf8.DecodeRune(src[n:])
		switch {
		case rn > 1:
			n += rn
		case r == '\\':
			if len(src) < n+2 {
				return eofFault.atStart()
			}
			switch src[n+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				n += 2
			case 'u':
				if len(src) < n+6 {
					if hasEscapedUTF16Prefix(src[n:]) {
						return eofFault.atStart()
					}
					return invalidEscape(src[n:]).atStart()
				}
				if !isHex4(src[n+2 : n+6]) {
					return invalidEscape(src[n : n+6]).atStart()
				}
				v1 := hex4(src[n+2 : n+6])
				n += 6
				if 0xD800 <= v1 && v1 < 0xE000 {
					switch {
					case len(src) < n+6:
						if hasLowSurrogatePrefix(src[n:]) {
							return eofFault.atStart()
						}
						err = invalidEscape(src[n-6:]).atStart()
					case src[n] != '\\' || src[n+1] != 'u' || !isHex4(src[n+2:n+6]):
						err = invalidEscape(src[n-6 : n+6]).atStart()
					case v1 >= 0xDC00 || hex4(src[n+2:n+6]) < 0xDC00 || hex4(src[n+2:n+6]) >= 0xE000:
						err = invalidEscape(src[n-6 : n+6]).atStart()
					default:
						n += 6
					}
				}
			default:
				return invalidEscape(src[n : n+2]).atStart()
			}
		case r == utf8.RuneError:
			if !utf8.FullRune(src[n:]) {
				return eofFault.atStart()
			}
			n += rn
			err = errInvalidUTF8
		default:
			return invalidCharacter(src[n:], "in string (expecting non-control character)").atStart()
		}
	}
	return eofFault.atStart()
}

func hasLowSurrogatePrefix(b []byte) bool {
	for i, c := range b {
		switch {
		case i == 0 && c != '\\':
			return false
		case i == 1 && c != 'u':
			return false
		case i == 2 && c != 'd' && c != 'D':
			return false
		case i == 3 && !('c' <= c && c <= 'f') && !('C' <= c && c <= 'F'):
			return false
		case i >= 2 && i < 6 && !isHexDigit(c):
			return false
		}
	}
	return true
}

func (d *decodeState) array(p any, t *typeInfo) {
	d.off++
	d.skipSpace()
	n := 0
	fixed := t.kind == kindArray
	if !fixed {
		t.resize(p, -1)
	}
	for d.data[d.off] != ']' {
		if fixed {
			if n < t.n {
				ep := t.index(p, n)
				d.pushIndex(n)
				d.value(ep, t.elem, false)
				d.pop()
				t.setIndex(p, n, ep)
			} else {
				d.skip()
			}
		} else {
			t.resize(p, n+1)
			ep := t.index(p, n)
			d.pushIndex(n)
			d.value(ep, t.elem, false)
			d.pop()
			t.setIndex(p, n, ep)
		}
		n++
		d.skipSpace()
		if d.data[d.off] == ',' {
			d.off++
			d.skipSpace()
		}
	}
	d.off++
	switch {
	case fixed:
		for i := n; i < t.n; i++ {
			t.setIndex(p, i, t.newElem())
		}
	case n == 0:
		t.resize(p, 0)
	default:
		t.resize(p, n)
	}
}

func (d *decodeState) readName() (string, int) {
	start := d.off
	d.off = d.stringEnd(start)
	end := d.off
	d.skipSpace()
	d.off++
	d.skipSpace()
	return unquote(d.data[start:end]), end
}

func (d *decodeState) object(p any, t *typeInfo) {
	d.off++
	d.skipSpace()
	for d.data[d.off] != '}' {
		name, _ := d.readName()
		fi := -1
		for i := range t.fields {
			if t.fields[i].name == name {
				fi = i
			}
		}
		if fi < 0 {
			for i := range t.fields {
				if strings.EqualFold(t.fields[i].name, name) {
					fi = i
					break
				}
			}
		}
		if fi < 0 {
			d.skip()
			if d.disallowUnknown {
				d.save(errors.New("json: unknown field " + strconv.Quote(name)))
			}
		} else {
			f := t.fields[fi]
			fp := f.addr(p)
			d.pushName(name)
			d.value(fp, f.t, f.quoted)
			d.pop()
		}
		d.skipSpace()
		if d.data[d.off] == ',' {
			d.off++
			d.skipSpace()
		}
	}
	d.off++
}

func (d *decodeState) mapValue(p any, t *typeInfo) {
	d.off++
	d.skipSpace()
	t.mapInit(p)
	for d.data[d.off] != '}' {
		name, nameEnd := d.readName()
		kp := t.newKey()
		ok := true
		k := t.key
		switch {
		case k.flags&flagUnmarshalText != 0:
			if err := kp.(textUnmarshaler).UnmarshalText([]byte(name)); err != nil {
				d.save(err)
				ok = false
			}
		case k.kind == kindString:
			k.store(kp, name)
		case k.kind == kindInt:
			n, err := strconv.ParseInt(name, 10, k.bits)
			if err != nil {
				d.pushName(name)
				d.save(d.typeError("number "+name, k.name, nameEnd, nil))
				d.pop()
				ok = false
			} else {
				k.store(kp, n)
			}
		default:
			n, err := strconv.ParseUint(name, 10, k.bits)
			if err != nil {
				d.pushName(name)
				d.save(d.typeError("number "+name, k.name, nameEnd, nil))
				d.pop()
				ok = false
			} else {
				k.store(kp, n)
			}
		}
		if !ok {
			d.skip()
		} else {
			vp := t.newElem()
			d.pushName(name)
			d.value(vp, t.elem, false)
			d.pop()
			t.mapStore(p, kp, vp)
		}
		d.skipSpace()
		if d.data[d.off] == ',' {
			d.off++
			d.skipSpace()
		}
	}
	d.off++
}

func (d *decodeState) interfaceValue(p any, t *typeInfo) {
	c := d.data[d.off]
	if c == 'n' {
		d.skip()
		t.setNil(p)
		return
	}
	if cur := t.value(p); cur != nil && isLivePointer(cur) {
		if !decodableDynamic(cur) {
			d.skip()
			d.save(&UnsupportedTypeError{dynamicTypeName(cur)})
			return
		}
		d.dynamicPointer(cur)
		return
	}
	if t.flags&flagNonEmptyInterface != 0 {
		start, _ := d.skip()
		d.save(d.typeError(kindName(c), t.name, start, nil))
		return
	}
	t.setIface(p, d.anyValue())
}

func isLivePointer(v any) bool {
	return strings.HasPrefix(dynamicTypeName(v), "*") && !isNilPointer(v) && !isNilBuiltin(v)
}

// dynamicPointer decodes into the value v points to, which is one of the
// types decodableDynamic accepts.
func (d *decodeState) dynamicPointer(v any) {
	if u, ok := v.(Unmarshaler); ok {
		start, end := d.skip()
		if err := u.UnmarshalJSON(append([]byte(nil), d.data[start:end]...)); err != nil {
			d.save(err)
		}
		return
	}
	c := d.data[d.off]
	if u, ok := v.(textUnmarshaler); ok {
		if c == 'n' {
			d.skip()
			return
		}
		start, end := d.skip()
		if c != '"' {
			d.kindError(strings.TrimPrefix(dynamicTypeName(v), "*"), start, end, errNotString)
			return
		}
		if err := u.UnmarshalText([]byte(unquote(d.data[start:end]))); err != nil {
			d.save(err)
		}
		return
	}
	switch x := v.(type) {
	case *any:
		if cur := *x; cur != nil && c != 'n' && isLivePointer(cur) {
			if !decodableDynamic(cur) {
				d.skip()
				d.save(&UnsupportedTypeError{dynamicTypeName(cur)})
				return
			}
			d.dynamicPointer(cur)
			return
		}
		*x = d.anyValue()
	case *map[string]any:
		if c == 'n' {
			d.skip()
			*x = nil
			return
		}
		if c != '{' {
			d.mismatchName("map[string]interface {}")
			return
		}
		if *x == nil {
			*x = map[string]any{}
		}
		d.off++
		d.skipSpace()
		for d.data[d.off] != '}' {
			name, _ := d.readName()
			d.pushName(name)
			(*x)[name] = d.anyValue()
			d.pop()
			d.skipSpace()
			if d.data[d.off] == ',' {
				d.off++
				d.skipSpace()
			}
		}
		d.off++
	case *[]any:
		if c == 'n' {
			d.skip()
			*x = nil
			return
		}
		if c != '[' {
			d.mismatchName("[]interface {}")
			return
		}
		a, _ := d.anyValue().([]any)
		*x = a
	case *string:
		if c == '"' {
			start, end := d.skip()
			*x = unquote(d.data[start:end])
		} else if c != 'n' {
			d.mismatchName("string")
		} else {
			d.skip()
		}
	case *bool:
		if c == 't' || c == 'f' {
			d.skip()
			*x = c == 't'
		} else if c != 'n' {
			d.mismatchName("bool")
		} else {
			d.skip()
		}
	case *float64:
		if c == '-' || '0' <= c && c <= '9' {
			start, end := d.skip()
			s := string(d.data[start:end])
			f, err := strconv.ParseFloat(s, 64)
			*x = f
			if err != nil {
				d.save(d.typeError("number "+s, "float64", end, nil))
			}
		} else if c != 'n' {
			d.mismatchName("float64")
		} else {
			d.skip()
		}
	}
}

func (d *decodeState) mismatchName(name string) {
	d.mismatch(&typeInfo{name: name})
}

// anyValue decodes the value at d.off as encoding/json does into an empty
// interface: map[string]any, []any, float64, string, bool or nil.
func (d *decodeState) anyValue() any {
	switch c := d.data[d.off]; c {
	case 'n':
		d.skip()
		return nil
	case 't', 'f':
		d.skip()
		return c == 't'
	case '"':
		start, end := d.skip()
		return unquote(d.data[start:end])
	case '[':
		d.off++
		d.skipSpace()
		a := []any{}
		for i := 0; d.data[d.off] != ']'; i++ {
			d.pushIndex(i)
			a = append(a, d.anyValue())
			d.pop()
			d.skipSpace()
			if d.data[d.off] == ',' {
				d.off++
				d.skipSpace()
			}
		}
		d.off++
		return a
	case '{':
		d.off++
		d.skipSpace()
		m := map[string]any{}
		for d.data[d.off] != '}' {
			name, _ := d.readName()
			d.pushName(name)
			m[name] = d.anyValue()
			d.pop()
			d.skipSpace()
			if d.data[d.off] == ',' {
				d.off++
				d.skipSpace()
			}
		}
		d.off++
		return m
	}
	start, end := d.skip()
	s := string(d.data[start:end])
	if d.useNumber {
		return Number(s)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		d.save(d.typeError("number "+s, "float64", end, nil))
	}
	return f
}

// unquote decodes a valid JSON string literal, replacing invalid UTF-8 and
// unpaired surrogate escapes with U+FFFD.
func unquote(b []byte) string {
	b = b[1 : len(b)-1]
	simple := true
	for _, c := range b {
		if c == '\\' || c >= utf8.RuneSelf {
			simple = false
			break
		}
	}
	if simple {
		return string(b)
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c == '\\' {
			switch e := b[i+1]; e {
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r := rune(hex4(b[i+2 : i+6]))
				i += 6
				if 0xD800 <= r && r < 0xE000 {
					r2 := rune(-1)
					if i+6 <= len(b) && b[i] == '\\' && b[i+1] == 'u' {
						r2 = rune(hex4(b[i+2 : i+6]))
					}
					if r < 0xDC00 && 0xDC00 <= r2 && r2 < 0xE000 {
						r = (r-0xD800)<<10 | (r2 - 0xDC00) + 0x10000
						i += 6
					} else {
						r = utf8.RuneError
					}
				}
				out = utf8.AppendRune(out, r)
				continue
			default:
				out = append(out, e)
			}
			i += 2
			continue
		}
		if c < utf8.RuneSelf {
			out = append(out, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			out = append(out, "�"...)
		} else {
			out = append(out, b[i:i+size]...)
		}
		i += size
	}
	return string(out)
}

func hex4(b []byte) int {
	n := 0
	for _, c := range b {
		n <<= 4
		switch {
		case '0' <= c && c <= '9':
			n |= int(c - '0')
		case 'a' <= c && c <= 'f':
			n |= int(c-'a') + 10
		default:
			n |= int(c-'A') + 10
		}
	}
	return n
}

// A CorruptInputError reports illegal base64 data at an input offset.
type corruptInputError int64

func (e corruptInputError) Error() string {
	return "illegal base64 data at input byte " + strconv.FormatInt(int64(e), 10)
}

func base64Value(c byte) int {
	switch {
	case 'A' <= c && c <= 'Z':
		return int(c - 'A')
	case 'a' <= c && c <= 'z':
		return int(c-'a') + 26
	case '0' <= c && c <= '9':
		return int(c-'0') + 52
	case c == '+':
		return 62
	case c == '/':
		return 63
	}
	return -1
}

// decodeBase64 decodes standard padded base64 as encoding/base64's
// StdEncoding does, ignoring CR and LF.
func decodeBase64(s string) ([]byte, error) {
	out := []byte{}
	si := 0
	for si < len(s) {
		var dbuf [4]int
		dlen := 4
		for j := 0; j < 4; j++ {
			for si < len(s) && (s[si] == '\n' || s[si] == '\r') {
				si++
			}
			if si == len(s) {
				if j == 0 {
					return out, nil
				}
				if j == 1 {
					return out, corruptInputError(si - j)
				}
				return out, corruptInputError(si - j)
			}
			in := s[si]
			si++
			if in == '=' {
				switch j {
				case 0, 1:
					return out, corruptInputError(si - 1)
				case 2:
					for si < len(s) && (s[si] == '\n' || s[si] == '\r') {
						si++
					}
					if si == len(s) {
						return out, corruptInputError(len(s))
					}
					if s[si] != '=' {
						return out, corruptInputError(si - 1)
					}
					si++
				}
				for si < len(s) && (s[si] == '\n' || s[si] == '\r') {
					si++
				}
				if si < len(s) {
					return out, corruptInputError(si)
				}
				dlen = j
				break
			}
			v := base64Value(in)
			if v < 0 {
				return out, corruptInputError(si - 1)
			}
			dbuf[j] = v
		}
		val := uint(dbuf[0])<<18 | uint(dbuf[1])<<12 | uint(dbuf[2])<<6 | uint(dbuf[3])
		switch dlen {
		case 4:
			out = append(out, byte(val>>16), byte(val>>8), byte(val))
		case 3:
			out = append(out, byte(val>>16), byte(val>>8))
		case 2:
			out = append(out, byte(val>>16))
		}
		if dlen < 4 {
			return out, nil
		}
	}
	return out, nil
}
