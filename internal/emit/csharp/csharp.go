// Package csharp emits C# 12 for .NET 8. Integers of every kind are longs
// normalized to their Go width (unsigned 64-bit values keep their bits);
// strings hold one char per byte; structs are generated classes updated in
// place; slices, maps, and function values hold boxed values.
package csharp

import (
	"bytes"
	"fmt"
	"go/constant"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
	"github.com/eugenioenko/goalchemy/internal/structure"
)

type Output struct {
	API       []artifact.APIDeclaration
	Files     []artifact.File
	Packages  []artifact.PackageFiles
	Contracts []string
}

type emitter struct {
	api       []artifact.APIDeclaration
	p         *ir.Program
	names     *naming.Names
	symbols   map[string]string
	contracts map[string]bool
	classes   map[*ir.Type]string
	tds       map[*ir.Type]string
	helperQ   []*ir.Type
	helperOut bytes.Buffer
	seenHelp  map[*ir.Type]bool
	frames    bytes.Buffer
	layout    *artifact.Layout
	helpers   map[string]*bytes.Buffer
}

const marker = "\x00@"

const spawnHelper = `    static void _spawnCall(Fn _f, object[] _a) {
        Fn g = Program.fnchk(_f);
        R.spawn(R.sync(() => {
            g.F(_a);
            return Array.Empty<object>();
        }));
    }

`

const mainHelpers = `    static Fn _envFn(object[] _env, Func<object[], object[], object> _f) => new Fn(_a => _f(_env, _a));

    static string _missing(Box _x, string[] ids, string[] names) {
        string id = Box.implementsAll(_x.t, ids);
        for (int _i = 0; _i < ids.Length; _i++) if (ids[_i] == id) return names[_i];
        return id;
    }

`

func (e *emitter) resolveMarkers(src string) (string, map[int]token.Position) {
	m := map[int]token.Position{}
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		for {
			j := strings.Index(ln, marker)
			if j < 0 {
				break
			}
			k := strings.IndexByte(ln[j+len(marker):], 0)
			n, _ := strconv.Atoi(ln[j+len(marker) : j+len(marker)+k])
			ln = ln[:j] + ln[j+len(marker)+k+1:]
			if e.p.Fset != nil {
				if pos := e.p.Fset.Position(token.Pos(n)); pos.IsValid() {
					m[i+1] = pos
				}
			}
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n"), m
}

func (e *emitter) use(c string) {
	if c != "" {
		e.contracts[c] = true
	}
}

// rc records the use of a contract and names the class holding its
// functions.
func (e *emitter) rc(contract string) string {
	e.use(contract)
	return "R"
}

var opaque = map[string][2]string{
	"task.FutureState": {"FutureState", "lib.task.start"},
	"sync.Mutex":       {"GoMutex", "std.sync.mutex.lock"},
	"sync.WaitGroup":   {"WaitGroup", "std.sync.waitgroup.add"},
	"context.Context":  {"GoContext", "std.context.err"},
	"crypto.Key":       {"Native.Key", "lib.crypto.close"},
	"http.Exchange":    {"R.Exchange", "lib.http.exchange_close"},
}

func intKind(t *ir.Type) string { return t.U().Int.String() }

func byteElem(t *ir.Type) bool { u := t.U(); return u.Kind == ir.KInt && u.Int == ir.U8 }
func byteContainer(t *ir.Type) bool {
	u := t.U()
	return (u.Kind == ir.KSlice || u.Kind == ir.KArray) && byteElem(u.Elem)
}
func sliceNil(t *ir.Type) string {
	if byteContainer(t) {
		return "Slice.BYTE_NIL"
	}
	return "Slice.NIL"
}

func unsigned64(t *ir.Type) bool {
	u := t.U()
	return u.Kind == ir.KInt && u.Int == ir.U64
}

func (e *emitter) susp(t *ir.Type) bool { return e.p.SuspTypes[t.U()] }

func (e *emitter) native() bool { return e.p.PauseModel == "native" }

func (e *emitter) adapt(f string, n int) string {
	e.use("core.task.spawn")
	return fmt.Sprintf("R.adapt(%s, %d)", f, n)
}

// jt renders the Java type of an IR type.
func (e *emitter) jt(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "bool"
	case ir.KFloat:
		if u.FloatBits == 32 {
			return "float"
		}
		return "double"
	case ir.KInt:
		return "long"
	case ir.KString:
		return "string"
	case ir.KStruct:
		return e.class(u)
	case ir.KArray:
		if byteContainer(t) {
			return "byte[]"
		}
		return "object[]"
	case ir.KSlice:
		return "Slice"
	case ir.KMap:
		return "GoMap"
	case ir.KPointer:
		if u.Elem.IsAggregate() {
			return e.jt(u.Elem)
		}
		return "Ref"
	case ir.KFunc:
		return "Fn"
	case ir.KInterface:
		return "Box"
	case ir.KChan:
		e.use("core.chan.make")
		return "Chan"
	case ir.KOpaque:
		o := opaque[u.Name]
		e.use(o[1])
		return o[0]
	case ir.KMapIter:
		return "GoMap.Iter"
	}
	panic("csharp: type " + t.String())
}

// cast converts an Object-typed expression to t's Java type.
func (e *emitter) cast(t *ir.Type, x string) string {
	switch e.jt(t) {
	case "long":
		return "((long) (" + x + "))"
	case "bool":
		return "((bool) (" + x + "))"
	}
	return "((" + e.jt(t) + ") (" + x + "))"
}

func (e *emitter) class(t *ir.Type) string {
	u := t.U()
	if n, ok := e.classes[u]; ok {
		return n
	}
	n := e.names.Type(u, "s_")
	e.classes[u] = n
	e.needHelpers(u)
	return n
}

func (e *emitter) fieldProp(t *ir.Type, i int) string {
	return e.names.Field(t, i, "f_")
}

func (e *emitter) needHelpers(t *ir.Type) {
	u := t.U()
	if e.seenHelp[u] {
		return
	}
	e.seenHelp[u] = true
	e.helperQ = append(e.helperQ, u)
}

func (e *emitter) flushHelpers() {
	for len(e.helperQ) > 0 {
		t := e.helperQ[0]
		e.helperQ = e.helperQ[1:]
		e.helperOut.Reset()
		switch t.Kind {
		case ir.KStruct:
			e.structClass(t)
		case ir.KArray:
			e.arrayHelpers(t)
		}
		e.helpers[e.layout.RepresentationOwner(t)].Write(e.helperOut.Bytes())
	}
}

func (e *emitter) structClass(t *ir.Type) {
	n := e.class(t)
	var b strings.Builder
	fmt.Fprintf(&b, "    sealed class %s {\n", n)
	for i, f := range t.Fields {
		fmt.Fprintf(&b, "        public %s %s;\n", e.jt(f.Type), e.fieldProp(t, i))
	}
	fmt.Fprintf(&b, "        public %s() {\n", n)
	for i, f := range t.Fields {
		fmt.Fprintf(&b, "            %s = %s;\n", e.fieldProp(t, i), e.zero(f.Type))
	}
	b.WriteString("        }\n")
	fmt.Fprintf(&b, "        public %s _clone() {\n            %s _r = new %s();\n            _r._set(this);\n            return _r;\n        }\n", n, n, n)
	fmt.Fprintf(&b, "        public void _set(%s _o) {\n", n)
	for i, f := range t.Fields {
		p := e.fieldProp(t, i)
		if f.Type.IsAggregate() {
			fmt.Fprintf(&b, "            %s;\n", e.setStmt(f.Type, "this."+p, "_o."+p))
		} else {
			fmt.Fprintf(&b, "            this.%s = _o.%s;\n", p, p)
		}
	}
	b.WriteString("        }\n")
	fmt.Fprintf(&b, "        public bool _eq(%s _o) {\n            return true", n)
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		p := e.fieldProp(t, i)
		fmt.Fprintf(&b, " && %s", e.eqExpr(f.Type, "this."+p, "_o."+p))
	}
	b.WriteString(";\n        }\n")
	b.WriteString("        public object _key() {\n            return new KeyList(new object[] {")
	var ks []string
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		ks = append(ks, e.keyExpr(f.Type, "this."+e.fieldProp(t, i)))
	}
	b.WriteString(strings.Join(ks, ", "))
	b.WriteString("});\n        }\n    }\n\n")
	e.helperOut.WriteString(b.String())
}

