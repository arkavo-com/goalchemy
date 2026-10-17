// Package java emits Java 21. Integers of every kind are longs normalized to
// their Go width (unsigned 64-bit values keep their bits); strings are
// binary Strings with one char per byte; structs are generated classes
// updated in place; slices, maps, and function values hold boxed values.
package java

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
	Files     []artifact.File
	Packages  []artifact.PackageFiles
	Contracts []string
}

type emitter struct {
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
	current   string
	locations map[string]string
}

const marker = "\x00@"

const spawnHelper = `    static void spawnCall(Fn f, Object[] a) {
        Fn g = Program.fnchk(f);
        TaskSpawn.spawn(TaskSpawn.sync(() -> {
            g.call(a);
            return new Object[0];
        }));
    }

`

const mainHelpers = `    interface EnvFn {
        Object call(Object[] env, Object[] a);
    }

    static Fn envFn(Object[] env, EnvFn f) {
        return a -> f.call(env, a);
    }

    static String missing(Box x, String[] ids, String[] names) {
        String id = Box.implementsAll(x.t, ids);
        for (int i = 0; i < ids.length; i++) if (ids[i].equals(id)) return names[i];
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

func (e *emitter) mainClass() string {
	if e.p.Library {
		return "Generated"
	}
	return "Main"
}

func (e *emitter) use(c string) {
	if c != "" {
		e.contracts[c] = true
	}
}

// rc names the runtime class implementing a contract and records its use.
func (e *emitter) rc(contract string) string {
	e.use(contract)
	return ClassOf(contract)
}

// ClassOf maps a contract ID to its Java runtime class name.
func ClassOf(contract string) string {
	id := strings.TrimPrefix(contract, "core.")
	var b strings.Builder
	for _, part := range strings.Split(id, ".") {
		for _, w := range strings.Split(part, "_") {
			if w != "" {
				b.WriteString(strings.ToUpper(w[:1]) + w[1:])
			}
		}
	}
	return b.String()
}

var opaque = map[string][2]string{
	"sync.Mutex":      {"StdSyncMutexLock.Mutex", "std.sync.mutex.lock"},
	"sync.WaitGroup":  {"StdSyncWaitgroupAdd.WaitGroup", "std.sync.waitgroup.add"},
	"context.Context": {"StdContextErr.Context", "std.context.err"},
	"crypto.Key":      {"Native.Key", "lib.crypto.close"},
	"http.Exchange":   {"LibHttpExchangeClose.Exchange", "lib.http.exchange_close"},
}

func intKind(t *ir.Type) string { return t.U().Int.String() }

func byteElem(t *ir.Type) bool {
	u := t.U()
	return u.Kind == ir.KInt && u.Int == ir.U8
}

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

func (e *emitter) adapt(f string, n int) string {
	e.use("core.task.spawn")
	return fmt.Sprintf("TaskSpawn.adapt(%s, %d)", f, n)
}

// jt renders the Java type of an IR type.
func (e *emitter) jt(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "boolean"
	case ir.KFloat:
		if u.FloatBits == 32 {
			return "float"
		}
		return "double"
	case ir.KInt:
		return "long"
	case ir.KString:
		return "String"
	case ir.KStruct:
		return e.class(u)
	case ir.KArray:
		if byteContainer(u) {
			return "byte[]"
		}
		return "Object[]"
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
		return "ChanMake.Chan"
	case ir.KOpaque:
		o := opaque[u.Name]
		e.use(o[1])
		return o[0]
	case ir.KMapIter:
		return "GoMap.Iter"
	}
	panic("java: type " + t.String())
}

// cast converts an Object-typed expression to t's Java type.
func (e *emitter) cast(t *ir.Type, x string) string {
	switch e.jt(t) {
	case "float":
		return "((Float) (" + x + ")).floatValue()"
	case "double":
		return "((Double) (" + x + ")).doubleValue()"
	case "long":
		return "((Long) (" + x + "))"
	case "boolean":
		return "((Boolean) (" + x + "))"
	}
	return "((" + e.jt(t) + ") (" + x + "))"
}

func (e *emitter) class(t *ir.Type) string {
	u := t.U()
	if n, ok := e.classes[u]; ok {
		return n
	}
	n := e.names.Type(u, "S$")
	e.classes[u] = n
	e.needHelpers(u)
	return n
}

func (e *emitter) fieldProp(t *ir.Type, i int) string {
	return e.names.Field(t, i, "f$")
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
		switch t.Kind {
		case ir.KStruct:
			e.structClass(t)
		case ir.KArray:
			e.arrayHelpers(t)
		}
	}
}

func (e *emitter) structClass(t *ir.Type) {
	n := e.class(t)
	var b strings.Builder
	fmt.Fprintf(&b, "    static final class %s {\n", n)
	for i, f := range t.Fields {
		fmt.Fprintf(&b, "        %s %s;\n", e.jt(f.Type), e.fieldProp(t, i))
	}
	fmt.Fprintf(&b, "        %s() {\n", n)
	for i, f := range t.Fields {
		fmt.Fprintf(&b, "            %s = %s;\n", e.fieldProp(t, i), e.zero(f.Type))
	}
	b.WriteString("        }\n")
	fmt.Fprintf(&b, "        %s $clone() {\n            %s r = new %s();\n            r.$set(this);\n            return r;\n        }\n", n, n, n)
	fmt.Fprintf(&b, "        void $set(%s o) {\n", n)
	for i, f := range t.Fields {
		p := e.fieldProp(t, i)
		if f.Type.IsAggregate() {
			fmt.Fprintf(&b, "            %s;\n", e.setStmt(f.Type, "this."+p, "o."+p))
		} else {
			fmt.Fprintf(&b, "            this.%s = o.%s;\n", p, p)
		}
	}
	b.WriteString("        }\n")
	fmt.Fprintf(&b, "        boolean $eq(%s o) {\n            return true", n)
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		p := e.fieldProp(t, i)
		fmt.Fprintf(&b, " && %s", e.eqExpr(f.Type, "this."+p, "o."+p))
	}
	b.WriteString(";\n        }\n")
	b.WriteString("        Object $key() {\n            return java.util.Arrays.asList(new Object[] {")
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
		fmt.Fprintf(&e.helperOut, "    static byte[] %s() { return new byte[%d]; }\n", e.names.Type(t, "zero_"), t.Len)
		fmt.Fprintf(&e.helperOut, "    static byte[] %s(byte[] s) { return s.clone(); }\n", e.names.Type(t, "clone_"))
		fmt.Fprintf(&e.helperOut, "    static void %s(byte[] d, byte[] s) { System.arraycopy(s, 0, d, 0, %d); }\n", e.names.Type(t, "set_"), t.Len)
		fmt.Fprintf(&e.helperOut, "    static boolean %s(byte[] x, byte[] y) { return java.util.Arrays.equals(x, y); }\n", e.names.Type(t, "eq_"))
		fmt.Fprintf(&e.helperOut, "    static Object %s(byte[] x) { return new String(x, java.nio.charset.StandardCharsets.ISO_8859_1); }\n\n", e.names.Type(t, "key_"))
		return
	}
	el := t.Elem
	var b strings.Builder
	fmt.Fprintf(&b, "    static Object[] %s() {\n        Object[] a = new Object[%d];\n        for (int i = 0; i < %d; i++) a[i] = %s;\n        return a;\n    }\n", e.names.Type(t, "zero_"), t.Len, t.Len, e.zero(el))
	fmt.Fprintf(&b, "    static Object[] %s(Object[] s) {\n        Object[] a = new Object[%d];\n        for (int i = 0; i < %d; i++) a[i] = %s;\n        return a;\n    }\n", e.names.Type(t, "clone_"), t.Len, t.Len, e.cloneExpr(el, e.cast(el, "s[i]")))
	if el.IsAggregate() {
		fmt.Fprintf(&b, "    static void %s(Object[] d, Object[] s) {\n        for (int i = 0; i < %d; i++) %s;\n    }\n", e.names.Type(t, "set_"), t.Len, e.setStmt(el, e.cast(el, "d[i]"), e.cast(el, "s[i]")))
	} else {
		fmt.Fprintf(&b, "    static void %s(Object[] d, Object[] s) {\n        System.arraycopy(s, 0, d, 0, %d);\n    }\n", e.names.Type(t, "set_"), t.Len)
	}
	fmt.Fprintf(&b, "    static boolean %s(Object[] x, Object[] y) {\n        for (int i = 0; i < %d; i++) if (!(%s)) return false;\n        return true;\n    }\n", e.names.Type(t, "eq_"), t.Len, e.eqExpr(el, e.cast(el, "x[i]"), e.cast(el, "y[i]")))
	fmt.Fprintf(&b, "    static Object %s(Object[] x) {\n        Object[] k = new Object[%d];\n        for (int i = 0; i < %d; i++) k[i] = %s;\n        return java.util.Arrays.asList(k);\n    }\n\n", e.names.Type(t, "key_"), t.Len, t.Len, e.keyExpr(el, e.cast(el, "x[i]")))
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
		return x + ".$clone()"
	case ir.KOpaque:
		if u.OpaqueRef {
			return x
		}
		return x + ".$clone()"
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
	return "v -> " + e.cloneExpr(t, e.cast(t, "v"))
}

func (e *emitter) setStmt(t *ir.Type, dst, src string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KOpaque:
		if u.Kind == ir.KStruct {
			e.class(u)
		}
		return dst + ".$set(" + src + ")"
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
		return a + ".$eq(" + b + ")"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s, %s)", e.names.Type(u, "eq_"), a, b)
	case ir.KInterface:
		return fmt.Sprintf("Box.ifaceEq(%s, %s)", a, b)
	case ir.KString:
		return a + ".equals(" + b + ")"
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
		return x + ".$key()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s)", e.names.Type(u, "key_"), x)
	case ir.KInterface:
		return fmt.Sprintf("Box.ifaceKey(%s)", x)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("Panics.unhashableKey(%q)", ir.TypeString(t))
	case ir.KPointer, ir.KChan, ir.KOpaque:
		return "(Object) (" + x + ")"
	}
	return "(Object) (" + x + ")"
}

func (e *emitter) keyFn(t *ir.Type) string {
	switch t.U().Kind {
	case ir.KFloat, ir.KStruct, ir.KArray, ir.KInterface, ir.KPointer, ir.KChan, ir.KOpaque:
		return "k -> " + e.keyExpr(t, e.cast(t, "k"))
	}
	return "GoMap::identityKey"
}

func (e *emitter) td(t *ir.Type) string {
	if n, ok := e.tds[t]; ok {
		return n
	}
	n := e.names.Type(t, "TD$")
	e.tds[t] = n
	return n
}

// fnLit renders a Fn lambda calling f with unboxed arguments; clone, when
// set, clones the receiver.
func (e *emitter) fnLit(f *ir.Func, cloneRecv *ir.Type) string {
	var as []string
	for i, p := range f.Params {
		a := e.cast(p.Type, fmt.Sprintf("a[%d]", i))
		if i == 0 && cloneRecv != nil {
			a = e.cloneExpr(cloneRecv, a)
		}
		as = append(as, a)
	}
	call := e.symbol(f.Sym) + "(" + strings.Join(as, ", ") + ")"
	if len(f.Results) == 0 && !f.MaySuspend {
		return "(Fn) a -> { " + call + "; return null; }"
	}
	return "(Fn) a -> " + call
}

func (e *emitter) typeDescs(b *bytes.Buffer) {
	var ts []*ir.Type
	for t := range e.tds {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for _, t := range ts {
		if t.Kind == ir.KString {
			fmt.Fprintf(b, "        %s = Program.STRING_TYPE;\n", e.tds[t])
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
		eq := "(a, b) -> " + e.eqExpr(t, e.cast(t, "a"), e.cast(t, "b"))
		key := "a -> " + e.keyExpr(t, e.cast(t, "a"))
		comparable := "true"
		if !t.Comparable() {
			eq = fmt.Sprintf("(a, b) -> Panics.uncomparableEq(%q)", name)
			key = fmt.Sprintf("a -> Panics.unhashableKey(%q)", name)
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
		fmt.Fprintf(b, "        %s = new TypeDesc(%s, %q, %s, %s, TypeDesc.methods(%s), %s, %s);\n",
			e.tds[t], javaString(name), u.Kind.String(), eq, key, strings.Join(ms, ", "), basic, comparable)
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
	folds  *structure.Folds
}

func (e *emitter) localName(l *ir.Local) string {
	return e.names.Local(l, "$")
}

func (e *emitter) symbol(sym string) string {
	name := e.names.Symbol(sym)
	location, ok := e.locations[sym]
	if !ok || location == e.current {
		return name
	}
	return location + "." + name
}

func (fe *fnEmitter) w(format string, args ...any) { fmt.Fprintf(&fe.b, format, args...) }

func (fe *fnEmitter) local(l *ir.Local) string {
	name := fe.e.localName(l)
	if n, ok := fe.folds.Name(l); ok {
		name = n
	}
	if fe.frame {
		return "fr." + name
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
	panic(fmt.Sprintf("java: operand %T", v))
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
		return javaString(constant.StringVal(c.Val))
	case constant.Int:
		if v, ok := constant.Int64Val(c.Val); ok {
			if v == -9223372036854775808 {
				return "Long.MIN_VALUE"
			}
			return strconv.FormatInt(v, 10) + "L"
		}
		u, _ := constant.Uint64Val(c.Val)
		return fmt.Sprintf("0x%xL", u)
	}
	panic("java: constant " + c.Val.String())
}

// javaString renders bytes as a Java string literal with one char per byte.
func javaString(s string) string {
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
			fmt.Fprintf(&b, "\\%03o", c)
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
		return "Panics.nilchk(" + fe.val(r.Ptr) + ").get()", true
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
	panic("java: place root")
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
				s = "(" + s + " & 255L)"
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
				return fmt.Sprintf("Panics.nilchk(%s).set(%s)", fe.val(r.Ptr), v)
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
			v = "(byte) (" + v + ")"
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
	return fmt.Sprintf("Program.fieldRef(%s, %q, o -> ((%s) o).%s, (o, v) -> ((%s) o).%s = %s)", base, f, cls, f, cls, f, fe.e.cast(last.Type, "v"))
}

func (e *emitter) retType(f *ir.Func) string {
	switch len(f.Results) {
	case 0:
		return "void"
	case 1:
		return e.jt(f.Results[0].Type)
	}
	return "Object[]"
}

// rawParam reports a parameter received unboxed and moved into a cell by
// BoxParam.
func rawParam(l *ir.Local) bool { return cell(l) && l.Kind != ir.LEnv }

func (fe *fnEmitter) params() string {
	var ps []string
	for _, l := range append(append([]*ir.Local(nil), fe.f.Env...), fe.f.Params...) {
		name := fe.e.localName(l)
		t := fe.e.ltype(l)
		if rawParam(l) {
			name = "p$" + name
			t = fe.e.jt(l.Type)
		}
		ps = append(ps, t+" "+name)
	}
	return strings.Join(ps, ", ")
}

func (e *emitter) function(f *ir.Func) string {
	if f.MaySuspend {
		e.frameFunction(f)
		return ""
	}
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}}
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
	fe.w("%s    static %s %s(%s) {\n", position(f.Pos), e.retType(f), e.names.Symbol(f.Sym), fe.params())
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
		fe.w("        java.util.ArrayList<Program.Deferred> $d = new java.util.ArrayList<>();\n        GoPanic $p = null;\n        try {\n        $body: {\n")
		fe.structured(3)
		fe.w("        }\n")
	} else {
		fe.structured(2)
	}
	if fe.defers {
		fe.w("        } catch (Throwable $e) {\n            $p = Program.catchPanic($e);\n        }\n        Program.runDefers($d, $p);\n")
		if r := fe.results(); r != "" {
			fe.w("        return %s;\n", r)
		}
	}
	fe.w("    }\n\n")
	return structure.Resolve(fe.b.String())
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
	case "boolean":
		return "false"
	case "String":
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
	return "new Object[] {" + strings.Join(vs, ", ") + "}"
}

func (fe *fnEmitter) mark(pos token.Pos) {
	if pos.IsValid() {
		fe.w("%s%d\x00", marker, int(pos))
	}
}

var javaSyntax = structure.Syntax{
	Indent: "    ", If: "if (%s) {", ElseIf: "} else if (%s) {", Else: "} else {", End: "}",
	Loop: "for (;;) {", While: "while (%s) {", Block: "%s: {", LoopLabel: "%s: ", LabelName: "$L%d",
	Break: "break;", BreakTo: "break %s;", Continue: "continue;", ContinueTo: "continue %s;",
	Not: "!%s", And: "%s && %s", Or: "%s || %s", Fallthrough: true,
	CaseOpen: "case %d:", Default: []string{`default: throw Panics.fault("bad block");`}, DispatchClose: []string{"}"},
}

// structured writes the function body as structured statements, with a
// dispatch over the blocks a resumable frame resumes at.
func (fe *fnEmitter) structured(depth int) {
	body := structure.Build(fe.f, structure.Options{Empty: func(b *ir.Block) bool { return fe.folds.Empty(b, false) }})
	syn := javaSyntax
	syn.DispatchOpen = []string{"%sfor (;;) switch (" + fe.pcVar() + ") {"}
	if !fe.frame {
		syn.DispatchOpen = []string{"int $pc = 0;", syn.DispatchOpen[0]}
	}
	syn.SetPC = fe.pcVar() + " = %d;"
	fe.b.WriteString(structure.Print(body, syn, javaEmitter{fe}, depth))
}

func (fe *fnEmitter) analyze() {
	structure.Analyze(fe.f, structure.FoldOptions{Order: structure.LeftToRight}, javaProbe{fe}, func(fs *structure.Folds) { fe.folds = fs })
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

// javaEmitter prints the target-specific statements of a structured body.
type javaEmitter struct{ fe *fnEmitter }

func (x javaEmitter) Code(b *ir.Block) string {
	fe := x.fe
	return fe.capture(func() {
		if b.ResumeOf != nil {
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

func (x javaEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	return x.fe.capture(func() {
		x.fe.mark(t.Position())
		x.fe.term(t)
	})
}

func (x javaEmitter) Pause(*ir.Pause) string    { panic("java: inline pause") }
func (x javaEmitter) Cond(v ir.Value) string    { return x.fe.val(v) }
func (x javaEmitter) Mark(pos token.Pos) string { return markText(pos) }

// javaProbe renders statements for fold analysis.
type javaProbe struct{ fe *fnEmitter }

func (x javaProbe) Instr(in ir.Instr) string { return x.fe.capture(func() { x.fe.instr(in) }) }
func (x javaProbe) Pause(*ir.Pause) string   { panic("java: inline pause") }
func (x javaProbe) Cond(v ir.Value) string   { return x.fe.val(v) }
func (x javaProbe) Leaf(b *ir.Block, t ir.Terminator) string {
	return x.fe.capture(func() { x.fe.term(t) })
}
func (x javaProbe) Assign(l *ir.Local) (string, string) { return x.fe.assign(l, ""), ";" }

func (fe *fnEmitter) pcVar() string {
	if fe.frame {
		return "fr.pc"
	}
	return "$pc"
}

func (fe *fnEmitter) term(t ir.Terminator) {
	switch t := t.(type) {
	case *ir.Pause:
		fe.pause(t)
	case *ir.Return:
		switch {
		case fe.frame:
			fe.w("TaskSpawn.ret(t, fr); return;\n")
		case fe.defers:
			fe.w("break $body;\n")
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
		fe.w("throw Panics.plainPanic(%s);\n", javaString(t.Msg))
	case *ir.Unreachable:
		fe.w("throw Panics.fault(\"unreachable\");\n")
	default:
		panic(fmt.Sprintf("java: terminator %T", t))
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
	cls := "F$" + e.names.Symbol(f.Sym)
	fe.w("%s    static final class %s extends TaskSpawn.Frame {\n", position(f.Pos), cls)
	for _, l := range f.Locals {
		if !fe.folds.Temp(l) {
			fe.w("        %s %s = %s;\n", e.ltype(l), e.localName(l), e.defaultOf(l))
		}
	}
	for _, l := range f.Params {
		if rawParam(l) {
			fe.w("        %s p$%s;\n", e.jt(l.Type), e.localName(l))
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
	fe.w("        public Object[] results() {\n            final %s fr = this;\n            return new Object[] {%s};\n        }\n", cls, strings.Join(rs, ", "))
	fe.w("        public void step(TaskSpawn.Task t) {\n            final %s fr = this;\n", cls)
	fe.structured(3)
	fe.w("        }\n    }\n\n")
	fe.frame = false
	fe.w("%s    static TaskSpawn.Frame %s(%s) {\n        %s fr = new %s();\n", position(f.Pos), e.names.Symbol(f.Sym), fe.params(), cls, cls)
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		if rawParam(l) {
			fe.w("        fr.p$%s = p$%s;\n", e.localName(l), e.localName(l))
		} else {
			fe.w("        fr.%s = %s;\n", e.localName(l), e.localName(l))
		}
	}
	fe.w("        return fr;\n    }\n\n")
	e.frames.WriteString(structure.Resolve(fe.b.String()))
}

func (fe *fnEmitter) resume(op ir.Instr) {
	e := fe.e
	switch i := op.(type) {
	case *ir.Call:
		for k, d := range i.Dsts {
			if d != nil {
				fe.w("%s; ", fe.assign(d, e.cast(d.Type, fmt.Sprintf("t.rv[%d]", k))))
			}
		}
	case *ir.Recv:
		if i.Dst != nil {
			fe.w("%s; ", fe.assign(i.Dst, e.cast(i.Dst.Type, "t.rv[0]")))
		}
		if i.Ok != nil {
			fe.w("%s; ", fe.assign(i.Ok, "((Boolean) t.rv[1])"))
		}
	case *ir.Select:
		fe.w("%s; ", fe.assign(i.Index, "((Long) t.rv[0])"))
		for k, c := range i.Cases {
			if c.Send || c.Dst == nil && c.Ok == nil {
				continue
			}
			fe.w("if ((Long) t.rv[0] == %d) {", k)
			if c.Dst != nil {
				fe.w(" %s;", fe.assign(c.Dst, e.cast(c.Dst.Type, "t.rv[1]")))
			}
			if c.Ok != nil {
				fe.w(" %s;", fe.assign(c.Ok, "((Boolean) t.rv[2])"))
			}
			fe.w(" } ")
		}
	}
	fe.w("\n")
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
	fe.w("fr.pc = %d; ", fe.order[p.Next])
	switch i := p.Op.(type) {
	case *ir.Call:
		switch i.Kind {
		case ir.CallExtern:
			args := append([]string{"t"}, fe.externArgs(i)...)
			fe.w("%s(%s);\n", fe.externSym(i.Extern.Contract), strings.Join(args, ", "))
		default:
			fe.w("TaskSpawn.call(t, (TaskSpawn.Frame) (%s));\n", fe.callExpr(i))
		}
	case *ir.Send:
		fe.w("%s.chanSend(t, %s, %s);\n", e.rc("core.chan.send"), fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		fe.w("%s.chanRecv(t, %s);\n", e.rc("core.chan.recv"), fe.val(i.Ch))
	case *ir.Select:
		var cs []string
		for _, c := range i.Cases {
			if c.Send {
				cs = append(cs, fmt.Sprintf("Select.scase(%s, true, %s)", fe.val(c.Ch), fe.val(c.V)))
			} else {
				cs = append(cs, fmt.Sprintf("Select.scase(%s, false, null)", fe.val(c.Ch)))
			}
		}
		e.use("core.select")
		fe.w("Select.select(t, %v%s);\n", i.Default, prefixComma(cs))
	default:
		panic(fmt.Sprintf("java: pause on %T", p.Op))
	}
	fe.w("            return;\n")
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
			v = fmt.Sprintf("TaskSpawn.adaptSlice(%s, %d)", v, len(u.Elem.U().Results))
			fe.e.use("core.task.spawn")
		}
		args = append(args, v)
	}
	return args
}

// boxArgs renders arguments as an Object[] for Fn calls.
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
		return "Program.fnchk(" + fe.val(c.Fn) + ").call(" + strings.Join(as, ", ") + ")"
	case ir.CallInterface:
		return fmt.Sprintf("Program.icall(%s, %q%s)", fe.val(c.Recv), c.Method, prefixComma(as))
	case ir.CallExtern:
		return fe.externSym(c.Extern.Contract) + "(" + strings.Join(fe.externArgs(c), ", ") + ")"
	}
	panic("java: call kind")
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
			as = append(as, fmt.Sprintf("(%s) env[%d]", e.ltype(l), j))
		}
		for j, p := range i.Func.Params {
			as = append(as, e.cast(p.Type, fmt.Sprintf("a[%d]", j)))
		}
		call := fe.e.symbol(i.Func.Sym) + "(" + strings.Join(as, ", ") + ")"
		body := call
		if len(i.Func.Results) == 0 && !i.Func.MaySuspend {
			body = "{ " + call + "; return null; }"
		}
		v := fmt.Sprintf("Program.closure(%d, %s.envFn(new Object[] {%s}, (env, a) -> %s))", i.Func.ID, supportClass, strings.Join(env, ", "), body)
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
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.makeSlice(%s, %s, () -> %s, %v)", e.rc("core.slice.make"), fe.val(i.Len), fe.val(i.Cap), e.zero(i.Dst.Type.U().Elem), byteContainer(i.Dst.Type))))
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
					v = "(byte) (" + v + ")"
				}
				es = append(es, v)
			}
			if byteContainer(i.Dst.Type) {
				v = fmt.Sprintf("%s.appendBytes(%s, new byte[] {%s})", cls, fe.val(i.S), strings.Join(es, ", "))
			} else {
				v = fmt.Sprintf("%s.append(%s, new Object[] {%s}, %s)", cls, fe.val(i.S), strings.Join(es, ", "), cl)
			}
		}
		v = fmt.Sprintf("Slice.zeroAppendGrowth(%s, %s, () -> %s)", v, fe.val(i.S), e.zero(i.Dst.Type.U().Elem))
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
		fe.w("{ Object[] $r = %s.mapGet(%s, %s, %s, () -> %s); %s;", e.rc("core.map.lookup"), fe.val(i.M), fe.val(i.K), e.keyFn(mt.Key), e.zero(mt.Elem),
			fe.assign(i.Dst, e.cloneExpr(mt.Elem, e.cast(mt.Elem, "$r[0]"))))
		if i.Ok != nil {
			fe.w(" %s;", fe.assign(i.Ok, "((Boolean) $r[1])"))
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
			fe.w("%s.clearSlice(%s, () -> %s);\n", e.rc("core.slice.clear"), fe.val(i.X), e.zero(i.X.IRType().U().Elem))
		}
	case *ir.DecodeRune:
		fe.w("{ Object[] $r = %s.decodeRune(%s, %s); %s; %s; }\n", e.rc("core.string.decode_rune"), fe.val(i.S), fe.val(i.I),
			fe.assign(i.Rune, "((Long) $r[0])"), fe.assign(i.Width, "((Long) $r[1])"))
	case *ir.MapIterInit:
		fe.w("%s;\n", fe.assign(i.Iter, fmt.Sprintf("%s.mapIter(%s)", e.rc("core.map.iterate"), fe.val(i.M))))
	case *ir.MapIterNext:
		mt := i.Iter.Type.Elem.U()
		fe.w("%s;", fe.assign(i.Ok, fmt.Sprintf("MapIterate.mapNext(%s)", fe.val(i.Iter))))
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
				v = "Long.toUnsignedString(" + v + ")"
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
		target := "$d"
		if fe.frame {
			target = "fr.defers"
		}
		fe.w("%s.add(new Program.Deferred(%s, new Object[] {%s}, %s, %v));\n", target, fn, strings.Join(args, ", "), fid, i.Call.Suspends && fe.frame)
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
				pfx = "fr."
			}
			fe.w("%s = new Cell(%sp$%s);\n", fe.local(i.L), pfx, fe.e.localName(i.L))
		} else {
			fe.w(";\n")
		}
	case *ir.MakeChan:
		fe.w("%s;\n", fe.assign(i.Dst, fmt.Sprintf("%s.makeChan(%s, () -> %s)", e.rc("core.chan.make"), fe.val(i.Size), e.zero(i.Dst.Type.U().Elem))))
	case *ir.Close:
		fe.w("%s.chanClose(%s);\n", e.rc("core.chan.close"), fe.val(i.Ch))
	case *ir.Go:
		e.use("core.task.spawn")
		c := i.Call
		if c.Kind == ir.CallInterface {
			panic("java: go statement with interface call")
		}
		if c.Suspends {
			if c.Kind == ir.CallExtern {
				fn, _ := fe.deferredFn(c)
				fe.w("TaskSpawn.spawn((TaskSpawn.Frame) ((%s).call(new Object[] {%s})));\n", fn, strings.Join(fe.externArgs(c), ", "))
				return
			}
			fe.w("TaskSpawn.spawn((TaskSpawn.Frame) (%s));\n", fe.callExpr(c))
			return
		}
		fn, _ := fe.deferredFn(c)
		args := fe.argList(c)
		if c.Kind == ir.CallExtern {
			args = fe.externArgs(c)
		}
		fe.w("%s.spawnCall(%s, new Object[] {%s});\n", supportClass, fn, strings.Join(args, ", "))
	default:
		panic(fmt.Sprintf("java: unsupported instruction %T", in))
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
			as = append(as, e.cast(p, fmt.Sprintf("a[%d]", j)))
		}
		if c.Suspends {
			call := fe.externSym(c.Extern.Contract) + "($nativeTask" + prefixComma(as) + ")"
			return "(Fn) a -> TaskSpawn.nativeFrame($nativeTask -> { " + call + "; })", "null"
		}
		call := fe.externSym(c.Extern.Contract) + "(" + strings.Join(as, ", ") + ")"
		if len(sig.Results) == 0 {
			return "(Fn) a -> { " + call + "; return null; }", "null"
		}
		return "(Fn) a -> " + call, "null"
	}
	panic("java: deferred interface call")
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
		fe.w("{ Object[] $r = (Object[]) (%s);", call)
		for k, d := range c.Dsts {
			v := e.cast(d.Type, fmt.Sprintf("$r[%d]", k))
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
		return "(long) " + v + ".length()"
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
	panic("java: len of " + t.String())
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
			return fmt.Sprintf("%s.compareTo(%s) %s 0", x, y, i.Op)
		case unsigned64(t):
			return fmt.Sprintf("Long.compareUnsigned(%s, %s) %s 0", x, y, i.Op)
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
			c = fmt.Sprintf("%s.compareTo(%s) %s 0", y, x, op)
		case unsigned64(t):
			c = fmt.Sprintf("Long.compareUnsigned(%s, %s) %s 0", y, x, op)
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
	panic(fmt.Sprintf("java: conversion %d", i.Kind))
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
		return "(Long) (" + fe.val(v) + ")"
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
		fe.w("if (!%s) throw Program.assertPanic(%s, %s, %s, %s == null ? null : %s.missing(%s, new String[] {%s}, new String[] {%s}));\n",
			test, x, javaString(ir.TypeString(i.X.IRType())), javaString(ir.TypeString(i.T)), x, supportClass, x, strings.Join(ids, ", "), strings.Join(names, ", "))
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
	fe.w("if (!%s) throw Program.assertPanic(%s, %s, %s, null);\n", test, x, javaString(ir.TypeString(i.X.IRType())), javaString(ir.TypeString(i.T)))
	fe.w("            %s;\n", fe.assign(i.Dst, val))
}
