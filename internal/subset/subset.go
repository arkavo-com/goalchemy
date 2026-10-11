// Package subset validates that type-checked Go source stays within the
// implemented Goalchemy feature gates. Unknown cases fail closed.
package subset

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/frontend"

	"golang.org/x/tools/go/packages"
)

type Gate string

const (
	Sequential  Gate = "sequential"
	Cooperative Gate = "cooperative"
)

type Options struct {
	Gate Gate
	// External reports whether an object from a non-source package is a
	// registered external capability symbol.
	External func(obj types.Object) bool
	// ExternalPackage reports whether an import path provides capabilities.
	ExternalPackage func(path string) bool
}

type checker struct {
	opts     Options
	fset     *token.FileSet
	prog     *frontend.Program
	pkg      *packages.Package
	info     *types.Info
	symbol   string
	out      []diagnostics.Diagnostic
	reported map[types.Object]bool
	typeMemo map[types.Type]string
	seenPos  map[string]bool
	// called holds callee identifiers of direct errors.As and json.Marshal calls.
	called map[*ast.Ident]bool
}

func Check(prog *frontend.Program, opts Options) []diagnostics.Diagnostic {
	if opts.Gate == "" {
		opts.Gate = Sequential
	}
	if opts.External == nil {
		opts.External = func(types.Object) bool { return false }
	}
	if opts.ExternalPackage == nil {
		opts.ExternalPackage = func(string) bool { return false }
	}
	c := &checker{opts: opts, fset: prog.Fset, prog: prog,
		reported: map[types.Object]bool{}, typeMemo: map[types.Type]string{}, seenPos: map[string]bool{},
		called: map[*ast.Ident]bool{}}
	for _, p := range prog.Source {
		if p.TypesInfo == nil {
			continue
		}
		c.pkg, c.info = p, p.TypesInfo
		for _, f := range p.Syntax {
			c.file(f)
		}
	}
	c.out = append(c.out, libraryGlobals(prog)...)
	diagnostics.Sort(c.out)
	return diagnostics.Dedup(c.out)
}

func (c *checker) report(n ast.Node, code, feature, msg, remedy string) {
	var d diagnostics.Diagnostic
	if n != nil {
		d = diagnostics.Span(c.fset, n.Pos(), n.End())
	}
	key := fmt.Sprintf("%s:%d:%s", d.File, d.Line, code)
	if c.seenPos[key] {
		return
	}
	c.seenPos[key] = true
	d.Code, d.Severity, d.Symbol, d.Feature = code, diagnostics.Error, c.symbol, feature
	d.Message, d.Remedy = msg, remedy
	c.out = append(c.out, d)
}

func (c *checker) unsupported(n ast.Node, code, feature, remedy string) {
	c.report(n, code, feature, feature+" is not supported in the Goalchemy "+string(c.opts.Gate)+" gate", remedy)
}