func (e *emitter) arrayHelpers(t *ir.Type) {
	if byteContainer(t) {
		fmt.Fprintf(&e.helperOut, "    static byte[] %s() => new byte[%d];\n", e.names.Type(t, "zero_"), t.Len)
		fmt.Fprintf(&e.helperOut, "    static byte[] %s(byte[] _s) => (byte[])_s.Clone();\n", e.names.Type(t, "clone_"))
		fmt.Fprintf(&e.helperOut, "    static void %s(byte[] _d, byte[] _s) { Array.Copy(_s, _d, %d); }\n", e.names.Type(t, "set_"), t.Len)
		fmt.Fprintf(&e.helperOut, "    static bool %s(byte[] _x, byte[] _y) { for (int _i = 0; _i < %d; _i++) if (_x[_i] != _y[_i]) return false; return true; }\n", e.names.Type(t, "eq_"), t.Len)
		fmt.Fprintf(&e.helperOut, "    static object %s(byte[] _x) => System.Text.Encoding.Latin1.GetString(_x);\n\n", e.names.Type(t, "key_"))
		return
	}
	el := t.Elem
	var b strings.Builder
	fmt.Fprintf(&b, "    static object[] %s() {\n        object[] _a = new object[%d];\n        for (int _i = 0; _i < %d; _i++) _a[_i] = %s;\n        return _a;\n    }\n", e.names.Type(t, "zero_"), t.Len, t.Len, e.zero(el))
	fmt.Fprintf(&b, "    static object[] %s(object[] _s) {\n        object[] _a = new object[%d];\n        for (int _i = 0; _i < %d; _i++) _a[_i] = %s;\n        return _a;\n    }\n", e.names.Type(t, "clone_"), t.Len, t.Len, e.cloneExpr(el, e.cast(el, "_s[_i]")))
	if el.IsAggregate() {
		fmt.Fprintf(&b, "    static void %s(object[] _d, object[] _s) {\n        for (int _i = 0; _i < %d; _i++) %s;\n    }\n", e.names.Type(t, "set_"), t.Len, e.setStmt(el, e.cast(el, "_d[_i]"), e.cast(el, "_s[_i]")))
	} else {
		fmt.Fprintf(&b, "    static void %s(object[] _d, object[] _s) {\n        Array.Copy(_s, _d, %d);\n    }\n", e.names.Type(t, "set_"), t.Len)
	}
	fmt.Fprintf(&b, "    static bool %s(object[] _x, object[] _y) {\n        for (int _i = 0; _i < %d; _i++) if (!(%s)) return false;\n        return true;\n    }\n", e.names.Type(t, "eq_"), t.Len, e.eqExpr(el, e.cast(el, "_x[_i]"), e.cast(el, "_y[_i]")))
	fmt.Fprintf(&b, "    static object %s(object[] _x) {\n        object[] _k = new object[%d];\n        for (int _i = 0; _i < %d; _i++) _k[_i] = %s;\n        return new KeyList(_k);\n    }\n\n", e.names.Type(t, "key_"), t.Len, t.Len, e.keyExpr(el, e.cast(el, "_x[_i]")))
	e.helperOut.WriteString(b.String())
}

func (e *emitter) zero(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "false"
	case ir.KFloat:
		if u.FloatBits == 32 {
			return "0.0f"
		}
		return "0.0d"
	case ir.KInt:
		return "0L"
	case ir.KString:
		return `""`
	case ir.KSlice:
		return sliceNil(t)
	case ir.KStruct:
		return "new " + e.class(u) + "()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s()", e.names.Type(u, "zero_"))
	case ir.KOpaque:
		if !u.OpaqueRef {
			return "new " + e.jt(u) + "()"
		}
	}
	return "null"
}

func (e *emitter) cloneExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct:
		e.class(u)
		return x + "._clone()"
	case ir.KOpaque:
		if u.OpaqueRef {
			return x
		}
		return x + "._clone()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s)", e.names.Type(u, "clone_"), x)
	}
	return x
}

func (e *emitter) cloneFn(t *ir.Type) string {
	if !t.IsAggregate() {
		return "null"
	}
	return "_v => " + e.cloneExpr(t, e.cast(t, "_v"))
}

func (e *emitter) setStmt(t *ir.Type, dst, src string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KOpaque:
		if u.Kind == ir.KStruct {
			e.class(u)
		}
		return dst + "._set(" + src + ")"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s, %s)", e.names.Type(u, "set_"), dst, src)
	}
	return dst + " = " + src
}

func (e *emitter) eqExpr(t *ir.Type, a, b string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct:
		e.class(u)
		return a + "._eq(" + b + ")"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s, %s)", e.names.Type(u, "eq_"), a, b)
	case ir.KInterface:
		return fmt.Sprintf("Box.ifaceEq(%s, %s)", a, b)
	case ir.KString:
		return a + " == " + b
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("Panics.uncomparableEq(%q)", ir.TypeString(t))
	}
	return a + " == " + b
}

func (e *emitter) keyExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KFloat:
		return "Floats.key(" + x + ")"
	case ir.KStruct:
		e.class(u)
		return x + "._key()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s)", e.names.Type(u, "key_"), x)
	case ir.KInterface:
		return fmt.Sprintf("Box.ifaceKey(%s)", x)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("Panics.unhashableKey(%q)", ir.TypeString(t))
	case ir.KPointer, ir.KChan, ir.KOpaque:
		return "new IdKey(" + x + ")"
	}
	return "(object) (" + x + ")"
}

func (e *emitter) keyFn(t *ir.Type) string {
	switch t.U().Kind {
	case ir.KFloat, ir.KStruct, ir.KArray, ir.KInterface, ir.KPointer, ir.KChan, ir.KOpaque:
		return "_k => " + e.keyExpr(t, e.cast(t, "_k"))
	}
	return "GoMap.identityKey"
}

func (e *emitter) td(t *ir.Type) string {
	if n, ok := e.tds[t]; ok {
		return n
	}
	n := e.names.Type(t, "td_")
	e.tds[t] = n
	return n
}

// fnLit renders a Fn lambda calling f with unboxed arguments; clone, when
// set, clones the receiver.
func (e *emitter) fnLit(f *ir.Func, cloneRecv *ir.Type) string {
	var as []string
	for i, p := range f.Params {
		a := e.cast(p.Type, fmt.Sprintf("_a[%d]", i))
		if i == 0 && cloneRecv != nil {
			a = e.cloneExpr(cloneRecv, a)
		}
		as = append(as, a)
	}
	call := e.symbol(f.Sym) + "(" + strings.Join(as, ", ") + ")"
	if len(f.Results) == 0 && !f.MaySuspend {
		return "new Fn(_a => { " + call + "; return null; })"
	}
	return "new Fn(_a => " + call + ")"
}

