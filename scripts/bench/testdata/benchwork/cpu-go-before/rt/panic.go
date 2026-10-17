package rt

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// PlainError is a runtime.Error whose message has no "runtime error: " prefix.
type PlainError string

func (e PlainError) Error() string { return string(e) }
func (e PlainError) RuntimeError() {}

// RuntimeError is a runtime.Error with the "runtime error: " prefix.
type RuntimeError string

func (e RuntimeError) Error() string { return "runtime error: " + string(e) }
func (e RuntimeError) RuntimeError() {}

// TypeAssertionError matches the message of *runtime.TypeAssertionError.
type TypeAssertionError struct{ msg string }

func (e *TypeAssertionError) Error() string { return e.msg }
func (e *TypeAssertionError) RuntimeError() {}

// CheckKey panics as Go map hashing does when k holds an unhashable value.
func CheckKey(k any) {
	if t := badKey(reflect.ValueOf(k)); t != nil {
		panic(RuntimeError("hash of unhashable type " + TypeName(t)))
	}
}

func badKey(v reflect.Value) reflect.Type {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return badKey(v.Elem())
	case reflect.Slice, reflect.Map, reflect.Func:
		return v.Type()
	case reflect.Struct:
		if m, ok := v.Interface().(sourceMap); ok {
			_ = m
			return v.Type()
		}
		for i := 0; i < v.NumField(); i++ {
			if t := badKey(v.Field(i)); t != nil {
				return t
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if t := badKey(v.Index(i)); t != nil {
				return t
			}
		}
	}
	return nil
}

// PanicAssert panics for a failed type assertion of x (static type iface) to
// target. For an interface target, want is a nil pointer to the target type
// and methods lists its methods as goName/srcName pairs; a method with the
// right name but another signature is missing, as in Go.
func PanicAssert(x any, iface, target string, want any, methods ...string) {
	if x == nil {
		panic(&TypeAssertionError{"interface conversion: " + iface + " is nil, not " + target})
	}
	dyn := TypeName(reflect.TypeOf(x))
	if want != nil {
		t := reflect.TypeOf(x)
		it := reflect.TypeOf(want).Elem()
		for i := 0; i+1 < len(methods); i += 2 {
			m, ok := t.MethodByName(methods[i])
			im, _ := it.MethodByName(methods[i])
			if !ok || !sameMethod(m.Type, im.Type) {
				panic(&TypeAssertionError{"interface conversion: " + dyn + " is not " + target + ": missing method " + methods[i+1]})
			}
		}
	}
	panic(&TypeAssertionError{"interface conversion: " + iface + " is " + dyn + ", not " + target})
}

// sameMethod reports whether method type m, whose first input is the
// receiver, has interface method type im.
func sameMethod(m, im reflect.Type) bool {
	if im == nil || m.NumIn() != im.NumIn()+1 || m.NumOut() != im.NumOut() || m.IsVariadic() != im.IsVariadic() {
		return false
	}
	for i := 0; i < im.NumIn(); i++ {
		if m.In(i+1) != im.In(i) {
			return false
		}
	}
	for i := 0; i < im.NumOut(); i++ {
		if m.Out(i) != im.Out(i) {
			return false
		}
	}
	return true
}

var names = map[reflect.Type]string{}

// RegisterType records the source name of a generated named type.
func RegisterType(t reflect.Type, name string) { names[t] = name }

type sourceMap interface {
	sourceMapTypes() (reflect.Type, reflect.Type)
}

// TypeName renders t the way the Go runtime prints source types.
func TypeName(t reflect.Type) string {
	if t == nil {
		return "nil"
	}
	if n, ok := names[t]; ok {
		return n
	}
	if t.Kind() == reflect.Struct {
		if m, ok := reflect.Zero(t).Interface().(sourceMap); ok {
			k, v := m.sourceMapTypes()
			return "map[" + TypeName(k) + "]" + TypeName(v)
		}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + TypeName(t.Elem())
	case reflect.Slice:
		return "[]" + TypeName(t.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + TypeName(t.Elem())
	case reflect.Map:
		return "map[" + TypeName(t.Key()) + "]" + TypeName(t.Elem())
	case reflect.Func:
		var b strings.Builder
		b.WriteString("func(")
		for i := 0; i < t.NumIn(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			if t.IsVariadic() && i == t.NumIn()-1 {
				b.WriteString("..." + TypeName(t.In(i).Elem()))
			} else {
				b.WriteString(TypeName(t.In(i)))
			}
		}
		b.WriteString(")")
		if t.NumOut() == 1 {
			b.WriteString(" " + TypeName(t.Out(0)))
		} else if t.NumOut() > 1 {
			b.WriteString(" (")
			for i := 0; i < t.NumOut(); i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(TypeName(t.Out(i)))
			}
			b.WriteString(")")
		}
		return b.String()
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return "interface {}"
		}
	case reflect.Struct:
		var b strings.Builder
		b.WriteString("struct {")
		for i := 0; i < t.NumField(); i++ {
			if i > 0 {
				b.WriteString(";")
			}
			f := t.Field(i)
			b.WriteString(" ")
			if !f.Anonymous {
				b.WriteString(f.Name + " ")
			}
			b.WriteString(TypeName(f.Type))
		}
		if t.NumField() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("}")
		return b.String()
	}
	return t.String()
}

// FormatPanic renders a panic value as the Go runtime prints it.
func FormatPanic(v any) string {
	switch x := v.(type) {
	case error:
		return x.Error()
	case interface{ String() string }:
		return x.String()
	case nil:
		return "nil"
	}
	rv := reflect.ValueOf(v)
	name := TypeName(rv.Type())
	builtin := rv.Type().PkgPath() == "" && rv.Type().Name() != "" && names[rv.Type()] == ""
	var s string
	switch rv.Kind() {
	case reflect.String:
		s = rv.String()
		if !builtin {
			return name + "(" + strconv.Quote(s) + ")"
		}
		return s
	case reflect.Bool:
		s = strconv.FormatBool(rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		s = strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		s = strconv.FormatUint(rv.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		s = strconv.FormatFloat(rv.Float(), 'g', -1, rv.Type().Bits())
	default:
		return "(" + name + ") " + fmt.Sprintf("%p", v)
	}
	if !builtin {
		return name + "(" + s + ")"
	}
	return s
}

// mainHook is set by the scheduler when the program uses tasks.
var mainHook func(entry func())

func reportPanic(r any) {
	os.Stderr.WriteString("panic: " + FormatPanic(r) + "\n")
	os.Exit(2)
}

// Main runs a program entry point, reporting an unrecovered panic as the Go
// runtime does: "panic: <value>" on standard error and exit status 2.
func Main(entry func()) {
	defer func() {
		if r := recover(); r != nil {
			reportPanic(r)
		}
	}()
	if mainHook != nil {
		mainHook(entry)
		return
	}
	entry()
}
