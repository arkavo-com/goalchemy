package catalog

import "go/types"

// ErrorsPackage is the standard errors package compiled as source.
const ErrorsPackage = StdModule + "errors"

// IsErrorsAs reports whether obj is errors.As from std/errors or its native
// lib/errors counterpart. Go implements As with reflection, so the compiler
// expands each call for the static type of its target instead.
func IsErrorsAs(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Name() != "As" || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == ErrorsPackage || fn.Pkg().Path() == LibModule+"errors"
}

// JSONPackage is the standard encoding/json package compiled as source.
const JSONPackage = StdModule + "encoding/json"

// JSONMarshal reports whether obj is json.Marshal, json.MarshalIndent or
// json.Unmarshal from std/encoding/json or its native lib/json counterpart,
// returning the name.
// The compiler describes the static type of the operand instead of using
// reflection.
func JSONMarshal(obj types.Object) (string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Signature().Recv() != nil {
		return "", false
	}
	p := fn.Pkg().Path()
	switch fn.Name() {
	case "Marshal", "MarshalIndent", "Unmarshal":
		return fn.Name(), p == JSONPackage || p == LibModule+"json"
	case "MarshalStream", "UnmarshalStream":
		return fn.Name(), p == LibModule+"json"
	}
	return "", false
}

// JSONStream reports whether obj is the Encode method of std/encoding/json's
// Encoder or the Decode method of its Decoder, returning the name. The
// compiler describes the static type of the operand, as for json.Marshal.
func JSONStream(obj types.Object) (string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != JSONPackage {
		return "", false
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return "", false
	}
	ptr, ok := recv.Type().(*types.Pointer)
	if !ok {
		return "", false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return "", false
	}
	switch {
	case fn.Name() == "Encode" && named.Obj().Name() == "Encoder", fn.Name() == "Decode" && named.Obj().Name() == "Decoder":
		return fn.Name(), true
	}
	return "", false
}
