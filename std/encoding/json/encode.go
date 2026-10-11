// Package json encodes and decodes JSON with the semantics of Go's
// encoding/json. Without reflection, the compiler describes each statically
// known type passed to Marshal or pointed to by an Unmarshal target. Values
// held in interfaces are encoded when they are JSON-shaped (nil, booleans,
// numbers and strings of predeclared types, []byte, []any, map[string]any)
// or implement Marshaler or encoding.TextMarshaler, as Number, RawMessage and
// jsonvalue.Value do; other dynamic values return *UnsupportedTypeError.
package json

import (
	libjson "github.com/eugenioenko/goalchemy/lib/json"
	"github.com/eugenioenko/goalchemy/std/sort"
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
	"github.com/eugenioenko/goalchemy/std/unicode/utf8"
)

// Marshal returns the JSON encoding of v, as encoding/json.Marshal does.
func Marshal(v any) ([]byte, error) { return nativeResult(libjson.Marshal(v)) }

// MarshalIndent is like Marshal but applies Indent to format the output.
// Each JSON element in the output will begin on a new line beginning with
// prefix followed by one or more copies of indent according to the
// indentation nesting.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return nativeResult(libjson.MarshalIndent(v, prefix, indent))
}

const (
	nativeOK = iota
	nativeUnsupportedType
	nativeUnsupportedValue
	nativeMarshalerFailed
	nativeMarshalerSyntax
)

const nativeError = -1

func nativeResult(b []byte, kind int, name, src, msg string, offset int64, cause error) ([]byte, error) {
	switch kind {
	case nativeOK:
		return b, nil
	case nativeUnsupportedType:
		return nil, &UnsupportedTypeError{name}
	case nativeUnsupportedValue:
		return nil, &UnsupportedValueError{src}
	case nativeMarshalerFailed:
		return nil, &MarshalerError{name, cause, src}
	case nativeMarshalerSyntax:
		return nil, &MarshalerError{name, &SyntaxError{msg, offset}, src}
	case nativeError:
		return nil, cause
	}
	return nil, &encodingError{msg}
}

// Marshaler is the interface implemented by types that can marshal
// themselves into valid JSON.
type Marshaler interface {
	MarshalJSON() ([]byte, error)
}

// Unmarshaler is the interface implemented by types that can unmarshal a
// JSON description of themselves.
type Unmarshaler interface {
	UnmarshalJSON([]byte) error
}

type textMarshaler interface {
	MarshalText() (text []byte, err error)
}

// RawMessage is a raw encoded JSON value.
type RawMessage []byte

// MarshalJSON returns m as the JSON encoding of m.
func (m RawMessage) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}

// UnmarshalJSON sets *m to a copy of data.
func (m *RawMessage) UnmarshalJSON(data []byte) error {
	if m == nil {
		return &rawMessageError{}
	}
	*m = append((*m)[0:0], data...)
	return nil
}

type rawMessageError struct{}

func (*rawMessageError) Error() string { return "json.RawMessage: UnmarshalJSON on nil pointer" }

// A Number represents a JSON number literal.
type Number string

// String returns the literal text of the number.
func (n Number) String() string { return string(n) }

// Float64 returns the number as a float64.
func (n Number) Float64() (float64, error) { return strconv.ParseFloat(string(n), 64) }

// Int64 returns the number as an int64.
func (n Number) Int64() (int64, error) { return strconv.ParseInt(string(n), 10, 64) }

// MarshalJSON returns the number literal, or 0 when n is empty.
func (n Number) MarshalJSON() ([]byte, error) {
	s := string(n)
	if s == "" {
		s = "0"
	}
	if !isValidNumber(s) {
		return nil, &encodingError{"json: invalid number literal " + strconv.Quote(s)}
	}
	return []byte(s), nil
}

// An UnsupportedTypeError is returned by Marshal when attempting to encode
// an unsupported value type. Type is the Go type name.
type UnsupportedTypeError struct {
	Type string
}

func (e *UnsupportedTypeError) Error() string {
	return "json: unsupported type: " + e.Type
}

