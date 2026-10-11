// SPDX-License-Identifier: Apache-2.0

// Package json is the native Go implementation behind std/encoding/json used
// when the program runs with the Go toolchain. The compiler expands calls to
// std/encoding/json.Marshal and MarshalIndent for the static type of their
// operand; these functions are not capabilities.
package json

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// Error kinds reported by Marshal and MarshalIndent.
const (
	OK = iota
	UnsupportedType
	UnsupportedValue
	MarshalerFailed
	MarshalerSyntax
	Other
	UnmarshalType = iota + 4
	UnmarshalTypeSyntax
	Syntax
	InvalidUnmarshal
)

// Marshal returns the JSON encoding of v. A failure is reported by kind with
// the type name, the marshaler method or value text, the syntax error message
// and offset, and the underlying error, so std/encoding/json can rebuild its
// own error values.
func Marshal(v any) ([]byte, int, string, string, string, int64, error) {
	b, err := json.Marshal(v)
	return classify(b, err)
}

// MarshalIndent is like Marshal but formats the output with Indent.
func MarshalIndent(v any, prefix, indent string) ([]byte, int, string, string, string, int64, error) {
	b, err := json.MarshalIndent(v, prefix, indent)
	return classify(b, err)
}

// Unmarshal decodes data into v. A failure is reported by kind with the
// type error's value, type, offset, struct and field, the syntax error
// message, and the underlying error, so std/encoding/json can rebuild its
// own error values. Errors returned by methods of v are passed through.
func Unmarshal(data []byte, v any) (int, string, string, int64, string, string, string, error) {
	return classifyUnmarshal(json.Unmarshal(data, v))
}

// MarshalStream returns the encoding of v that an Encoder with the given
// HTML escaping writes, without the trailing newline.
func MarshalStream(v any, escapeHTML bool) ([]byte, int, string, string, string, int64, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		return classify(nil, err)
	}
	return classify(bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil)
}

// UnmarshalStream decodes one complete JSON value read by a Decoder into v
// with the Decoder's options, reporting failures as Unmarshal does. With
// useNumber, numbers stored in interfaces are converted by number to the
// caller's Number type.
func UnmarshalStream(data []byte, v any, useNumber, disallowUnknown bool, number func(string) any) (int, string, string, int64, string, string, string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if useNumber {
		dec.UseNumber()
	}
	if disallowUnknown {
		dec.DisallowUnknownFields()
	}
	err := dec.Decode(v)
	if useNumber {
		if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && !rv.IsNil() {
			convertNumbers(rv.Elem(), number, map[visit]bool{})
		}
	}
	return classifyUnmarshal(err)
}

var numberType = reflect.TypeFor[json.Number]()

type visit struct {
	p uintptr
	n int
	t reflect.Type
}

func convertNumbers(v reflect.Value, number func(string) any, seen map[visit]bool) {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		e := v.Elem()
		if e.Type() == numberType {
			if v.CanSet() {
				v.Set(reflect.ValueOf(number(e.String())))
			}
			return
		}
		c := reflect.New(e.Type()).Elem()
		c.Set(e)
		convertNumbers(c, number, seen)
		if v.CanSet() {
			v.Set(c)
		}
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() || !v.CanInterface() {
			return
		}
		key := visit{v.Pointer(), 0, v.Type()}
		if v.Kind() == reflect.Slice {
			key.n = v.Len()
		}
		if seen[key] {
			return
		}
		seen[key] = true
		switch v.Kind() {
		case reflect.Pointer:
			convertNumbers(v.Elem(), number, seen)
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				convertNumbers(v.Index(i), number, seen)
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				c := reflect.New(v.Type().Elem()).Elem()
				c.Set(v.MapIndex(k))
				convertNumbers(c, number, seen)
				v.SetMapIndex(k, c)
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			convertNumbers(v.Index(i), number, seen)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.IsExported() || f.Anonymous {
				convertNumbers(v.Field(i), number, seen)
			}
		}
	}
}

func classifyUnmarshal(err error) (int, string, string, int64, string, string, string, error) {
	if err == nil {
		return OK, "", "", 0, "", "", "", nil
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) && error(ute) == err {
		cause := errors.Unwrap(ute)
		var se *json.SyntaxError
		if errors.As(cause, &se) && error(se) == cause {
			return UnmarshalTypeSyntax, ute.Value, ute.Type.String(), ute.Offset, ute.Struct, ute.Field, se.Error(), nil
		}
		return UnmarshalType, ute.Value, ute.Type.String(), ute.Offset, ute.Struct, ute.Field, "", cause
	}
	var se *json.SyntaxError
	if errors.As(err, &se) && error(se) == err {
		return Syntax, "", "", se.Offset, "", "", se.Error(), nil
	}
	var iue *json.InvalidUnmarshalError
	if errors.As(err, &iue) && error(iue) == err {
		name := ""
		if iue.Type != nil {
			name = iue.Type.String()
		}
		return InvalidUnmarshal, "", name, 0, "", "", "", nil
	}
	return Other, "", "", 0, "", "", "", err
}

func classify(b []byte, err error) ([]byte, int, string, string, string, int64, error) {
	if err == nil {
		return b, OK, "", "", "", 0, nil
	}
	var ute *json.UnsupportedTypeError
	if errors.As(err, &ute) && error(ute) == err {
		return nil, UnsupportedType, ute.Type.String(), "", "", 0, nil
	}
	var uve *json.UnsupportedValueError
	if errors.As(err, &uve) && error(uve) == err {
		return nil, UnsupportedValue, "", uve.Str, "", 0, nil
	}
	var me *json.MarshalerError
	if errors.As(err, &me) && error(me) == err {
		src := strings.TrimPrefix(me.Error(), "json: error calling ")
		src = src[:strings.Index(src, " ")]
		var se *json.SyntaxError
		if errors.As(me.Err, &se) && error(se) == me.Err {
			return nil, MarshalerSyntax, me.Type.String(), src, se.Error(), se.Offset, nil
		}
		return nil, MarshalerFailed, me.Type.String(), src, "", 0, me.Err
	}
	return nil, Other, "", "", err.Error(), 0, nil
}