func (e *emitter) typeDescs(b *bytes.Buffer) {
	var ts []*ir.Type
	for t := range e.tds {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for _, t := range ts {
		if t.Kind == ir.KString {
			fmt.Fprintf(b, "    static readonly TypeDesc %s = Program.STRING_TYPE;\n", e.tds[t])
			continue
		}
		name := ir.TypeString(t)
		u := t.U()
		basic := "null"
		switch u.Kind {
		case ir.KInt:
			basic = `"int"`
			if u.Int == ir.U64 {
				basic = `"uint"`
			}
		case ir.KBool:
			basic = `"bool"`
		case ir.KString:
			basic = `"string"`
		case ir.KFloat:
			basic = strconv.Quote(u.Basic)
		}
		eq := "(_a, _b) => " + e.eqExpr(t, e.cast(t, "_a"), e.cast(t, "_b"))
		key := "_a => " + e.keyExpr(t, e.cast(t, "_a"))
		comparable := "true"
		if !t.Comparable() {
			eq = fmt.Sprintf("(_a, _b) => Panics.uncomparableEq(%q)", name)
			key = fmt.Sprintf("_a => Panics.unhashableKey(%q)", name)
			comparable = "false"
		}
		var ms []string
		for _, m := range t.MethodSet {
			var clone *ir.Type
			if t.IsAggregate() {
				clone = t
			}
			fn := fmt.Sprintf("Program.closure(%d, %s)", m.Func.ID, e.fnLit(m.Func, clone))
			if e.p.SuspMethods[m.ID] && !m.Func.MaySuspend {
				fn = e.adapt(fn, len(m.Func.Results))
			}
			ms = append(ms, fmt.Sprintf("%q, %s", m.ID, fn))
		}
		fmt.Fprintf(b, "    static readonly TypeDesc %s = new TypeDesc(%s, %q, %s, %s, TypeDesc.MethodsOf(%s), %s, %s);\n",
			e.tds[t], csString(name), u.Kind.String(), eq, key, strings.Join(ms, ", "), basic, comparable)
	}
	e.flushHelpers()
}

// ---- functions ----

type fnEmitter struct {
	e      *emitter
	f      *ir.Func
	b      strings.Builder
	order  map[*ir.Block]int
	defers bool
	frame  bool
	// task is set when f is an async method of the native pause model,
	// taking the running task as its first parameter t.
	task  bool
	folds *structure.Folds
}

func (e *emitter) localName(l *ir.Local) string {
	return e.names.Local(l, "_")
}

func (e *emitter) symbol(sym string) string {
	return e.names.Symbol(sym)
}

func (fe *fnEmitter) w(format string, args ...any) { fmt.Fprintf(&fe.b, format, args...) }

func (fe *fnEmitter) local(l *ir.Local) string {
	name := fe.e.localName(l)
	if n, ok := fe.folds.Name(l); ok {
		name = n
	}
	if fe.frame {
		return "_fr." + name
	}
	return name
}

func cell(l *ir.Local) bool { return l.Boxed && !l.Type.IsAggregate() }

// ltype renders the Java type of a local's storage.
func (e *emitter) ltype(l *ir.Local) string {
	if cell(l) {
		return "Cell"
	}
	return e.jt(l.Type)
}

func (fe *fnEmitter) val(v ir.Value) string {
	switch v := v.(type) {
	case *ir.Local:
		if s, ok := fe.folds.Use(v); ok {
			return s
		}
		if cell(v) {
			return fe.e.cast(v.Type, fe.local(v)+".v")
		}
		return fe.local(v)
	case *ir.Const:
		return fe.e.constant(v)
	case *ir.FuncRef:
		f := fmt.Sprintf("Program.closure(%d, %s)", v.Func.ID, fe.e.fnLit(v.Func, nil))
		if fe.e.susp(v.Type) && !v.Func.MaySuspend {
			return fe.e.adapt(f, len(v.Func.Results))
		}
		return f
	}
	panic(fmt.Sprintf("csharp: operand %T", v))
}

func (e *emitter) constant(c *ir.Const) string {
	if c.Nil {
		if c.Type.U().Kind == ir.KSlice {
			return sliceNil(c.Type)
		}
		return "null"
	}
	if c.Type.U().Kind == ir.KFloat {
		return ir.FloatLiteral(c) + map[bool]string{true: "f", false: "d"}[c.Type.U().FloatBits == 32]
	}
	switch c.Val.Kind() {
	case constant.Bool:
		return c.Val.ExactString()
	case constant.String:
		return csString(constant.StringVal(c.Val))
	case constant.Int:
		if v, ok := constant.Int64Val(c.Val); ok {
			if v == -9223372036854775808 {
				return "long.MinValue"
			}
			return strconv.FormatInt(v, 10) + "L"
		}
		u, _ := constant.Uint64Val(c.Val)
		return fmt.Sprintf("unchecked((long) 0x%xUL)", u)
	}
	panic("csharp: constant " + c.Val.String())
}

// csString renders bytes as a C# string literal with one char per byte.
func csString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "\\u%04x", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func rootType(p *ir.Place) *ir.Type {
	switch r := p.Root.(type) {
	case ir.LocalRoot:
		return r.Local.Type
	case ir.GlobalRoot:
		return r.Global.Type
	case ir.DerefRoot:
		return r.Ptr.IRType().U().Elem
	case ir.SliceRoot:
		return r.Slice.IRType().U().Elem
	case ir.ValueRoot:
		return r.Value.IRType()
	}
	return nil
}

func (fe *fnEmitter) idx(i ir.Value, n string) string {
	if unsigned64(i.IRType()) {
		return fmt.Sprintf("Panics.idxu(%s, %s)", fe.val(i), n)
	}
	return fmt.Sprintf("Panics.idx(%s, %s)", fe.val(i), n)
}

// rootExpr renders the place root; obj reports an Object-typed expression.
func (fe *fnEmitter) rootExpr(p *ir.Place) (string, bool) {
	switch r := p.Root.(type) {
	case ir.LocalRoot:
		if cell(r.Local) {
			return fe.local(r.Local) + ".v", true
		}
		return fe.local(r.Local), false
	case ir.GlobalRoot:
		if r.Global.AddrTaken && !r.Global.Type.IsAggregate() {
			return fe.e.symbol(r.Global.Sym) + ".v", true
		}
		return fe.e.symbol(r.Global.Sym), false
	case ir.DerefRoot:
		if r.Ptr.IRType().U().Elem.IsAggregate() {
			return "Panics.nilchk(" + fe.val(r.Ptr) + ")", false
		}
		return "Panics.nilchk(" + fe.val(r.Ptr) + ").Get()", true
	case ir.SliceRoot:
		fn := "sget"
		if byteElem(rootType(p)) {
			fn = "bget"
		}
		if unsigned64(r.Index.IRType()) {
			fn += "u"
		}
		return fe.e.rc("core.slice.index") + "." + fn + "(" + fe.val(r.Slice) + ", " + fe.val(r.Index) + ")", !byteElem(rootType(p))
	case ir.ValueRoot:
		return fe.val(r.Value), false
	}
	panic("csharp: place root")
}

// typed returns expr converted to a typed expression of type t.
func (fe *fnEmitter) typed(expr string, obj bool, t *ir.Type) string {
	if obj {
		return fe.e.cast(t, expr)
	}
	return expr
}

// walk applies projections except the last n, returning a typed expression
// for the base and its type.
func (fe *fnEmitter) walk(p *ir.Place, drop int) (string, *ir.Type, bool) {
	s, obj := fe.rootExpr(p)
	cur := rootType(p)
	for _, pr := range p.Path[:len(p.Path)-drop] {
		s = fe.typed(s, obj, cur)
		if pr.Index != nil {
			s = s + "[" + fe.idx(pr.Index, strconv.FormatInt(cur.U().Len, 10)) + "]"
			obj = !byteContainer(cur)
			if !obj {
				s = "((long) (" + s + "))"
			}
		} else {
			s = s + "." + fe.e.fieldProp(cur, pr.Field)
			obj = false
		}
		cur = pr.Type
	}
	return s, cur, obj
}

func (fe *fnEmitter) placeRead(p *ir.Place) string {
	s, t, obj := fe.walk(p, 0)
	return fe.typed(s, obj, t)
}

func (fe *fnEmitter) store(p *ir.Place, v string) string {
	t := p.Type
	e := fe.e
	if len(p.Path) == 0 {
		switch r := p.Root.(type) {
		case ir.SliceRoot:
			if !t.IsAggregate() {
				fn := "sset"
				if byteElem(t) {
					fn = "bset"
				}
				if unsigned64(r.Index.IRType()) {
					fn += "u"
				}
				return fmt.Sprintf("%s.%s(%s, %s, %s)", e.rc("core.slice.store"), fn, fe.val(r.Slice), fe.val(r.Index), v)
			}
		case ir.DerefRoot:
			if !t.IsAggregate() {
				return fmt.Sprintf("Panics.nilchk(%s).Set(%s)", fe.val(r.Ptr), v)
			}
		case ir.LocalRoot:
			if cell(r.Local) {
				return fe.local(r.Local) + ".v = " + v
			}
			if t.IsAggregate() && r.Local.Kind == ir.LTemp {
				return fe.local(r.Local) + " = " + v
			}
		case ir.GlobalRoot:
			if r.Global.AddrTaken && !t.IsAggregate() {
				return fe.e.symbol(r.Global.Sym) + ".v = " + v
			}
		}
	}
	if t.IsAggregate() {
		return e.setStmt(t, fe.placeRead(p), v)
	}
	if len(p.Path) == 0 {
		s, _ := fe.rootExpr(p)
		return s + " = " + v
	}
	base, bt, obj := fe.walk(p, 1)
	base = fe.typed(base, obj, bt)
	last := p.Path[len(p.Path)-1]
	if last.Index != nil {
		if byteContainer(bt) {
			v = "unchecked((byte) (" + v + "))"
		}
		return fmt.Sprintf("%s[%s] = %s", base, fe.idx(last.Index, strconv.FormatInt(bt.U().Len, 10)), v)
	}
	return base + "." + fe.e.fieldProp(bt, last.Field) + " = " + v
}

func (fe *fnEmitter) addrOf(p *ir.Place) string {
	t := p.Type
	if len(p.Path) == 0 {
		switch r := p.Root.(type) {
		case ir.DerefRoot:
			return fe.val(r.Ptr)
		case ir.LocalRoot:
			if t.IsAggregate() || r.Local.Boxed {
				return fe.local(r.Local)
			}
		case ir.GlobalRoot:
			return fe.e.symbol(r.Global.Sym)
		}
	}
	if t.IsAggregate() {
		return fe.placeRead(p)
	}
	base, bt, obj := fe.walk(p, 1)
	base = fe.typed(base, obj, bt)
	last := p.Path[len(p.Path)-1]
	cls := fe.e.jt(bt)
	f := fe.e.fieldProp(bt, last.Field)
	return fmt.Sprintf("Program.fieldRef(%s, %q, _o => ((%s) _o).%s, (_o, _v) => ((%s) _o).%s = %s)", base, f, cls, f, cls, f, fe.e.cast(last.Type, "_v"))
}

func (e *emitter) retType(f *ir.Func) string {
	switch len(f.Results) {
	case 0:
		return "void"
	case 1:
		return e.jt(f.Results[0].Type)
	}
	return "object[]"
}

// rawParam reports a parameter received unboxed and moved into a cell by
// BoxParam.
func rawParam(l *ir.Local) bool { return cell(l) && l.Kind != ir.LEnv }

func (fe *fnEmitter) params() string {
	var ps []string
	for _, l := range append(append([]*ir.Local(nil), fe.f.Env...), fe.f.Params...) {
		t := fe.e.ltype(l)
		if rawParam(l) {
			t = fe.e.jt(l.Type)
		}
		ps = append(ps, t+" "+fe.paramName(l))
	}
	return strings.Join(ps, ", ")
}

func (fe *fnEmitter) paramName(l *ir.Local) string {
	if rawParam(l) {
		return "_p_" + fe.e.localName(l)
	}
	return fe.e.localName(l)
}

// asyncType renders the return type of f as an async method.
func (e *emitter) asyncType(f *ir.Func) string {
	if len(f.Results) == 0 {
		return "Async"
	}
	return "Async<" + e.retType(f) + ">"
}

// starter renders the frame-convention overload of an async method, used
// by go, defer, function values and library calls.
func (fe *fnEmitter) starter() string {
	f := fe.f
	args := []string{"_t"}
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		args = append(args, fe.paramName(l))
	}
	n := ""
	if len(f.Results) > 0 {
		n = fmt.Sprintf(", %d", len(f.Results))
	}
	sym := fe.e.symbol(f.Sym)
	return fmt.Sprintf("    static Frame %s(%s) => R.nativeTask(_t => %s(%s)%s);\n\n", sym, fe.params(), sym, strings.Join(args, ", "), n)
}