func (c *checker) file(f *ast.File) {
	for _, g := range f.Comments {
		for _, cm := range g.List {
			if !strings.HasPrefix(cm.Text, "//go:") {
				continue
			}
			if strings.HasPrefix(cm.Text, "//go:build") || strings.HasPrefix(cm.Text, "//go:generate") {
				continue
			}
			name := strings.Fields(cm.Text)[0]
			code, remedy := "GCS001", "Remove the directive; directives that change executable semantics need explicit support."
			if name == "//go:linkname" {
				remedy = "Replace linkname access with included source or an explicit capability."
			}
			c.report(cm, code, "compiler directive "+strings.TrimPrefix(name, "//"),
				"compiler directive "+strings.TrimPrefix(name, "//")+" is not authorized", remedy)
		}
	}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		switch {
		case path == "C":
			c.report(imp, "GCS009", "cgo", "cgo is not supported", "Remove import \"C\" and use an external capability.")
		case path == "unsafe":
			c.report(imp, "GCS002", "import unsafe", "package unsafe is rejected", "Remove unsafe operations.")
		case c.prog.IsSource(path), c.opts.ExternalPackage(path):
		case path == "time":
			c.report(imp, "GCS002", "import time",
				"import \"time\" is outside github.com/eugenioenko/goalchemy/std and github.com/eugenioenko/goalchemy/lib",
				"Import \"github.com/eugenioenko/goalchemy/std/time\" for Time, Duration, Now, Format and Parse, and \"github.com/eugenioenko/goalchemy/lib/time\" for Sleep and scheduler durations.")
		case path == "encoding/json":
			c.report(imp, "GCS002", "import encoding/json",
				"import \"encoding/json\" is outside github.com/eugenioenko/goalchemy/std and github.com/eugenioenko/goalchemy/lib",
				"Import \"github.com/eugenioenko/goalchemy/std/encoding/json\" for Marshal, MarshalIndent and Unmarshal, and \"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue\" to parse, inspect, build and encode JSON values.")
		case replacement[path] != "":
			c.report(imp, "GCS002", "import "+path,
				fmt.Sprintf("import %q is outside github.com/eugenioenko/goalchemy/%s", path, replacement[path]),
				fmt.Sprintf("Import \"github.com/eugenioenko/goalchemy/%s/%s\" instead; it provides the supported subset with the same names.", replacement[path], path))
		default:
			c.report(imp, "GCS002", "import "+path,
				fmt.Sprintf("import %q is neither included source nor a github.com/eugenioenko/goalchemy/lib package", path),
				"Include the package as module source or use a github.com/eugenioenko/goalchemy/lib package.")
		}
	}
	for _, d := range f.Decls {
		c.decl(d)
	}
}

// replacement maps standard packages to the Goalchemy root that replaces them.
var replacement = map[string]string{
	"sync": "lib", "context": "lib", "runtime": "lib", "errors": "std", "fmt": "std",
	"strconv": "std", "strings": "std", "bytes": "std", "sort": "std", "unicode": "std",
	"unicode/utf8": "std", "io": "std", "encoding/hex": "std", "encoding/binary": "std", "log/slog": "std", "os": "std",
}

func (c *checker) decl(d ast.Decl) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		old := c.symbol
		c.symbol = c.pkg.PkgPath + "." + d.Name.Name
		if d.Recv != nil && len(d.Recv.List) == 1 {
			c.symbol = c.pkg.PkgPath + "." + recvName(d.Recv.List[0].Type) + "." + d.Name.Name
		}
		defer func() { c.symbol = old }()
		if d.Type.TypeParams != nil && d.Type.TypeParams.NumFields() > 0 {
			c.unsupported(d.Type.TypeParams, "GCS003", "generic function declaration", "Write a concrete function; generics are rejected in 0.1.")
		}
		if d.Recv != nil {
			for _, f := range d.Recv.List {
				if hasRecvTypeParams(f.Type) {
					c.unsupported(f.Type, "GCS003", "method on a generic type", "Use a concrete receiver type.")
				}
			}
		}
		if d.Body == nil {
			c.unsupported(d, "GCS005", "function without body", "Provide a Go body or declare the function as an external capability.")
		}
		c.walk(d)
	case *ast.GenDecl:
		old := c.symbol
		defer func() { c.symbol = old }()
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				c.symbol = c.pkg.PkgPath + "." + s.Name.Name
				if s.TypeParams != nil && s.TypeParams.NumFields() > 0 {
					feature := "generic type declaration"
					if s.Assign.IsValid() {
						feature = "generic type alias"
					}
					c.unsupported(s.TypeParams, "GCS003", feature, "Declare a concrete type.")
				}
				c.walk(s)
			case *ast.ValueSpec:
				if len(s.Names) > 0 {
					c.symbol = c.pkg.PkgPath + "." + s.Names[0].Name
				}
				c.walk(s)
			case *ast.ImportSpec:
			}
		}
	}
}

