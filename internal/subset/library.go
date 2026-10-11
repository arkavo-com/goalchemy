package subset

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/jsontype"
)

const (
	fmtPackage  = catalog.StdModule + "fmt"
	slogPackage = catalog.StdModule + "log/slog"
)

func calleeIdent(fun ast.Expr) *ast.Ident {
	switch f := ast.Unparen(fun).(type) {
	case *ast.SelectorExpr:
		return f.Sel
	case *ast.Ident:
		return f
	}
	return nil
}

// libraryCall checks standard library calls that Go implements with
// reflection: errors.As targets, fmt operands and slog values must have static
// types the compiler and std/fmt can handle without it.
func (c *checker) libraryCall(n *ast.CallExpr, fun ast.Expr) {
	id := calleeIdent(fun)
	if id == nil {
		return
	}
	fn, ok := c.info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return
	}
	switch {
	case catalog.IsErrorsAs(fn):
		c.called[id] = true
		c.errorsAs(n, fn)
	case isJSONMarshal(fn):
		c.called[id] = true
		c.jsonMarshal(n, fn)
	case isJSONStream(fn):
		c.called[id] = true
		c.jsonStream(n, fn)
	case fn.Pkg().Path() == fmtPackage && fn.Signature().Variadic() && !n.Ellipsis.IsValid():
		params := fn.Signature().Params()
		for _, a := range n.Args[min(params.Len()-1, len(n.Args)):] {
			c.fmtOperand(a)
		}
	case fn.Pkg().Path() == slogPackage:
		params := fn.Signature().Params()
		switch {
		case fn.Signature().Variadic() && !n.Ellipsis.IsValid() && types.Identical(params.At(params.Len()-1).Type(), anySlice):
			for _, a := range n.Args[min(params.Len()-1, len(n.Args)):] {
				c.slogOperand(a)
			}
		case fn.Signature().Recv() == nil && (fn.Name() == "Any" && len(n.Args) == 2 || fn.Name() == "AnyValue" && len(n.Args) == 1):
			c.slogOperand(n.Args[len(n.Args)-1])
		}
	}
}

func (c *checker) errorsAs(n *ast.CallExpr, fn *types.Func) {
	if c.pkg.PkgPath == catalog.ErrorsPackage {
		return
	}
	if fn.Pkg().Path() != catalog.ErrorsPackage {
		c.unsupported(n, "GCS006", "lib/errors.As", "Import github.com/eugenioenko/goalchemy/std/errors and call errors.As.")
		return
	}
	if len(n.Args) != 2 {
		c.unsupported(n, "GCS006", "errors.As with a multi-value argument", "Pass the error and the target pointer as separate arguments.")
		return
	}
	t := c.info.TypeOf(n.Args[1])
	if t == nil {
		return
	}
	p, ok := t.Underlying().(*types.Pointer)
	if !ok {
		c.report(n.Args[1], "GCS006", "errors.As target", "errors.As target of static type "+typeName(t)+" is not supported",
			"Pass a pointer such as &target, where target is an interface type or implements error.")
		return
	}
	if !types.IsInterface(p.Elem()) && !types.Implements(p.Elem(), errorInterface) {
		remedy := "Point target at an interface type or at a type implementing error."
		if types.Implements(types.NewPointer(p.Elem()), errorInterface) {
			remedy = "Use a pointer to the pointer type, such as var target *T; errors.As(err, &target)."
		}
		c.report(n.Args[1], "GCS006", "errors.As target", "errors.As target *"+typeName(p.Elem())+" must point to an interface type or a type implementing error", remedy)
	}
}

func (c *checker) deferredIntrinsic(call *ast.CallExpr, stmt string) {
	id := calleeIdent(call.Fun)
	if id == nil {
		return
	}
	if catalog.IsErrorsAs(c.info.Uses[id]) {
		c.unsupported(call, "GCS006", stmt+" errors.As", "Call errors.As directly and use its result.")
	}
	if name, ok := catalog.JSONMarshal(c.info.Uses[id]); ok {
		c.unsupported(call, "GCS006", stmt+" json."+name, "Call json."+name+" directly and use its results.")
	}
	if name, ok := catalog.JSONStream(c.info.Uses[id]); ok {
		c.unsupported(call, "GCS006", stmt+" "+jsonStreamName(name), "Call "+jsonStreamName(name)+" directly and use its result.")
	}
}

func isJSONMarshal(obj types.Object) bool {
	_, ok := catalog.JSONMarshal(obj)
	return ok
}

func isJSONStream(obj types.Object) bool {
	_, ok := catalog.JSONStream(obj)
	return ok
}

func jsonStreamName(method string) string {
	if method == "Encode" {
		return "json.Encoder.Encode"
	}
	return "json.Decoder.Decode"
}