func (e *emitter) function(f *ir.Func) string {
	if f.MaySuspend && !e.native() {
		e.frameFunction(f)
		return ""
	}
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}, task: f.MaySuspend}
	for i, b := range f.Blocks {
		fe.order[b] = i
	}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if _, ok := in.(*ir.Defer); ok {
				fe.defers = true
			}
		}
	}
	if fe.task {
		e.use("core.task.spawn")
		ps := "GoTask _t"
		if p := fe.params(); p != "" {
			ps += ", " + p
		}
		fe.w("%s    static async %s %s(%s) {\n        Program.checkStack();\n", position(f.Pos), e.asyncType(f), e.symbol(f.Sym), ps)
	} else {
		fe.w("%s    static %s %s(%s) {\n        using var _sourceDepth = Program.enterSource();\n", position(f.Pos), e.retType(f), e.symbol(f.Sym), fe.params())
	}
	isParam := map[*ir.Local]bool{}
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		isParam[l] = !rawParam(l)
	}
	fe.analyze()
	for _, l := range f.Locals {
		if !isParam[l] && !fe.folds.Temp(l) {
			fe.w("        %s %s = %s;\n", e.ltype(l), e.localName(l), e.defaultOf(l))
		}
	}
	if fe.defers {
		fe.w("        var _d = new List<Deferred>();\n        GoPanic _p = null;\n        try {\n")
		fe.structured(3)
	} else {
		fe.structured(2)
	}
	if fe.defers {
		run := "Program.runDefers(_d, _p)"
		if fe.pausingDefer() {
			run = "await R.runDefersAsync(_t, _d, _p)"
		}
		fe.w("        _end: ;\n        } catch (Exception _e) {\n            _p = Program.catchPanic(_e);\n        }\n        %s;\n", run)
		if r := fe.results(); r != "" {
			fe.w("        return %s;\n", r)
		}
	}
	fe.w("    }\n\n")
	if fe.task {
		fe.b.WriteString(fe.starter())
	}
	return structure.Resolve(fe.b.String())
}

// pausingDefer reports whether a deferred call of f may pause.
func (fe *fnEmitter) pausingDefer() bool {
	for _, b := range fe.f.Blocks {
		for _, in := range b.Instrs {
			if d, ok := in.(*ir.Defer); ok && d.Call.Suspends {
				return true
			}
		}
	}
	return false
}

// defaultOf renders a placeholder initial value satisfying Java's definite
// assignment rules; DeclVar assigns the real initial value.
func (e *emitter) defaultOf(l *ir.Local) string {
	if cell(l) {
		return "null"
	}
	switch e.jt(l.Type) {
	case "float":
		return "0.0f"
	case "double":
		return "0.0d"
	case "long":
		return "0L"
	case "bool":
		return "false"
	case "string":
		return `""`
	case "Slice":
		return sliceNil(l.Type)
	}
	return "null"
}

func (fe *fnEmitter) results() string {
	var vs []string
	for _, r := range fe.f.Results {
		v := fe.val(r)
		if r.Boxed && r.Type.IsAggregate() {
			v = fe.e.cloneExpr(r.Type, v)
		}
		vs = append(vs, v)
	}
	switch len(vs) {
	case 0:
		return ""
	case 1:
		return vs[0]
	}
	return "new object[] {" + strings.Join(vs, ", ") + "}"
}

func (fe *fnEmitter) mark(pos token.Pos) {
	if pos.IsValid() {
		fe.w("%s%d\x00", marker, int(pos))
	}
}

var csSyntax = structure.Syntax{
	Indent: "    ", If: "if (%s) {", ElseIf: "} else if (%s) {", Else: "} else {", End: "}",
	Loop: "for (;;) {", While: "while (%s) {", LabelName: "L%d",
	Break: "break;", Continue: "continue;", Goto: "goto %s;", Label: "%s: ;", GotoLoops: true,
	Not: "!%s", And: "%s && %s", Or: "%s || %s", EntryLabel: "_b%d: ;", GotoEntry: "goto _b%d;",
	Resume: func(heads []*ir.Block) string {
		var b strings.Builder
		b.WriteString("switch (_fr.pc) {\n")
		for _, h := range heads {
			fmt.Fprintf(&b, "case %d: goto _b%d;\n", h.ID, h.ID)
		}
		b.WriteString("}")
		return b.String()
	},
}

// structured writes the function body as structured statements; a
// resumable frame first jumps to the block it resumes at.
func (fe *fnEmitter) structured(depth int) {
	body := structure.Build(fe.f, structure.Options{Inline: fe.task, Empty: func(b *ir.Block) bool { return fe.folds.Empty(b, fe.task) }})
	fe.b.WriteString(structure.Print(body, csSyntax, csEmitter{fe}, depth))
}

func (fe *fnEmitter) analyze() {
	structure.Analyze(fe.f, structure.FoldOptions{Inline: fe.task, Order: structure.LeftToRight}, csProbe{fe}, func(fs *structure.Folds) { fe.folds = fs })
}

func (fe *fnEmitter) capture(fn func()) string {
	saved := fe.b
	fe.b = strings.Builder{}
	fn()
	out := fe.b.String()
	fe.b = saved
	return out
}

func markText(pos token.Pos) string {
	if !pos.IsValid() {
		return ""
	}
	return fmt.Sprintf("%s%d\x00", marker, int(pos))
}

// csEmitter prints the target-specific statements of a structured body.
type csEmitter struct{ fe *fnEmitter }

func (x csEmitter) Code(b *ir.Block) string {
	fe := x.fe
	return fe.capture(func() {
		if b.ResumeOf != nil && fe.frame {
			fe.resume(b.ResumeOf)
		}
		for _, in := range b.Instrs {
			if !fe.folds.Folded(in) {
				fe.mark(in.Position())
				fe.instr(in)
			}
		}
	})
}

func (x csEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	return x.fe.capture(func() {
		x.fe.mark(t.Position())
		x.fe.term(t)
	})
}

func (x csEmitter) Pause(p *ir.Pause) string {
	if x.fe.folds.FoldedPause(p) {
		return ""
	}
	return x.fe.capture(func() {
		x.fe.mark(p.Position())
		x.fe.nativePause(p)
	})
}

func (x csEmitter) Cond(v ir.Value) string    { return x.fe.val(v) }
func (x csEmitter) Mark(pos token.Pos) string { return markText(pos) }

// csProbe renders statements for fold analysis.
type csProbe struct{ fe *fnEmitter }

func (x csProbe) Instr(in ir.Instr) string { return x.fe.capture(func() { x.fe.instr(in) }) }
func (x csProbe) Pause(p *ir.Pause) string { return x.fe.capture(func() { x.fe.nativePause(p) }) }
func (x csProbe) Cond(v ir.Value) string   { return x.fe.val(v) }
func (x csProbe) Leaf(b *ir.Block, t ir.Terminator) string {
	return x.fe.capture(func() { x.fe.term(t) })
}
func (x csProbe) Assign(l *ir.Local) (string, string) { return x.fe.assign(l, ""), ";" }

func (fe *fnEmitter) pcVar() string {
	if fe.frame {
		return "_fr.pc"
	}
	return "_pc"
}

func (fe *fnEmitter) term(t ir.Terminator) {
	switch t := t.(type) {
	case *ir.Pause:
		fe.pause(t)
	case *ir.Return:
		switch {
		case fe.frame:
			fe.w("R.ret(_t, _fr); return;\n")
		case fe.defers:
			fe.w("goto _end;\n")
		default:
			if r := fe.results(); r != "" {
				fe.w("return %s;\n", r)
			} else {
				fe.w("return;\n")
			}
		}
	case *ir.Panic:
		fe.w("throw Program.goPanic(%s);\n", fe.val(t.X))
	case *ir.PanicRuntime:
		fe.w("throw Panics.plainPanic(%s);\n", csString(t.Msg))
	case *ir.Unreachable:
		fe.w("throw Panics.fault(\"unreachable\");\n")
	default:
		panic(fmt.Sprintf("csharp: terminator %T", t))
	}
}