// An UnsupportedValueError is returned by Marshal when attempting to encode
// an unsupported value.
type UnsupportedValueError struct {
	Str string
}

func (e *UnsupportedValueError) Error() string {
	return "json: unsupported value: " + e.Str
}

// A MarshalerError represents an error from calling a MarshalJSON or
// MarshalText method. Type is the Go type name.
type MarshalerError struct {
	Type       string
	Err        error
	sourceFunc string
}

func (e *MarshalerError) Error() string {
	srcFunc := e.sourceFunc
	if srcFunc == "" {
		srcFunc = "MarshalJSON"
	}
	return "json: error calling " + srcFunc + " for type " + e.Type + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MarshalerError) Unwrap() error { return e.Err }

type encodingError struct{ msg string }

func (e *encodingError) Error() string { return e.msg }

const hex = "0123456789abcdef"

const (
	kindBool = iota + 1
	kindInt
	kindUint
	kindFloat32
	kindFloat64
	kindString
	kindBytes
	kindStruct
	kindSlice
	kindArray
	kindMap
	kindPointer
	kindInterface
)

const (
	flagMarshalJSON = 1 << iota
	flagAddrMarshalJSON
	flagMarshalText
	flagAddrMarshalText
	flagUnmarshalJSON
	flagUnmarshalText
	flagNonEmptyInterface
)

type typeInfo struct {
	name     string
	kind     int
	flags    int
	n        int
	bits     int
	elem     *typeInfo
	key      *typeInfo
	fields   []field
	load     func(any) any
	value    func(any) any
	deref    func(any) any
	length   func(any) int
	index    func(any, int) any
	entries  func(any) ([]any, []any, bool)
	store    func(any, any)
	setNil   func(any)
	alloc    func(any) any
	resize   func(any, int)
	setIndex func(any, int, any)
	newElem  func() any
	newKey   func() any
	mapInit  func(any)
	mapStore func(any, any, any)
	setIface func(any, any)
}

type field struct {
	name      string
	key       string
	omitEmpty bool
	quoted    bool
	viaPtr    bool
	rawKey    string
	t         *typeInfo
	addr      func(any) any
}

func newType(name string, kind, flags, n, bits int) *typeInfo {
	return &typeInfo{name: name, kind: kind, flags: flags, n: n, bits: bits}
}

func describeDecode(t *typeInfo, store func(any, any), setNil func(any), alloc func(any) any, resize func(any, int),
	setIndex func(any, int, any), newElem, newKey func() any, mapInit func(any), mapStore func(any, any, any), setIface func(any, any)) {
	t.store, t.setNil, t.alloc, t.resize, t.setIndex = store, setNil, alloc, resize, setIndex
	t.newElem, t.newKey, t.mapInit, t.mapStore, t.setIface = newElem, newKey, mapInit, mapStore, setIface
}

func describe(t, elem, key *typeInfo, load, value, deref func(any) any, length func(any) int,
	index func(any, int) any, entries func(any) ([]any, []any, bool)) {
	t.elem, t.key = elem, key
	t.load, t.value, t.deref = load, value, deref
	t.length, t.index, t.entries = length, index, entries
}

func addField(t *typeInfo, name, key string, omitEmpty, quoted, viaPtr bool, ft *typeInfo, addr func(any) any) {
	t.fields = append(t.fields, field{name: name, key: key, rawKey: `"` + name + `":`, omitEmpty: omitEmpty, quoted: quoted, viaPtr: viaPtr, t: ft, addr: addr})
}

const (
	startDetectingCyclesAfter = 1000
	maxEncodeDepth            = 10000
)

type encodeState struct {
	buf      []byte
	ptrLevel int
	ptrSeen  []any
	depth    int
	rawHTML  bool
}

func marshalTyped(p any, t *typeInfo) ([]byte, error) {
	e := &encodeState{}
	if err := e.typed(p, t, false, false); err != nil {
		return nil, err
	}
	return e.buf, nil
}