func recvName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return "(*" + recvName(e.X) + ")"
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return recvName(e.X)
	case *ast.IndexListExpr:
		return recvName(e.X)
	case *ast.ParenExpr:
		return recvName(e.X)
	}
	return "?"
}

func hasRecvTypeParams(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.StarExpr:
		return hasRecvTypeParams(e.X)
	case *ast.ParenExpr:
		return hasRecvTypeParams(e.X)
	case *ast.IndexExpr, *ast.IndexListExpr:
		return true
	}
	return false
}

func (c *checker) cooperative(n ast.Node, feature string) {
	if c.opts.Gate == Cooperative {
		return
	}
	c.report(n, "GCS011", feature, feature+" requires the cooperative execution gate",
		"Remove concurrency from sequential programs or compile with the cooperative gate.")
}

func (c *checker) walk(root ast.Node) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case nil:
			return false
		case *ast.FuncLit:
			return true
		case *ast.BranchStmt:
			if n.Tok == token.GOTO {
				c.unsupported(n, "GCS005", "goto", "Restructure the control flow with loops, labeled break or continue.")
			}
		case *ast.GoStmt:
			c.cooperative(n, "go statement")
			c.deferredIntrinsic(n.Call, "go")
		case *ast.DeferStmt:
			c.deferredIntrinsic(n.Call, "defer")
		case *ast.SelectStmt:
			c.cooperative(n, "select statement")
		case *ast.SendStmt:
			c.cooperative(n, "channel send")
		case *ast.RangeStmt:
			c.rangeStmt(n)
		case *ast.UnaryExpr:
			switch n.Op {
			case token.ARROW:
				c.cooperative(n, "channel receive")
			case token.AND:
				if c.elementPath(n.X) {
					c.unsupported(n, "GCS006", "pointer to slice or array element", "Copy the element into a variable, or use an index.")
				}
			}
		case *ast.SliceExpr:
			if t, ok := c.info.Types[n.X]; ok {
				if _, isArr := t.Type.Underlying().(*types.Array); isArr && c.elementPath(n.X) {
					c.unsupported(n, "GCS006", "slicing an array stored in a slice or array element", "Copy the array into a variable before slicing it.")
				}
			}
		case *ast.SelectorExpr:
			if c.rejectedPkgRef(n) {
				return false
			}
			c.selector(n)
		case *ast.CallExpr:
			if !c.call(n) {
				return false
			}
		case *ast.Ident:
			c.ident(n)
			return false
		}
		if e, ok := n.(ast.Expr); ok {
			if !c.exprType(e) {
				return false
			}
		}
		return true
	})
}

func (c *checker) rangeStmt(n *ast.RangeStmt) {
	t := c.info.TypeOf(n.X)
	if t == nil {
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Signature:
		c.unsupported(n.X, "GCS005", "range over function iterator", "Use an explicit loop calling the function.")
	case *types.Chan:
		c.cooperative(n.X, "range over channel")
	case *types.Pointer:
		if _, ok := u.Elem().Underlying().(*types.Array); ok && n.Value != nil && c.elementPath(n.X) {
			c.unsupported(n.X, "GCS006", "range over array element", "Copy the array into a variable first.")
		}
	}
}

// elementPath reports whether an addressable expression designates storage
// inside a slice or array element, which 0.1 cannot address.
func (c *checker) elementPath(e ast.Expr) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.SelectorExpr:
			sel := c.info.Selections[x]
			if sel == nil || sel.Kind() != types.FieldVal || sel.Indirect() {
				return false
			}
			e = x.X
		case *ast.IndexExpr:
			t := c.info.TypeOf(x.X)
			if t == nil {
				return false
			}
			switch u := t.Underlying().(type) {
			case *types.Slice, *types.Array:
				return true
			case *types.Pointer:
				_, ok := u.Elem().Underlying().(*types.Array)
				return ok
			}
			return false
		default:
			return false
		}
	}
}