// frameFunction emits a suspending function as a resumable frame class and
// a starter binding the arguments.
func (e *emitter) frameFunction(f *ir.Func) {
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}, frame: true}
	for i, b := range f.Blocks {
		fe.order[b] = i
	}
	fe.analyze()
	fe.frame = false
	cls := e.names.Derived("F", e.symbol(f.Sym))
	fe.w("%s    sealed class %s : Frame {\n", position(f.Pos), cls)
	for _, l := range f.Locals {
		if !fe.folds.Temp(l) {
			fe.w("        public %s %s = %s;\n", e.ltype(l), e.localName(l), e.defaultOf(l))
		}
	}
	for _, l := range f.Params {
		if rawParam(l) {
			fe.w("        public %s _p_%s;\n", e.jt(l.Type), e.localName(l))
		}
	}
	fe.frame = true
	var rs []string
	for _, r := range f.Results {
		v := fe.val(r)
		if r.Boxed && r.Type.IsAggregate() {
			v = e.cloneExpr(r.Type, v)
		}
		rs = append(rs, v)
	}
	fe.w("        public override object[] results() {\n            var _fr = this;\n            return new object[] {%s};\n        }\n", strings.Join(rs, ", "))
	fe.w("        public override void step(GoTask _t) {\n            var _fr = this;\n")
	fe.structured(3)
	fe.w("        }\n    }\n\n")
	fe.frame = false
	fe.w("%s    static Frame %s(%s) {\n        %s _fr = new %s();\n", position(f.Pos), e.symbol(f.Sym), fe.params(), cls, cls)
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		if rawParam(l) {
			fe.w("        _fr._p_%s = _p_%s;\n", e.localName(l), e.localName(l))
		} else {
			fe.w("        _fr.%s = %s;\n", e.localName(l), e.localName(l))
		}
	}
	fe.w("        return _fr;\n    }\n\n")
	e.frames.WriteString(structure.Resolve(fe.b.String()))
}

func (fe *fnEmitter) resume(op ir.Instr) {
	e := fe.e
	sep, end := "; ", "\n"
	if fe.task {
		sep, end = ";\n", ""
	}
	switch i := op.(type) {
	case *ir.Call:
		for k, d := range i.Dsts {
			if d != nil {
				fe.w("%s"+sep, fe.assign(d, e.cast(d.Type, fmt.Sprintf("_t.rv[%d]", k))))
			}
		}
	case *ir.Recv:
		if i.Dst != nil {
			fe.w("%s"+sep, fe.assign(i.Dst, e.cast(i.Dst.Type, "_t.rv[0]")))
		}
		if i.Ok != nil {
			fe.w("%s"+sep, fe.assign(i.Ok, "((bool) _t.rv[1])"))
		}
	case *ir.Select:
		fe.w("%s"+sep, fe.assign(i.Index, "((long) _t.rv[0])"))
		for k, c := range i.Cases {
			if c.Send || c.Dst == nil && c.Ok == nil {
				continue
			}
			fe.w("if ((long) _t.rv[0] == %d) {", k)
			if c.Dst != nil {
				fe.w(" %s;", fe.assign(c.Dst, e.cast(c.Dst.Type, "_t.rv[1]")))
			}
			if c.Ok != nil {
				fe.w(" %s;", fe.assign(c.Ok, "((bool) _t.rv[2])"))
			}
			fe.b.WriteString(" }" + strings.TrimPrefix(sep, ";"))
		}
	}
	fe.b.WriteString(end)
}

// assign renders an assignment to a temp or local.
func (fe *fnEmitter) assign(l *ir.Local, v string) string {
	if cell(l) {
		return fe.local(l) + ".v = " + v
	}
	return fe.local(l) + " = " + v
}