// jsonStream checks Encoder.Encode operands as json.Marshal operands and
// Decoder.Decode targets as json.Unmarshal targets.
func (c *checker) jsonStream(n *ast.CallExpr, fn *types.Func) {
	if c.pkg.PkgPath == catalog.JSONPackage {
		return
	}
	name := jsonStreamName(fn.Name())
	if fn.Name() == "Decode" {
		c.jsonUnmarshal(n.Args[0], name)
		return
	}
	c.jsonOperand(n.Args[0], name)
}

const jsonDynamicRemedy = "Pass the value to json.Marshal directly or through a field of its own type, or build dynamic JSON with jsonvalue.Value."

// jsonMarshal checks that every type reachable from a json.Marshal operand
// has an encoder that behaves exactly like encoding/json's, and that values
// visibly stored in interfaces are JSON-shaped.
func (c *checker) jsonMarshal(n *ast.CallExpr, fn *types.Func) {
	if c.pkg.PkgPath == catalog.JSONPackage {
		return
	}
	name := "json." + fn.Name()
	if fn.Pkg().Path() != catalog.JSONPackage {
		c.unsupported(n, "GCS006", "lib/"+name, "Import github.com/eugenioenko/goalchemy/std/encoding/json and call "+name+".")
		return
	}
	want := map[string]int{"Marshal": 1, "MarshalIndent": 3, "Unmarshal": 2}[fn.Name()]
	if len(n.Args) != want {
		c.unsupported(n, "GCS006", name+" with a multi-value argument", "Pass each argument separately.")
		return
	}
	if fn.Name() == "Unmarshal" {
		c.jsonUnmarshal(n.Args[1], name)
		return
	}
	c.jsonOperand(n.Args[0], name)
}

func (c *checker) jsonOperand(arg ast.Expr, name string) {
	tv := c.info.Types[arg]
	if tv.Type == nil || tv.IsNil() {
		return
	}
	if !types.IsInterface(tv.Type) {
		if p := jsontype.Check(tv.Type); p != nil {
			c.report(arg, "GCS006", name+" operand", name+" cannot encode "+p.Path+": "+p.Msg,
				"Give the field a supported type, implement json.Marshaler, or exclude it with a `json:\"-\"` tag.")
			return
		}
	}
	c.jsonLiteral(arg)
}

// jsonUnmarshal checks that an Unmarshal target decodes exactly as with
// encoding/json: a pointer whose reachable types the compiler can describe,
// or an interface whose visibly stored value std/encoding/json can decode.
func (c *checker) jsonUnmarshal(target ast.Expr, name string) {
	tv := c.info.Types[target]
	if tv.Type == nil || tv.IsNil() {
		return
	}
	if types.IsInterface(tv.Type) {
		if call, ok := ast.Unparen(target).(*ast.CallExpr); ok {
			if ft := c.info.Types[call.Fun]; ft.IsType() && len(call.Args) == 1 {
				if at := c.info.TypeOf(call.Args[0]); at != nil && !types.IsInterface(at) && !jsontype.DynamicDecode(at) {
					c.report(call.Args[0], "GCS006", name+" target in an interface",
						name+" cannot decode into a "+typeName(at)+" stored in an interface",
						"Pass the pointer to "+name+" directly, without converting it to an interface.")
				}
			}
		}
		return
	}
	p, ok := tv.Type.Underlying().(*types.Pointer)
	if !ok {
		c.report(target, "GCS006", name+" target",
			name+" target of static type "+typeName(tv.Type)+" is not a pointer",
			"Pass a pointer, such as &value.")
		return
	}
	if prob := jsontype.CheckDecode(p.Elem()); prob != nil {
		c.report(target, "GCS006", name+" target", name+" cannot decode "+prob.Path+": "+prob.Msg,
			"Give the field a supported type, implement json.Unmarshaler, or exclude it with a `json:\"-\"` tag.")
	}
}

func (c *checker) jsonLiteral(e ast.Expr) {
	switch x := ast.Unparen(e).(type) {
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			c.jsonLiteral(x.X)
		}
	case *ast.CallExpr:
		if tv := c.info.Types[x.Fun]; tv.IsType() && len(x.Args) == 1 {
			if types.IsInterface(tv.Type) {
				c.jsonInterfaceValue(x.Args[0])
			} else {
				c.jsonLiteral(x.Args[0])
			}
		}
	case *ast.CompositeLit:
		lt := c.info.TypeOf(x)
		if lt == nil {
			return
		}
		if p, ok := lt.Underlying().(*types.Pointer); ok {
			lt = p.Elem()
		}
		for i, el := range x.Elts {
			v := el
			kv, isKV := el.(*ast.KeyValueExpr)
			if isKV {
				v = kv.Value
			}
			var slot types.Type
			switch u := lt.Underlying().(type) {
			case *types.Struct:
				if isKV {
					if k, ok := kv.Key.(*ast.Ident); ok {
						for j := 0; j < u.NumFields(); j++ {
							if u.Field(j).Name() == k.Name {
								slot = u.Field(j).Type()
							}
						}
					}
				} else if i < u.NumFields() {
					slot = u.Field(i).Type()
				}
			case *types.Slice:
				slot = u.Elem()
			case *types.Array:
				slot = u.Elem()
			case *types.Map:
				slot = u.Elem()
			}
			if slot != nil && types.IsInterface(slot) {
				c.jsonInterfaceValue(v)
			} else {
				c.jsonLiteral(v)
			}
		}
	}
}