func marshalIndentTyped(p any, t *typeInfo, prefix, indent string) ([]byte, error) {
	b, err := marshalTyped(p, t)
	if err != nil {
		return nil, err
	}
	return indentOutput(b, prefix, indent)
}

func marshalAny(v any) ([]byte, error) {
	e := &encodeState{}
	if err := e.dynamic(v, false); err != nil {
		return nil, err
	}
	return e.buf, nil
}

func marshalIndentAny(v any, prefix, indent string) ([]byte, error) {
	b, err := marshalAny(v)
	if err != nil {
		return nil, err
	}
	return indentOutput(b, prefix, indent)
}

func indentOutput(b []byte, prefix, indent string) ([]byte, error) {
	b2, err := appendIndent(make([]byte, 0, 2*len(b)), b, prefix, indent)
	if err != nil {
		return nil, err
	}
	return b2, nil
}

func (e *encodeState) typed(p any, t *typeInfo, addr, quoted bool) error {
	switch {
	case t.kind != kindPointer && addr && t.flags&flagAddrMarshalJSON != 0:
		return e.marshalJSON(p, t.methodName())
	case t.flags&flagMarshalJSON != 0:
		v := t.receiver(p)
		if v == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		return e.marshalJSON(v, t.methodName())
	case t.kind != kindPointer && addr && t.flags&flagAddrMarshalText != 0:
		return e.marshalText(p, t.methodName())
	case t.flags&flagMarshalText != 0:
		v := t.receiver(p)
		if v == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		return e.marshalText(v, t.methodName())
	}
	switch t.kind {
	case kindBool, kindInt, kindUint, kindFloat32, kindFloat64, kindString:
		return e.basic(t.load(p), t.kind, quoted)
	case kindBytes:
		return e.bytes(t.load(p).([]byte))
	case kindStruct:
		return e.structValue(p, t, addr)
	case kindSlice:
		n := t.length(p)
		if n < 0 {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		if err := e.enter(nil, t.name); err != nil {
			return err
		}
		if err := e.array(p, t, n, true); err != nil {
			return err
		}
		e.ptrLevel--
		return nil
	case kindArray:
		return e.array(p, t, t.n, addr)
	case kindMap:
		return e.mapValue(p, t)
	case kindPointer:
		v := t.deref(p)
		if v == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		if err := e.enter(v, t.name); err != nil {
			return err
		}
		if err := e.typed(v, t.elem, true, quoted); err != nil {
			return err
		}
		e.leave(v)
		return nil
	case kindInterface:
		return e.dynamic(t.value(p), false)
	}
	return &UnsupportedTypeError{t.name}
}

func (t *typeInfo) methodName() string {
	if t.kind == kindInterface {
		return ""
	}
	return pointerName(t.name)
}

func pointerName(name string) string {
	if strings.HasPrefix(name, "*") {
		return name
	}
	return "*" + name
}

func (t *typeInfo) receiver(p any) any {
	if t.kind == kindPointer {
		return t.deref(p)
	}
	return t.value(p)
}

func (e *encodeState) enter(ptr any, name string) error {
	e.ptrLevel++
	if e.ptrLevel <= startDetectingCyclesAfter {
		return nil
	}
	if ptr == nil {
		if e.ptrLevel > maxEncodeDepth {
			return &UnsupportedValueError{"encountered a cycle via " + name}
		}
		return nil
	}
	for _, s := range e.ptrSeen {
		if s == ptr {
			return &UnsupportedValueError{"encountered a cycle via " + name}
		}
	}
	e.ptrSeen = append(e.ptrSeen, ptr)
	return nil
}

func (e *encodeState) leave(ptr any) {
	if e.ptrLevel > startDetectingCyclesAfter && len(e.ptrSeen) > 0 && e.ptrSeen[len(e.ptrSeen)-1] == ptr {
		e.ptrSeen = e.ptrSeen[:len(e.ptrSeen)-1]
	}
	e.ptrLevel--
}

func (e *encodeState) marshalJSON(v any, name string) error {
	m, ok := v.(Marshaler)
	if !ok {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	b, err := m.MarshalJSON()
	if err == nil {
		var out []byte
		out, err = compactValue(e.buf, b, !e.rawHTML, e.valueOffset(b), e.depth+1)
		if err == nil {
			e.buf = out
		}
	}
	if err != nil {
		if name == "" {
			name = pointerName(dynamicTypeName(v))
		}
		return &MarshalerError{name, err, "MarshalJSON"}
	}
	return nil
}

// valueOffset is the output offset encoding/json reports syntax errors in
// marshaler output against: it counts a preceding comma only when the output
// does not start with a closing delimiter.
func (e *encodeState) valueOffset(b []byte) int {
	n := len(e.buf)
	if n > 0 && e.buf[n-1] == ',' {
		if i := consumeWhitespace(b); i < len(b) && (b[i] == '}' || b[i] == ']') {
			return n - 1
		}
	}
	return n
}

func (e *encodeState) marshalText(v any, name string) error {
	m, ok := v.(textMarshaler)
	if !ok {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	b, err := m.MarshalText()
	if err != nil {
		if name == "" {
			name = pointerName(dynamicTypeName(v))
		}
		return &MarshalerError{name, err, "MarshalText"}
	}
	e.buf = appendString(e.buf, string(b), !e.rawHTML)
	return nil
}

func (e *encodeState) basic(v any, kind int, quoted bool) error {
	switch kind {
	case kindBool:
		e.quote(quoted)
		if v.(bool) {
			e.buf = append(e.buf, "true"...)
		} else {
			e.buf = append(e.buf, "false"...)
		}
		e.quote(quoted)
	case kindInt:
		e.quote(quoted)
		e.buf = strconv.AppendInt(e.buf, v.(int64), 10)
		e.quote(quoted)
	case kindUint:
		e.quote(quoted)
		e.buf = strconv.AppendUint(e.buf, v.(uint64), 10)
		e.quote(quoted)
	case kindFloat32:
		return e.float(v.(float64), 32, quoted)
	case kindFloat64:
		return e.float(v.(float64), 64, quoted)
	case kindString:
		s := v.(string)
		if quoted {
			e.buf = appendString(e.buf, string(appendString(nil, s, !e.rawHTML)), false)
		} else {
			e.buf = appendString(e.buf, s, !e.rawHTML)
		}
	}
	return nil
}

func (e *encodeState) quote(quoted bool) {
	if quoted {
		e.buf = append(e.buf, '"')
	}
}

func (e *encodeState) float(f float64, bits int, quoted bool) error {
	if f != f || f > maxFloat64 || f < -maxFloat64 {
		return &UnsupportedValueError{strconv.FormatFloat(f, 'g', -1, bits)}
	}
	e.quote(quoted)
	abs := f
	if abs < 0 {
		abs = -abs
	}
	format := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) || bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	b := strconv.AppendFloat(e.buf, f, format, -1, bits)
	if format == 'e' {
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	e.buf = b
	e.quote(quoted)
	return nil
}

const maxFloat64 = 1.79769313486231570814527423731704356798070e+308

func (e *encodeState) bytes(s []byte) error {
	if s == nil {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	e.buf = append(e.buf, '"')
	e.buf = appendBase64(e.buf, s)
	e.buf = append(e.buf, '"')
	return nil
}

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func appendBase64(dst, src []byte) []byte {
	i := 0
	for ; i+3 <= len(src); i += 3 {
		v := uint(src[i])<<16 | uint(src[i+1])<<8 | uint(src[i+2])
		dst = append(dst, base64Alphabet[v>>18&0x3F], base64Alphabet[v>>12&0x3F], base64Alphabet[v>>6&0x3F], base64Alphabet[v&0x3F])
	}
	switch len(src) - i {
	case 1:
		v := uint(src[i]) << 16
		dst = append(dst, base64Alphabet[v>>18&0x3F], base64Alphabet[v>>12&0x3F], '=', '=')
	case 2:
		v := uint(src[i])<<16 | uint(src[i+1])<<8
		dst = append(dst, base64Alphabet[v>>18&0x3F], base64Alphabet[v>>12&0x3F], base64Alphabet[v>>6&0x3F], '=')
	}
	return dst
}

func (e *encodeState) structValue(p any, t *typeInfo, addr bool) error {
	e.depth++
	next := byte('{')
	for i := range t.fields {
		f := t.fields[i]
		fp := f.addr(p)
		if fp == nil {
			continue
		}
		if f.omitEmpty && f.t.isEmpty(fp) {
			continue
		}
		e.buf = append(e.buf, next)
		next = ','
		if e.rawHTML {
			e.buf = append(e.buf, f.rawKey...)
		} else {
			e.buf = append(e.buf, f.key...)
		}
		if err := e.typed(fp, f.t, addr || f.viaPtr, f.quoted); err != nil {
			return err
		}
	}
	if next == '{' {
		e.buf = append(e.buf, "{}"...)
	} else {
		e.buf = append(e.buf, '}')
	}
	e.depth--
	return nil
}

func (t *typeInfo) isEmpty(p any) bool {
	switch t.kind {
	case kindBool:
		return !t.load(p).(bool)
	case kindInt:
		return t.load(p).(int64) == 0
	case kindUint:
		return t.load(p).(uint64) == 0
	case kindFloat32, kindFloat64:
		return t.load(p).(float64) == 0
	case kindString:
		return t.load(p).(string) == ""
	case kindBytes:
		return len(t.load(p).([]byte)) == 0
	case kindSlice:
		return t.length(p) <= 0
	case kindArray:
		return t.n == 0
	case kindMap:
		return t.length(p) <= 0
	case kindPointer:
		return t.deref(p) == nil
	case kindInterface:
		return t.value(p) == nil
	}
	return false
}

func (e *encodeState) array(p any, t *typeInfo, n int, addr bool) error {
	e.depth++
	e.buf = append(e.buf, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		if err := e.typed(t.index(p, i), t.elem, addr, false); err != nil {
			return err
		}
	}
	e.buf = append(e.buf, ']')
	e.depth--
	return nil
}

type keyed struct {
	keys []string
	vals []any
}

func (k *keyed) Len() int           { return len(k.keys) }
func (k *keyed) Less(i, j int) bool { return k.keys[i] < k.keys[j] }
func (k *keyed) Swap(i, j int) {
	k.keys[i], k.keys[j] = k.keys[j], k.keys[i]
	k.vals[i], k.vals[j] = k.vals[j], k.vals[i]
}

func (e *encodeState) mapValue(p any, t *typeInfo) error {
	keys, vals, isNil := t.entries(p)
	if isNil {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	if err := e.enter(nil, t.name); err != nil {
		return err
	}
	sv := &keyed{keys: make([]string, len(keys)), vals: vals}
	for i, k := range keys {
		s, err := t.key.keyName(k)
		if err != nil {
			return &MarshalerError{pointerName(t.key.name), err, "MarshalText"}
		}
		sv.keys[i] = s
	}
	sort.Sort(sv)
	e.depth++
	e.buf = append(e.buf, '{')
	for i, s := range sv.keys {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		e.buf = appendString(e.buf, s, !e.rawHTML)
		e.buf = append(e.buf, ':')
		if err := e.typed(sv.vals[i], t.elem, false, false); err != nil {
			return err
		}
	}
	e.buf = append(e.buf, '}')
	e.depth--
	e.ptrLevel--
	return nil
}

func (t *typeInfo) keyName(p any) (string, error) {
	if t.kind == kindString {
		return t.load(p).(string), nil
	}
	if t.flags&flagMarshalText != 0 {
		v := t.receiver(p)
		if v == nil {
			return "", nil
		}
		b, err := v.(textMarshaler).MarshalText()
		return string(b), err
	}
	if t.kind == kindInt {
		return strconv.FormatInt(t.load(p).(int64), 10), nil
	}
	return strconv.FormatUint(t.load(p).(uint64), 10), nil
}

func (e *encodeState) dynamic(v any, quoted bool) error {
	if v == nil || isNilPointer(v) {
		e.buf = append(e.buf, "null"...)
		return nil
	}
	if _, ok := v.(Marshaler); ok {
		return e.marshalJSON(v, "")
	}
	if _, ok := v.(textMarshaler); ok {
		return e.marshalText(v, "")
	}
	switch x := v.(type) {
	case bool:
		return e.basic(x, kindBool, quoted)
	case int:
		return e.basic(int64(x), kindInt, quoted)
	case int8:
		return e.basic(int64(x), kindInt, quoted)
	case int16:
		return e.basic(int64(x), kindInt, quoted)
	case int32:
		return e.basic(int64(x), kindInt, quoted)
	case int64:
		return e.basic(x, kindInt, quoted)
	case uint:
		return e.basic(uint64(x), kindUint, quoted)
	case uint8:
		return e.basic(uint64(x), kindUint, quoted)
	case uint16:
		return e.basic(uint64(x), kindUint, quoted)
	case uint32:
		return e.basic(uint64(x), kindUint, quoted)
	case uint64:
		return e.basic(x, kindUint, quoted)
	case float32:
		return e.float(float64(x), 32, quoted)
	case float64:
		return e.float(x, 64, quoted)
	case string:
		return e.basic(x, kindString, quoted)
	case []byte:
		return e.bytes(x)
	case []any:
		if x == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		if err := e.enter(nil, "[]interface {}"); err != nil {
			return err
		}
		e.depth++
		e.buf = append(e.buf, '[')
		for i, el := range x {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			if err := e.dynamic(el, false); err != nil {
				return err
			}
		}
		e.buf = append(e.buf, ']')
		e.depth--
		e.ptrLevel--
		return nil
	case map[string]any:
		if x == nil {
			e.buf = append(e.buf, "null"...)
			return nil
		}
		if err := e.enter(nil, "map[string]interface {}"); err != nil {
			return err
		}
		sv := &keyed{}
		for k, el := range x {
			sv.keys = append(sv.keys, k)
			sv.vals = append(sv.vals, el)
		}
		sort.Sort(sv)
		e.depth++
		e.buf = append(e.buf, '{')
		for i, k := range sv.keys {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			e.buf = appendString(e.buf, k, !e.rawHTML)
			e.buf = append(e.buf, ':')
			if err := e.dynamic(sv.vals[i], false); err != nil {
				return err
			}
		}
		e.buf = append(e.buf, '}')
		e.depth--
		e.ptrLevel--
		return nil
	}
	return &UnsupportedTypeError{dynamicTypeName(v)}
}

func isNilPointer(v any) bool { return false }

type never struct{ _ int }

const conversionPrefix = "interface conversion: interface {} is "

func dynamicTypeName(v any) (name string) {
	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok {
			return
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, conversionPrefix) {
			return
		}
		msg = msg[len(conversionPrefix):]
		if i := strings.LastIndex(msg, ", not "); i >= 0 {
			name = msg[:i]
		}
	}()
	_ = v.(never)
	return ""
}

func appendString(dst []byte, src string, escapeHTML bool) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(src); {
		if b := src[i]; b < utf8.RuneSelf {
			if htmlSafeSet[b] || (!escapeHTML && safeSet[b]) {
				i++
				continue
			}
			dst = append(dst, src[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(src[i:min(len(src), i+utf8.UTFMax)])
		if c == utf8.RuneError && size == 1 {
			dst = append(dst, src[start:i]...)
			dst = append(dst, "\uFFFD"...)
			i += size
			start = i
			continue
		}
		if c == '\u2028' || c == '\u2029' {
			dst = append(dst, src[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hex[c&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, src[start:]...)
	dst = append(dst, '"')
	return dst
}

func isValidNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	switch {
	default:
		return false
	case s[0] == '0':
		s = s[1:]
	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	if len(s) >= 2 && s[0] == '.' && '0' <= s[1] && s[1] <= '9' {
		s = s[2:]
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			if s == "" {
				return false
			}
		}
		for len(s) > 0 && '0' <= s[0] && s[0] <= '9' {
			s = s[1:]
		}
	}
	return s == ""
}