func (fe *fnEmitter) pause(p *ir.Pause) {
	e := fe.e
	fe.w("_fr.pc = %d; ", fe.order[p.Next])
	switch i := p.Op.(type) {
	case *ir.Call:
		switch i.Kind {
		case ir.CallExtern:
			args := append([]string{"_t"}, fe.externArgs(i)...)
			fe.w("%s(%s);\n", fe.externSym(i.Extern.Contract), strings.Join(args, ", "))
		default:
			fe.w("R.call(_t, (Frame) (%s));\n", fe.callExpr(i))
		}
	case *ir.Send:
		fe.w("%s.chanSend(_t, %s, %s);\n", e.rc("core.chan.send"), fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		fe.w("%s.chanRecv(_t, %s);\n", e.rc("core.chan.recv"), fe.val(i.Ch))
	case *ir.Select:
		var cs []string
		for _, c := range i.Cases {
			if c.Send {
				cs = append(cs, fmt.Sprintf("R.scase(%s, true, %s)", fe.val(c.Ch), fe.val(c.V)))
			} else {
				cs = append(cs, fmt.Sprintf("R.scase(%s, false, null)", fe.val(c.Ch)))
			}
		}
		e.use("core.select")
		fe.w("R.select(_t, %v%s);\n", i.Default, prefixComma(cs))
	default:
		panic(fmt.Sprintf("csharp: pause on %T", p.Op))
	}
	fe.w("            return;\n")
}

// nativePause emits a pause point of an async method: a generated callee
// is awaited for its results; a runtime primitive either completes at once
// or leaves the task blocked, and then the task waits for the scheduler to
// choose it again. Primitive, function value and method results arrive in
// t.rv.
func (fe *fnEmitter) nativePause(p *ir.Pause) {
	e := fe.e
	const wait = "if (_t.blocked) await R.wait(_t);\n"
	switch i := p.Op.(type) {
	case *ir.Call:
		switch i.Kind {
		case ir.CallExtern:
			args := append([]string{"_t"}, fe.externArgs(i)...)
			fe.w("%s(%s);\n"+wait, fe.externSym(i.Extern.Contract), strings.Join(args, ", "))
		case ir.CallStatic:
			call := "await " + e.symbol(i.Func.Sym) + "(" + strings.Join(append([]string{"_t"}, fe.argList(i)...), ", ") + ")"
			switch len(i.Dsts) {
			case 0:
				fe.w("%s;\n", call)
			case 1:
				fe.w("%s;\n", fe.assign(i.Dsts[0], call))
			default:
				fe.w("{ object[] _r = %s;", call)
				for k, d := range i.Dsts {
					if d != nil {
						fe.w(" %s;", fe.assign(d, e.cast(d.Type, fmt.Sprintf("_r[%d]", k))))
					}
				}
				fe.w(" }\n")
			}
			return
		default:
			fe.w("await R.awaitFrame(_t, (Frame) (%s));\n", fe.callExpr(i))
		}
	case *ir.Send:
		fe.w("%s.chanSend(_t, %s, %s);\n"+wait, e.rc("core.chan.send"), fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		fe.w("%s.chanRecv(_t, %s);\n"+wait, e.rc("core.chan.recv"), fe.val(i.Ch))
	case *ir.Select:
		var cs []string
		for _, c := range i.Cases {
			if c.Send {
				cs = append(cs, fmt.Sprintf("R.scase(%s, true, %s)", fe.val(c.Ch), fe.val(c.V)))
			} else {
				cs = append(cs, fmt.Sprintf("R.scase(%s, false, null)", fe.val(c.Ch)))
			}
		}
		e.use("core.select")
		fe.w("R.select(_t, %v%s);\n"+wait, i.Default, prefixComma(cs))
	default:
		panic(fmt.Sprintf("csharp: pause on %T", p.Op))
	}
	fe.resume(p.Op)
}

func prefixComma(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return ", " + strings.Join(xs, ", ")
}

var binName = map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Rem: "rem",
	ir.And: "and", ir.Or: "or", ir.Xor: "xor", ir.AndNot: "andnot", ir.Shl: "shl", ir.Shr: "shr"}

var binContract = map[ir.BinOpKind]string{ir.Add: "core.integer.add", ir.Sub: "core.integer.sub", ir.Mul: "core.integer.mul",
	ir.Div: "core.integer.div", ir.Rem: "core.integer.rem", ir.And: "core.integer.and", ir.Or: "core.integer.or",
	ir.Xor: "core.integer.xor", ir.AndNot: "core.integer.andnot", ir.Shl: "core.integer.shl", ir.Shr: "core.integer.shr"}

func (fe *fnEmitter) externSym(contract string) string {
	cls := fe.e.rc(contract)
	if s, ok := fe.e.symbols[contract]; ok {
		return s
	}
	parts := strings.Split(contract, ".")
	var b strings.Builder
	for i, p := range parts {
		for j, w := range strings.Split(p, "_") {
			if w == "" {
				continue
			}
			if i == 0 && j == 0 {
				b.WriteString(w)
			} else {
				b.WriteString(strings.ToUpper(w[:1]) + w[1:])
			}
		}
	}
	m := b.String()
	switch contract {
	case "std.context.done":
		m = "stdContextContextDone"
	case "std.context.err":
		m = "stdContextContextErr"
	}
	return cls + "." + m
}

// externArgs renders capability arguments, adapting function slices to the
// resumable form the runtime expects.
func (fe *fnEmitter) externArgs(c *ir.Call) []string {
	var args []string
	for _, a := range c.Args {
		v := fe.val(a)
		if u := a.IRType().U(); u.Kind == ir.KSlice && u.Elem.U().Kind == ir.KFunc && !fe.e.susp(u.Elem) {
			v = fmt.Sprintf("R.adaptSlice(%s, %d)", v, len(u.Elem.U().Results))
			fe.e.use("core.task.spawn")
		}
		args = append(args, v)
	}
	return args
}

// boxArgs renders arguments as an object[] for Fn calls.
func (fe *fnEmitter) argList(c *ir.Call) []string {
	var as []string
	for _, a := range c.Args {
		as = append(as, fe.val(a))
	}
	return as
}

// callExpr renders a call; for value and interface calls the result is
// Object (or Object[]); for static and extern calls it is typed.
func (fe *fnEmitter) callExpr(c *ir.Call) string {
	as := fe.argList(c)
	switch c.Kind {
	case ir.CallStatic:
		return fe.e.symbol(c.Func.Sym) + "(" + strings.Join(as, ", ") + ")"
	case ir.CallValue:
		return "Program.fnchk(" + fe.val(c.Fn) + ").Call(" + strings.Join(as, ", ") + ")"
	case ir.CallInterface:
		return fmt.Sprintf("Program.icall(%s, %q%s)", fe.val(c.Recv), c.Method, prefixComma(as))
	case ir.CallExtern:
		return fe.externSym(c.Extern.Contract) + "(" + strings.Join(fe.externArgs(c), ", ") + ")"
	}
	panic("csharp: call kind")
}

func (fe *fnEmitter) instr(in ir.Instr) {
	e := fe.e
	switch i := in.(type) {
	case *ir.DeclVar:
		init := e.zero(i.L.Type)
		if i.Init != nil {
			init = fe.val(i.Init)
		}
		if cell(i.L) {
			fe.w("%s = new Cell(%s);\n", fe.local(i.L), init)
		} else {
			fe.w("%s = %s;\n", fe.local(i.L), init)
		}
	case *ir.Assign:
		fe.w("%s;\n", fe.assign(i.Dst, fe.val(i.Src)))
	case *ir.Zero:
		fe.w("%s;\n", fe.assign(i.Dst, e.zero(i.Dst.Type)))
	case *ir.Load:
		x := fe.placeRead(i.Place)
		if i.Dst.Type.IsAggregate() {
			x = e.cloneExpr(i.Dst.Type, x)
		}
		fe.w("%s;\n", fe.assign(i.Dst, x))
	case *ir.Store:
		fe.w("%s;\n", fe.store(i.Place, fe.val(i.V)))
	case *ir.AddrOf:
		fe.w("%s;\n", fe.assign(i.Dst, fe.addrOf(i.Place)))
	case *ir.New:
		el := i.Dst.Type.U().Elem
		if el.IsAggregate() {
			fe.w("%s;\n", fe.assign(i.Dst, e.zero(el)))
		} else {
			fe.w("%s;\n", fe.assign(i.Dst, "new Cell("+e.zero(el)+")"))
		}
	case *ir.UnOp:
		x := fe.val(i.X)
		switch i.Op {
		case ir.Not:
			fe.w("%s;\n", fe.assign(i.Dst, "!"+x))
		case ir.Neg:
			if i.Dst.Type.U().Kind == ir.KFloat {
				e.use("core.float.neg")
				fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.floatNeg_f%d(%s)", e.rc("core.float.neg"), i.Dst.Type.U().FloatBits, x)))
				break
			}
			fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.neg_%s(%s)", e.rc("core.integer.neg"), intKind(i.Dst.Type), x)))
		case ir.BitNot:
			fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.not_%s(%s)", e.rc("core.integer.not"), intKind(i.Dst.Type), x)))
		}
	case *ir.BinOp:
		fe.w("%s;\n", fe.assign(i.Dst, fe.binop(i)))
	case *ir.Convert:
		fe.w("%s;\n", fe.assign(i.Dst, fe.convert(i)))
	case *ir.MakeInterface:
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("Box.box(%s, %s)", e.td(i.X.IRType()), fe.val(i.X))))
	case *ir.TypeAssert:
		fe.typeAssert(i)
	case *ir.Call:
		fe.call(i)
	case *ir.MakeClosure:
		var env []string
		for _, l := range i.Env {
			env = append(env, fe.local(l))
		}
		var as []string
		for j, l := range i.Func.Env {
			as = append(as, fmt.Sprintf("(%s) _env[%d]", e.ltype(l), j))
		}
		for j, p := range i.Func.Params {
			as = append(as, e.cast(p.Type, fmt.Sprintf("_a[%d]", j)))
		}
		call := fe.e.symbol(i.Func.Sym) + "(" + strings.Join(as, ", ") + ")"
		body := call
		if len(i.Func.Results) == 0 && !i.Func.MaySuspend {
			body = "{ " + call + "; return null; }"
		}
		v := fmt.Sprintf("Program.closure(%d, GoProgram._envFn(new object[] {%s}, (_env, _a) => %s))", i.Func.ID, strings.Join(env, ", "), body)
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		fe.w("%s;\n", fe.assign(i.Dst, v))
	case *ir.MakeBound:
		v := fmt.Sprintf("Program.bound(%d, %s, %s)", i.Func.ID, e.fnLit(i.Func, nil), fe.val(i.Recv))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		fe.w("%s;\n", fe.assign(i.Dst, v))
	case *ir.MakeIfaceBound:
		v := fmt.Sprintf("Program.ibound(%s, %q)", fe.val(i.Recv), i.Method)
		if e.susp(i.Dst.Type) && !e.p.SuspMethods[i.Method] {
			v = e.adapt(v, len(i.Dst.Type.U().Results))
		}
		fe.w("%s;\n", fe.assign(i.Dst, v))
	case *ir.Len:
		fe.w("%s;\n", fe.assign(i.Dst, fe.lenExpr(i.X, false)))
	case *ir.Cap:
		fe.w("%s;\n", fe.assign(i.Dst, fe.lenExpr(i.X, true)))
	case *ir.MakeSlice:
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.makeSlice(%s, %s, () => %s, %v)", e.rc("core.slice.make"), fe.val(i.Len), fe.val(i.Cap), e.zero(i.Dst.Type.U().Elem), byteContainer(i.Dst.Type))))
	case *ir.MakeMap:
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.makeMap(%s)", e.rc("core.map.make"), e.keyFn(i.Dst.Type.U().Key))))
	case *ir.Append:
		cls := e.rc("core.slice.append")
		cl := e.cloneFn(i.Dst.Type.U().Elem)
		var v string
		switch {
		case i.Spread != nil && i.Spread.IRType().U().Kind == ir.KString:
			v = fmt.Sprintf("%s.appendString(%s, %s)", cls, fe.val(i.S), fe.val(i.Spread))
		case i.Spread != nil:
			v = fmt.Sprintf("%s.appendSlice(%s, %s, %s)", cls, fe.val(i.S), fe.val(i.Spread), cl)
		default:
			var es []string
			for _, x := range i.Elems {
				v := fe.val(x)
				if byteContainer(i.Dst.Type) {
					v = "unchecked((byte) (" + v + "))"
				}
				es = append(es, v)
			}
			if byteContainer(i.Dst.Type) {
				v = fmt.Sprintf("%s.appendBytes(%s, new byte[] {%s})", cls, fe.val(i.S), strings.Join(es, ", "))
			} else {
				v = fmt.Sprintf("%s.append(%s, new object[] {%s}, %s)", cls, fe.val(i.S), strings.Join(es, ", "), cl)
			}
		}
		v = fmt.Sprintf("Slice.zeroAppendGrowth(%s, %s, () => %s)", v, fe.val(i.S), e.zero(i.Dst.Type.U().Elem))
		fe.w("%s;\n", fe.assign(i.Dst, v))
	case *ir.Copy:
		cls := e.rc("core.slice.copy")
		if i.Src.IRType().U().Kind == ir.KString {
			fe.w("%s;\n", fe.assign(i.N, fmt.Sprintf("%s.copyString(%s, %s)", cls, fe.val(i.Dst), fe.val(i.Src))))
		} else {
			fe.w("%s;\n", fe.assign(i.N, fmt.Sprintf("%s.copy(%s, %s, %s)", cls, fe.val(i.Dst), fe.val(i.Src), e.cloneFn(i.Dst.IRType().U().Elem))))
		}
	case *ir.IndexString:
		fn := "sindex"
		if unsigned64(i.I.IRType()) {
			fn = "sindexu"
		}
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.%s(%s, %s)", e.rc("core.string.index"), fn, fe.val(i.S), fe.val(i.I))))
	case *ir.SliceOp:
		fe.w("%s;\n", fe.assign(i.Dst, fe.sliceOp(i)))
	case *ir.MapLookup:
		mt := i.M.IRType().U()
		fe.w("{ object[] _r = %s.mapGet(%s, %s, %s, () => %s); %s;", e.rc("core.map.lookup"), fe.val(i.M), fe.val(i.K), e.keyFn(mt.Key), e.zero(mt.Elem),
			fe.assign(i.Dst, e.cloneExpr(mt.Elem, e.cast(mt.Elem, "_r[0]"))))
		if i.Ok != nil {
			fe.w(" %s;", fe.assign(i.Ok, "((bool) _r[1])"))
		}
		fe.w(" }\n")
	case *ir.MapStore:
		fe.w("%s.mapSet(%s, %s, %s);\n", e.rc("core.map.store"), fe.val(i.M), fe.val(i.K), fe.val(i.V))
	case *ir.MapDelete:
		fe.w("%s.mapDelete(%s, %s, %s);\n", e.rc("core.map.delete"), fe.val(i.M), fe.val(i.K), e.keyFn(i.M.IRType().U().Key))
	case *ir.Clear:
		if i.X.IRType().U().Kind == ir.KMap {
			fe.w("%s.mapClear(%s);\n", e.rc("core.map.clear"), fe.val(i.X))
		} else {
			fe.w("%s.clearSlice(%s, () => %s);\n", e.rc("core.slice.clear"), fe.val(i.X), e.zero(i.X.IRType().U().Elem))
		}
	case *ir.DecodeRune:
		fe.w("{ object[] _r = %s.decodeRune(%s, %s); %s; %s; }\n", e.rc("core.string.decode_rune"), fe.val(i.S), fe.val(i.I),
			fe.assign(i.Rune, "((long) _r[0])"), fe.assign(i.Width, "((long) _r[1])"))
	case *ir.MapIterInit:
		fe.w("%s;\n", fe.assign(i.Iter, fmt.Sprintf("%s.mapIter(%s)", e.rc("core.map.iterate"), fe.val(i.M))))
	case *ir.MapIterNext:
		mt := i.Iter.Type.Elem.U()
		fe.w("%s;", fe.assign(i.Ok, fmt.Sprintf("R.mapNext(%s)", fe.val(i.Iter))))
		if i.Key != nil {
			fe.w(" if (%s) %s;", fe.val(i.Ok), fe.assign(i.Key, e.cloneExpr(mt.Key, e.cast(mt.Key, fe.val(i.Iter)+".k"))))
		}
		if i.Val != nil {
			fe.w(" if (%s) %s;", fe.val(i.Ok), fe.assign(i.Val, e.cloneExpr(mt.Elem, e.cast(mt.Elem, fe.val(i.Iter)+".v"))))
		}
		fe.w("\n")
	case *ir.Print:
		var as []string
		for _, a := range i.Args {
			v := fe.val(a)
			if a.IRType().U().Kind == ir.KFloat {
				v = fmt.Sprintf("Floats.print(%s, %d)", v, a.IRType().U().FloatBits)
			} else if unsigned64(a.IRType()) {
				v = "Ints.u64String(" + v + ")"
			}
			as = append(as, v)
		}
		fe.w("%s.print(%v%s);\n", e.rc("core.print"), i.Newline, prefixComma(as))
	case *ir.Defer:
		fn, fid := fe.deferredFn(i.Call)
		args := fe.argList(i.Call)
		if i.Call.Kind == ir.CallExtern {
			args = fe.externArgs(i.Call)
		}
		target := "_d"
		if fe.frame {
			target = "_fr.defers"
		}
		fe.w("%s.Add(new Deferred(%s, new object[] {%s}, %s, %v));\n", target, fn, strings.Join(args, ", "), fid, i.Call.Suspends && (fe.frame || fe.task))
	case *ir.Recover:
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("Program.recover(%d)", fe.f.ID)))
	case *ir.Rebind:
		switch {
		case cell(i.L):
			fe.w("%s = new Cell(%s.v);\n", fe.local(i.L), fe.local(i.L))
		case i.L.Type.IsAggregate():
			fe.w("%s = %s;\n", fe.local(i.L), e.cloneExpr(i.L.Type, fe.local(i.L)))
		default:
			fe.w(";\n")
		}
	case *ir.BoxParam:
		if rawParam(i.L) {
			pfx := ""
			if fe.frame {
				pfx = "_fr."
			}
			fe.w("%s = new Cell(%s_p_%s);\n", fe.local(i.L), pfx, fe.e.localName(i.L))
		} else {
			fe.w(";\n")
		}
	case *ir.MakeChan:
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.makeChan(%s, () => %s)", e.rc("core.chan.make"), fe.val(i.Size), e.zero(i.Dst.Type.U().Elem))))
	case *ir.Close:
		fe.w("%s.chanClose(%s);\n", e.rc("core.chan.close"), fe.val(i.Ch))
	case *ir.Go:
		e.use("core.task.spawn")
		c := i.Call
		if c.Kind == ir.CallInterface {
			panic("csharp: go statement with interface call")
		}
		if c.Suspends {
			if c.Kind == ir.CallExtern {
				fn, _ := fe.deferredFn(c)
				fe.w("R.spawn((Frame)((%s).Call(new object[] {%s})));\n", fn, strings.Join(fe.externArgs(c), ", "))
				return
			}
			fe.w("R.spawn((Frame) (%s));\n", fe.callExpr(c))
			return
		}
		fn, _ := fe.deferredFn(c)
		args := fe.argList(c)
		if c.Kind == ir.CallExtern {
			args = fe.externArgs(c)
		}
		fe.w("GoProgram._spawnCall(%s, new object[] {%s});\n", fn, strings.Join(args, ", "))
	default:
		panic(fmt.Sprintf("csharp: unsupported instruction %T", in))
	}
}