func (c *checker) jsonInterfaceValue(v ast.Expr) {
	tv := c.info.Types[v]
	if tv.Type == nil || tv.IsNil() {
		return
	}
	if !types.IsInterface(tv.Type) && !jsontype.Dynamic(tv.Type) {
		c.report(v, "GCS006", "json value in an interface",
			"json.Marshal cannot encode a "+typeName(tv.Type)+" stored in an interface; only JSON-shaped values, json.Marshaler and encoding.TextMarshaler are encoded dynamically",
			jsonDynamicRemedy)
		return
	}
	c.jsonLiteral(v)
}

var errorInterface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

var stringerInterface = types.NewInterfaceType([]*types.Func{
	types.NewFunc(token.NoPos, nil, "String", types.NewSignatureType(nil, nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.String])), false)),
}, nil).Complete()

var anySlice = types.NewSlice(types.Universe.Lookup("any").Type())

// slogOperand rejects log/slog values that std/log/slog would render
// through std/fmt but std/fmt cannot format.
func (c *checker) slogOperand(a ast.Expr) {
	tv, ok := c.info.Types[a]
	if !ok || tv.Type == nil {
		return
	}
	if hasMethod(tv.Type, "LogValue") || hasMethod(tv.Type, "MarshalText") {
		return
	}
	if s, ok := types.Unalias(tv.Type).(*types.Slice); ok {
		if n, ok := types.Unalias(s.Elem()).(*types.Named); ok && n.Obj().Name() == "Attr" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == slogPackage {
			return
		}
	}
	c.operand(a, "slog value", "std/log/slog renders basic types, []byte, interfaces, and values with an Error, String, LogValue or MarshalText method without reflection")
}

// fmtOperand rejects operands std/fmt cannot format without reflection:
// values other than basic types, []byte, interfaces, and types with an
// Error, String, GoString or Format method.
func (c *checker) fmtOperand(a ast.Expr) {
	c.operand(a, "fmt operand", "std/fmt formats basic types, []byte, interfaces, and values with an Error or String method without reflection")
}

func (c *checker) operand(a ast.Expr, what, why string) {
	tv, ok := c.info.Types[a]
	if !ok || tv.Type == nil || tv.IsNil() {
		return
	}
	t := tv.Type
	if b, ok := t.(*types.Basic); ok && b.Info()&types.IsUntyped != 0 {
		return
	}
	if types.IsInterface(t) || types.Implements(t, errorInterface) || types.Implements(t, stringerInterface) || hasMethod(t, "Format") || hasMethod(t, "GoString") {
		return
	}
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		if u.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) != 0 {
			return
		}
	case *types.Slice:
		if b, ok := types.Unalias(u.Elem()).(*types.Basic); ok && b.Kind() == types.Uint8 {
			return
		}
	}
	c.report(a, "GCS006", what+" of type "+typeName(t),
		what+" of type "+typeName(t)+" is not supported: "+why,
		"Convert named basic types to their basic type, such as int(x), or give the type a String() string method.")
}

// hasMethod reports whether t's method set has the named method with the
// signature std/fmt or std/log/slog asserts for.
func hasMethod(t types.Type, name string) bool {
	obj, _, _ := types.LookupFieldOrMethod(t, false, nil, name)
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	sig := fn.Signature()
	params, results := sig.Params(), sig.Results()
	if sig.Variadic() {
		return false
	}
	switch name {
	case "Format":
		return params.Len() == 2 && results.Len() == 0 && isNamed(params.At(0).Type(), fmtPackage, "State") &&
			types.Identical(params.At(1).Type(), types.Typ[types.Int32])
	case "GoString":
		return params.Len() == 0 && results.Len() == 1 && types.Identical(results.At(0).Type(), types.Typ[types.String])
	case "LogValue":
		return params.Len() == 0 && results.Len() == 1 && isNamed(results.At(0).Type(), slogPackage, "Value")
	case "MarshalText":
		return params.Len() == 0 && results.Len() == 2 && types.Identical(results.At(0).Type(), types.NewSlice(types.Typ[types.Byte])) &&
			types.Identical(results.At(1).Type(), types.Universe.Lookup("error").Type())
	}
	return false
}

func isNamed(t types.Type, pkg, name string) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == name && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkg
}

func typeName(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}