func (c *checker) selector(n *ast.SelectorExpr) {
	sel := c.info.Selections[n]
	if sel == nil {
		return
	}
	if sel.Kind() == types.MethodVal {
		fn := sel.Obj().(*types.Func)
		recv := fn.Signature().Recv()
		if recv != nil {
			if _, ptrRecv := recv.Type().(*types.Pointer); ptrRecv {
				if xt := c.info.TypeOf(n.X); xt != nil {
					if _, isPtr := xt.Underlying().(*types.Pointer); !isPtr && !sel.Indirect() && c.elementPath(n.X) {
						c.unsupported(n, "GCS006", "pointer-receiver call on a slice or array element", "Copy the element to a variable, call the method, then store it back.")
					}
				}
			}
		}
	}
	obj := sel.Obj()
	if obj.Pkg() != nil && !c.prog.IsSource(obj.Pkg().Path()) && !c.opts.External(obj) {
		c.report(n.Sel, "GCS008", "external symbol "+objName(obj),
			fmt.Sprintf("%s is not a registered external capability", objName(obj)),
			"Use included source or declare a capability contract for this symbol.")
	}
}

func objName(obj types.Object) string {
	if fn, ok := obj.(*types.Func); ok {
		if r := fn.Signature().Recv(); r != nil {
			return types.TypeString(r.Type(), nil) + "." + fn.Name()
		}
	}
	if obj.Pkg() == nil {
		return obj.Name()
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

func (c *checker) ident(id *ast.Ident) {
	if id.Name == "_" {
		return
	}
	if obj := c.info.Defs[id]; obj != nil {
		if _, isVar := obj.(*types.Var); isVar {
			if why := c.typeProblem(obj.Type()); why != "" && !c.reported[obj] {
				c.reported[obj] = true
				c.typeDiag(id, obj.Type(), why)
			}
		}
		return
	}
	obj := c.info.Uses[id]
	if obj == nil {
		return
	}
	switch o := obj.(type) {
	case *types.PkgName:
		return
	case *types.Builtin:
		c.builtinName(id, o.Name())
		return
	case *types.Nil:
		return
	}
	if catalog.IsErrorsAs(obj) {
		if !c.called[id] {
			c.unsupported(id, "GCS006", "errors.As used as a function value", "Call errors.As directly with a pointer target.")
		}
		return
	}
	if name, ok := catalog.JSONMarshal(obj); ok {
		if !c.called[id] {
			c.unsupported(id, "GCS006", "json."+name+" used as a function value", "Call json."+name+" directly.")
		}
		return
	}
	if name, ok := catalog.JSONStream(obj); ok && c.pkg.PkgPath != catalog.JSONPackage && !c.called[id] {
		c.unsupported(id, "GCS006", jsonStreamName(name)+" used as a method value", "Call "+jsonStreamName(name)+" directly.")
	}
	if obj.Pkg() != nil && !c.prog.IsSource(obj.Pkg().Path()) {
		if !c.opts.External(obj) {
			c.report(id, "GCS008", "external symbol "+objName(obj),
				fmt.Sprintf("%s is not a registered external capability", objName(obj)),
				"Use included source or declare a capability contract for this symbol.")
		}
		return
	}
	if c.reported[obj] {
		return
	}
	if tv, ok := c.info.Types[id]; ok && tv.Value == nil {
		if why := c.typeProblem(tv.Type); why != "" {
			c.reported[obj] = true
			c.typeDiag(id, tv.Type, why)
		}
	}
	if inst, ok := c.info.Instances[id]; ok && inst.TypeArgs.Len() > 0 {
		c.unsupported(id, "GCS003", "generic instantiation", "Use a concrete function or an explicit non-generic override.")
	}
}

func (c *checker) builtinName(id *ast.Ident, name string) {
	switch name {
	case "len", "cap", "append", "copy", "delete", "make", "new", "panic", "recover",
		"print", "println", "clear", "min", "max":
	case "close":
		c.cooperative(id, "builtin close")
	case "complex", "real", "imag":
		c.unsupported(id, "GCS007", "builtin "+name, "Complex values are unsupported.")
	default:
		c.unsupported(id, "GCS007", "builtin "+name, "Use a supported builtin.")
	}
}

// rejectedPkgRef reports whether e is a qualified identifier from a package
// whose import was already rejected.
func (c *checker) rejectedPkgRef(e ast.Expr) bool {
	sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	pn, ok := c.info.Uses[id].(*types.PkgName)
	if !ok {
		return false
	}
	path := pn.Imported().Path()
	return !c.prog.IsSource(path) && !c.opts.ExternalPackage(path)
}

// call checks calls and reports whether descent should continue.
func (c *checker) call(n *ast.CallExpr) bool {
	fun := ast.Unparen(n.Fun)
	if c.rejectedPkgRef(fun) {
		return false
	}
	if id, ok := fun.(*ast.Ident); ok {
		if b, ok := c.info.Uses[id].(*types.Builtin); ok {
			before := len(c.out)
			c.builtinName(id, b.Name())
			if len(c.out) > before {
				return false
			}
		}
	}
	if tv, ok := c.info.Types[fun]; ok && tv.IsType() && len(n.Args) == 1 {
		to := tv.Type.Underlying()
		from := c.info.TypeOf(n.Args[0])
		if p, ok := to.(*types.Pointer); ok && from != nil {
			if _, toArr := p.Elem().Underlying().(*types.Array); toArr {
				if _, fromSlice := from.Underlying().(*types.Slice); fromSlice {
					c.unsupported(n, "GCS006", "slice to array pointer conversion", "Convert to an array value instead.")
				}
			}
		}
		return true
	}
	c.libraryCall(n, fun)
	id, _ := fun.(*ast.Ident)
	if id == nil {
		return true
	}
	b, ok := c.info.Uses[id].(*types.Builtin)
	if !ok {
		return true
	}
	switch b.Name() {
	case "print", "println":
		for _, a := range n.Args {
			t := c.info.TypeOf(a)
			if t == nil {
				continue
			}
			if c.typeProblem(t) != "" {
				continue
			}
			elems := []types.Type{t}
			if tup, ok := t.(*types.Tuple); ok {
				elems = elems[:0]
				for i := 0; i < tup.Len(); i++ {
					elems = append(elems, tup.At(i).Type())
				}
			}
			for _, et := range elems {
				if bt, ok := et.Underlying().(*types.Basic); !ok || bt.Info()&(types.IsBoolean|types.IsInteger|types.IsString|types.IsFloat) == 0 {
					c.unsupported(a, "GCS006", "print of "+types.TypeString(et, nil),
						"Print only Booleans, integers, and strings; format other values first.")
				}
			}
		}
	}
	return true
}

// exprType reports whether descent should continue below e.
func (c *checker) exprType(e ast.Expr) bool {
	tv, ok := c.info.Types[e]
	if !ok || tv.Type == nil {
		return true
	}
	if tv.IsType() {
		switch e.(type) {
		case *ast.StructType, *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.FuncType, *ast.InterfaceType, *ast.Ellipsis, *ast.ParenExpr:
			if it, ok := tv.Type.(*types.Interface); ok && !it.IsMethodSet() {
				break
			}
			return true
		}
	}
	if tv.Value != nil && !tv.IsType() {
		if tv.Value.Kind() == constant.Int || tv.Value.Kind() == constant.Bool || tv.Value.Kind() == constant.String {
			if why := c.typeProblem(tv.Type); why == "" {
				return false
			}
		}
		if b, ok := tv.Type.(*types.Basic); ok && b.Info()&types.IsUntyped != 0 {
			return false
		}
	}
	if why := c.typeProblem(tv.Type); why != "" {
		c.typeDiag(e, tv.Type, why)
		return false
	}
	return true
}

func (c *checker) typeDiag(n ast.Node, t types.Type, why string) {
	code, remedy := "GCS004", "Use a supported type."
	switch {
	case strings.HasPrefix(why, "generic"), strings.HasPrefix(why, "type parameter"), strings.HasPrefix(why, "type set"):
		code, remedy = "GCS003", "Use concrete types; generics are rejected in 0.1."
	case strings.HasPrefix(why, "channel"):
		if c.opts.Gate != Cooperative {
			c.report(n, "GCS011", why, why+" requires the cooperative execution gate",
				"Remove concurrency from sequential programs or compile with the cooperative gate.")
			return
		}
	case strings.HasPrefix(why, "complex"):
		remedy = "Use supported float32/float64 scalars or explicit real/imaginary components; complex values are unsupported."
	case strings.HasPrefix(why, "unsafe"), strings.HasPrefix(why, "uintptr"):
		remedy = "Remove unsafe and address-integer operations."
	}
	c.report(n, code, why, fmt.Sprintf("type %s uses %s, which is not supported", types.TypeString(t, nil), why), remedy)
}

// typeProblem returns a short description of the first unsupported component
// of t, or "" when t is within the gate.
func (c *checker) typeProblem(t types.Type) string {
	if t == nil {
		return ""
	}
	if r, ok := c.typeMemo[t]; ok {
		return r
	}
	c.typeMemo[t] = ""
	r := c.typeProblemUncached(t)
	c.typeMemo[t] = r
	return r
}

func (c *checker) typeProblemUncached(t types.Type) string {
	switch t := t.(type) {
	case *types.Alias:
		if t.TypeArgs().Len() > 0 {
			return "generic alias instantiation"
		}
		return c.typeProblem(types.Unalias(t))
	case *types.Basic:
		switch {
		case t.Kind() == types.UnsafePointer:
			return "unsafe.Pointer"
		case t.Kind() == types.Uintptr:
			return "uintptr"
		case t.Info()&types.IsComplex != 0:
			return "complex values"
		}
		return ""
	case *types.Named:
		if t.TypeArgs().Len() > 0 {
			return "generic type instantiation"
		}
		if t.Obj().Pkg() != nil && !c.prog.IsSource(t.Obj().Pkg().Path()) {
			if !c.opts.External(t.Obj()) {
				return "external type " + t.Obj().Pkg().Path() + "." + t.Obj().Name()
			}
			// A registered capability type's representation belongs to
			// the capability, not to the source program.
			return ""
		}
		if t.Obj().Pkg() != nil && c.prog.IsSource(t.Obj().Pkg().Path()) {
			// Problems inside a source type are reported at its declaration.
			return ""
		}
		return c.typeProblem(t.Underlying())
	case *types.TypeParam:
		return "type parameter " + t.Obj().Name()
	case *types.Pointer:
		return c.typeProblem(t.Elem())
	case *types.Slice:
		return c.typeProblem(t.Elem())
	case *types.Array:
		return c.typeProblem(t.Elem())
	case *types.Map:
		if r := c.typeProblem(t.Key()); r != "" {
			return r
		}
		return c.typeProblem(t.Elem())
	case *types.Chan:
		if c.opts.Gate != Cooperative {
			return "channel type"
		}
		return c.typeProblem(t.Elem())
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if r := c.typeProblem(t.Field(i).Type()); r != "" {
				return r
			}
		}
		return ""
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if r := c.typeProblem(t.At(i).Type()); r != "" {
				return r
			}
		}
		return ""
	case *types.Signature:
		if t.TypeParams().Len() > 0 {
			return "generic signature"
		}
		if r := c.typeProblem(t.Params()); r != "" {
			return r
		}
		return c.typeProblem(t.Results())
	case *types.Interface:
		if !t.IsMethodSet() {
			return "type set interface"
		}
		for i := 0; i < t.NumMethods(); i++ {
			if r := c.typeProblem(t.Method(i).Type()); r != "" {
				return r
			}
		}
		return ""
	}
	return fmt.Sprintf("unknown type form %T", t)
}