// deferredFn renders the callee of a deferred or spawned call as a Fn that
// takes the evaluated arguments, and its recover identity.
func (fe *fnEmitter) deferredFn(c *ir.Call) (string, string) {
	e := fe.e
	switch c.Kind {
	case ir.CallStatic:
		return e.fnLit(c.Func, nil), strconv.Itoa(c.Func.ID)
	case ir.CallValue:
		f := fe.val(c.Fn)
		return f, "Program.fid(" + f + ")"
	case ir.CallExtern:
		sig := c.Extern.Sig
		var as []string
		for j, p := range sig.Params {
			as = append(as, e.cast(p, fmt.Sprintf("_a[%d]", j)))
		}
		if c.Suspends {
			call := fe.externSym(c.Extern.Contract) + "(_nativeTask" + prefixComma(as) + ")"
			return "new Fn(_a => R.nativeFrame(_nativeTask => { " + call + "; }))", "null"
		}
		call := fe.externSym(c.Extern.Contract) + "(" + strings.Join(as, ", ") + ")"
		if len(sig.Results) == 0 {
			return "new Fn(_a => { " + call + "; return null; })", "null"
		}
		return "new Fn(_a => " + call + ")", "null"
	}
	panic("csharp: deferred interface call")
}

func (fe *fnEmitter) call(c *ir.Call) {
	e := fe.e
	call := fe.callExpr(c)
	typedResult := c.Kind == ir.CallStatic || c.Kind == ir.CallExtern
	switch len(c.Dsts) {
	case 0:
		fe.w("%s;\n", call)
	case 1:
		d := c.Dsts[0]
		v := call
		if !typedResult {
			v = e.cast(d.Type, call)
		} else if c.Kind == ir.CallExtern {
			v = e.externResult(c, call, 0)
		}
		fe.w("%s;\n", fe.assign(d, v))
	default:
		fe.w("{ object[] _r = (object[]) (%s);", call)
		for k, d := range c.Dsts {
			v := e.cast(d.Type, fmt.Sprintf("_r[%d]", k))
			if c.Kind == ir.CallExtern && d.Type.U().Kind == ir.KFunc && e.susp(d.Type) {
				v = e.adapt(v, len(d.Type.U().Results))
			}
			fe.w(" %s;", fe.assign(d, v))
		}
		fe.w(" }\n")
	}
}

// externResult adapts a single capability result where needed.
func (e *emitter) externResult(c *ir.Call, call string, k int) string {
	d := c.Dsts[k]
	if d.Type.U().Kind == ir.KFunc && e.susp(d.Type) {
		return e.adapt(call, len(d.Type.U().Results))
	}
	return call
}

func (fe *fnEmitter) lenExpr(x ir.Value, isCap bool) string {
	t := x.IRType().U()
	v := fe.val(x)
	switch t.Kind {
	case ir.KString:
		return "(long) " + v + ".Length"
	case ir.KSlice:
		if isCap {
			return "(long) " + v + ".c"
		}
		return "(long) " + v + ".l"
	case ir.KArray:
		return strconv.FormatInt(t.Len, 10) + "L"
	case ir.KPointer:
		return strconv.FormatInt(t.Elem.U().Len, 10) + "L"
	case ir.KMap:
		return fe.e.rc("core.map.len") + ".mapLen(" + v + ")"
	case ir.KChan:
		if isCap {
			return fe.e.rc("core.chan.cap") + ".chanCap(" + v + ")"
		}
		return fe.e.rc("core.chan.len") + ".chanLen(" + v + ")"
	}
	panic("csharp: len of " + t.String())
}

func (fe *fnEmitter) binop(i *ir.BinOp) string {
	e := fe.e
	x, y := fe.val(i.X), fe.val(i.Y)
	t := i.X.IRType()
	if t.U().Kind == ir.KFloat && !i.Op.IsComparison() {
		op := map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Min: "min", ir.Max: "max"}[i.Op]
		e.use("core.float." + op)
		return fmt.Sprintf("%s.float%s_f%d(%s, %s)", e.rc("core.float."+op), strings.ToUpper(op[:1])+op[1:], t.U().FloatBits, x, y)
	}
	u := t.U()
	switch i.Op {
	case ir.Eq, ir.Ne:
		xc, xnil := i.X.(*ir.Const)
		yc, ynil := i.Y.(*ir.Const)
		xnil = xnil && xc.Nil
		ynil = ynil && yc.Nil
		var eq string
		switch {
		case u.Kind == ir.KSlice:
			if ynil {
				eq = x + ".a == null"
			} else {
				eq = y + ".a == null"
			}
		case (xnil || ynil) && u.Kind != ir.KInterface:
			eq = x + " == " + y
		default:
			eq = e.eqExpr(t, x, y)
		}
		if i.Op == ir.Ne {
			eq = "!(" + eq + ")"
		}
		return eq
	case ir.Lt, ir.Le, ir.Gt, ir.Ge:
		switch {
		case u.Kind == ir.KString:
			return fmt.Sprintf("string.CompareOrdinal(%s, %s) %s 0", x, y, i.Op)
		case unsigned64(t):
			return fmt.Sprintf("Ints.cmpu(%s, %s) %s 0", x, y, i.Op)
		}
		return fmt.Sprintf("%s %s %s", x, i.Op, y)
	case ir.Min, ir.Max:
		op := "<"
		if i.Op == ir.Max {
			op = ">"
		}
		var c string
		switch {
		case u.Kind == ir.KString:
			c = fmt.Sprintf("string.CompareOrdinal(%s, %s) %s 0", y, x, op)
		case unsigned64(t):
			c = fmt.Sprintf("Ints.cmpu(%s, %s) %s 0", y, x, op)
		default:
			c = fmt.Sprintf("%s %s %s", y, op, x)
		}
		return fmt.Sprintf("(%s ? %s : %s)", c, y, x)
	}
	if u.Kind == ir.KString {
		e.use("core.string.concat")
		return x + " + " + y
	}
	cls := e.rc(binContract[i.Op])
	if i.Op == ir.Shl || i.Op == ir.Shr {
		cnt := "Ints.count(" + y + ")"
		if unsigned64(i.Y.IRType()) {
			cnt = "Ints.countu(" + y + ")"
		}
		return fmt.Sprintf("%s.%s_%s(%s, %s)", cls, binName[i.Op], intKind(i.Dst.Type), x, cnt)
	}
	return fmt.Sprintf("%s.%s_%s(%s, %s)", cls, binName[i.Op], intKind(i.Dst.Type), x, y)
}

func (fe *fnEmitter) convert(i *ir.Convert) string {
	e := fe.e
	x := fe.val(i.X)
	switch i.Kind {
	case ir.ConvFloat:
		e.use("core.float.convert")
		u, from := i.Dst.Type.U(), i.X.IRType().U()
		cls := e.rc("core.float.convert")
		if u.Kind == ir.KFloat {
			if from.Kind == ir.KFloat {
				return fmt.Sprintf("(%s)%s.round(%s, %d)", e.jt(i.Dst.Type), cls, x, u.FloatBits)
			}
			return fmt.Sprintf("(%s)%s.integerFloat(%s, %t, %d)", e.jt(i.Dst.Type), cls, x, !from.Int.Signed(), u.FloatBits)
		}
		return fmt.Sprintf("%s.floatInteger(%s, %d, %t)", cls, x, u.Int.Bits(), u.Int.Signed())
	case ir.ConvNop, ir.ConvIfaceToIface:
		return x
	case ir.ConvInt:
		return fmt.Sprintf("%s.to_%s(%s)", e.rc("core.integer.convert"), intKind(i.Dst.Type), x)
	case ir.ConvIntToString:
		fn := "fromRune"
		if unsigned64(i.X.IRType()) {
			fn = "fromRuneU"
		}
		return fmt.Sprintf("%s.%s(%s)", e.rc("core.string.from_rune"), fn, x)
	case ir.ConvStringToBytes:
		return fmt.Sprintf("%s.toBytes(%s)", e.rc("core.string.to_bytes"), x)
	case ir.ConvBytesToString:
		return fmt.Sprintf("%s.fromBytes(%s)", e.rc("core.string.from_bytes"), x)
	case ir.ConvStringToRunes:
		return fmt.Sprintf("%s.toRunes(%s)", e.rc("core.string.to_runes"), x)
	case ir.ConvRunesToString:
		return fmt.Sprintf("%s.fromRunes(%s)", e.rc("core.string.from_runes"), x)
	case ir.ConvSliceToArray:
		at := i.Dst.Type.U()
		if byteContainer(at) {
			return fmt.Sprintf("%s.sliceToByteArray(%s, %d)", e.rc("core.slice.to_array"), x, at.Len)
		}
		return fmt.Sprintf("%s.sliceToArray(%s, %d, %s)", e.rc("core.slice.to_array"), x, at.Len, e.cloneFn(at.Elem))
	}
	panic(fmt.Sprintf("csharp: conversion %d", i.Kind))
}

func (fe *fnEmitter) sliceOp(i *ir.SliceOp) string {
	u := false
	opt := func(v ir.Value) string {
		if v == nil {
			return "null"
		}
		if unsigned64(v.IRType()) {
			u = true
		}
		return "(long?) (" + fe.val(v) + ")"
	}
	lo, hi, mx := opt(i.Lo), opt(i.Hi), opt(i.Max)
	x := fe.val(i.X)
	switch i.X.IRType().U().Kind {
	case ir.KString:
		return fmt.Sprintf("%s.sslice(%s, %s, %s, %v)", fe.e.rc("core.string.slice"), x, lo, hi, u)
	case ir.KPointer:
		return fmt.Sprintf("%s.sliceArray(Panics.nilchk(%s), %s, %s, %s, %v)", fe.e.rc("core.slice.slice"), x, lo, hi, mx, u)
	}
	return fmt.Sprintf("%s.reslice(%s, %s, %s, %s, %v)", fe.e.rc("core.slice.slice"), x, lo, hi, mx, u)
}

func (fe *fnEmitter) typeAssert(i *ir.TypeAssert) {
	e := fe.e
	x := fe.val(i.X)
	if i.T.IsInterface() {
		var ids, names []string
		for _, m := range i.T.U().Methods {
			ids = append(ids, strconv.Quote(m.ID))
			names = append(names, strconv.Quote(m.Name))
		}
		test := fmt.Sprintf("(%s != null && Box.implementsAll(%s.t%s) == null)", x, x, prefixComma(ids))
		if i.Ok != nil {
			fe.w("%s; %s;\n", fe.assign(i.Ok, test), fe.assign(i.Dst, fmt.Sprintf("%s ? %s : null", fe.val(i.Ok), x)))
			return
		}
		fe.w("if (!%s) throw Program.assertPanic(%s, %s, %s, %s == null ? null : GoProgram._missing(%s, new string[] {%s}, new string[] {%s}));\n",
			test, x, csString(ir.TypeString(i.X.IRType())), csString(ir.TypeString(i.T)), x, x, strings.Join(ids, ", "), strings.Join(names, ", "))
		fe.w("            %s;\n", fe.assign(i.Dst, x))
		return
	}
	td := e.td(i.T)
	test := fmt.Sprintf("(%s != null && %s.t == %s)", x, x, td)
	val := e.cloneExpr(i.T, e.cast(i.T, x+".v"))
	if i.Ok != nil {
		fe.w("%s; %s;\n", fe.assign(i.Ok, test), fe.assign(i.Dst, fmt.Sprintf("%s ? %s : %s", fe.val(i.Ok), val, e.zero(i.T))))
		return
	}
	fe.w("if (!%s) throw Program.assertPanic(%s, %s, %s, null);\n", test, x, csString(ir.TypeString(i.X.IRType())), csString(ir.TypeString(i.T)))
	fe.w("            %s;\n", fe.assign(i.Dst, val))
}
