// Package py emits Python 3.10+. Go value semantics are preserved with
// explicit copies, stable struct storage objects, cells for boxed scalars,
// integers normalized to their Go widths, and bytes for Go strings.
package py

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
	current   string
	locations map[string]string
	imports   map[string]map[string]bool
}

const marker = "\x00@"

// resolveMarkers strips position markers, recording the source line of
// each generated line.
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

var opaqueContracts = map[string]string{
	"sync.Mutex":      "std.sync.mutex.lock",
	"sync.WaitGroup":  "std.sync.waitgroup.add",
	"context.Context": "std.context.err",
}

func byteElem(t *ir.Type) bool { u := t.U(); return u.Kind == ir.KInt && u.Int == ir.U8 }

func byteStorage(t *ir.Type) bool {
	u := t.U()
	return (u.Kind == ir.KSlice || u.Kind == ir.KArray) && byteElem(u.Elem)
}

func intKind(t *ir.Type) string { return t.U().Int.String() }

func (e *emitter) susp(t *ir.Type) bool { return e.p.SuspTypes[t.U()] }

func (e *emitter) native() bool { return e.p.PauseModel == "native" }

func (e *emitter) adapt(f string, n int) string {
	e.use("core.task.spawn")
	return fmt.Sprintf("rt.adapt(%s, %d)", f, n)
}

// ---- type helpers ----

func (e *emitter) class(t *ir.Type) string {
	u := t.U()
	if n, ok := e.classes[u]; ok {
		return n
	}
	n := e.names.Type(u, "struct_")
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
	var props []string
	for i := range t.Fields {
		props = append(props, strconv.Quote(e.fieldProp(t, i)))
	}
	props = append(props, `"__weakref__"`)
	fmt.Fprintf(&b, "class %s:\n    __slots__ = (%s,)\n\n    def __init__(self):\n", n, strings.Join(props, ", "))
	if len(t.Fields) == 0 {
		b.WriteString("        pass\n")
	}
	for i, f := range t.Fields {
		fmt.Fprintf(&b, "        self.%s = %s\n", e.fieldProp(t, i), e.zero(f.Type))
	}
	fmt.Fprintf(&b, "\n    def _clone(self):\n        _r = %s()\n        _r._set(self)\n        return _r\n", n)
	b.WriteString("\n    def _set(self, _o):\n")
	if len(t.Fields) == 0 {
		b.WriteString("        pass\n")
	}
	for i, f := range t.Fields {
		p := e.fieldProp(t, i)
		if f.Type.IsAggregate() {
			fmt.Fprintf(&b, "        %s\n", e.setStmt(f.Type, "self."+p, "_o."+p))
		} else {
			fmt.Fprintf(&b, "        self.%s = _o.%s\n", p, p)
		}
	}
	b.WriteString("\n    def _eq(self, _o):\n        return True")
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		p := e.fieldProp(t, i)
		fmt.Fprintf(&b, " and %s", e.eqExpr(f.Type, "self."+p, "_o."+p))
	}
	b.WriteString("\n\n    def _key(self):\n        return (")
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		fmt.Fprintf(&b, "%s, ", e.keyExpr(f.Type, "self."+e.fieldProp(t, i)))
	}
	b.WriteString(")\n\n\n")
	e.helperOut.WriteString(b.String())
}

func (e *emitter) arrayHelpers(t *ir.Type) {
	zero, clone, set, eq, key := e.names.Type(t, "zero_"), e.names.Type(t, "clone_"), e.names.Type(t, "set_"), e.names.Type(t, "eq_"), e.names.Type(t, "key_")
	el := t.Elem
	if byteElem(el) {
		fmt.Fprintf(&e.helperOut, "def %s():\n    return rt.alloc_bytes(%d)\n\n\ndef %s(_s):\n    return bytearray(_s)\n\n\ndef %s(_d, _s):\n    memoryview(_d)[:] = _s\n\n\ndef %s(_x, _y):\n    return _x == _y\n\n\ndef %s(_x):\n    return bytes(_x)\n\n\n", zero, t.Len, clone, set, eq, key)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "def %s():\n    return [%s for _ in range(%d)]\n\n\n", zero, e.zero(el), t.Len)
	fmt.Fprintf(&b, "def %s(_s):\n    return [%s for _x in _s]\n\n\n", clone, e.cloneExpr(el, "_x"))
	if el.IsAggregate() {
		fmt.Fprintf(&b, "def %s(_d, _s):\n    for _i in range(%d):\n        %s\n\n\n", set, t.Len, e.setStmt(el, "_d[_i]", "_s[_i]"))
	} else {
		fmt.Fprintf(&b, "def %s(_d, _s):\n    _d[:] = _s\n\n\n", set)
	}
	fmt.Fprintf(&b, "def %s(_x, _y):\n    return all(%s for _a, _b in zip(_x, _y))\n\n\n", eq, e.eqExpr(el, "_a", "_b"))
	fmt.Fprintf(&b, "def %s(_x):\n    return tuple(%s for _a in _x)\n\n\n", key, e.keyExpr(el, "_a"))
	e.helperOut.WriteString(b.String())
}

func (e *emitter) zero(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "False"
	case ir.KFloat:
		return "0.0"
	case ir.KInt:
		return "0"
	case ir.KString:
		return `b""`
	case ir.KSlice:
		if byteStorage(t) {
			return "rt.BYTE_NIL"
		}
		return "rt.NIL"
	case ir.KStruct:
		return e.class(u) + "()"
	case ir.KArray:
		e.needHelpers(u)
		return e.names.Type(u, "zero_") + "()"
	case ir.KOpaque:
		if !u.OpaqueRef {
			e.use(opaqueContracts[u.Name])
			return "rt." + u.Obj + "()"
		}
	}
	return "None"
}

func (e *emitter) cloneExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KOpaque:
		if u.Kind == ir.KStruct {
			e.class(u)
		} else if u.OpaqueRef {
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
		return ""
	}
	return "lambda _v: " + e.cloneExpr(t, "_v")
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
		return fmt.Sprintf("rt.iface_eq(%s, %s)", a, b)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("rt.uncomparable_eq(%q)", ir.TypeString(t))
	case ir.KPointer, ir.KChan, ir.KOpaque:
		return a + " is " + b
	}
	return a + " == " + b
}

func (e *emitter) keyExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KFloat:
		return "rt.float_key(" + x + ")"
	case ir.KStruct:
		e.class(u)
		return x + "._key()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s)", e.names.Type(u, "key_"), x)
	case ir.KInterface:
		return fmt.Sprintf("rt.iface_key(%s)", x)
	case ir.KPointer, ir.KChan, ir.KOpaque:
		return fmt.Sprintf("id(%s)", x)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("rt.unhashable_key(%q)", ir.TypeString(t))
	case ir.KBool:
		return fmt.Sprintf("(%s, 'b')", x)
	}
	return x
}

func (e *emitter) keyFn(t *ir.Type) string {
	switch t.U().Kind {
	case ir.KFloat, ir.KStruct, ir.KArray, ir.KInterface:
		return "lambda _k: " + e.keyExpr(t, "_k")
	}
	return "rt.identity_key"
}

func (e *emitter) td(t *ir.Type) string {
	if n, ok := e.tds[t]; ok {
		return n
	}
	n := "_types." + e.names.Type(t, "td_")
	e.tds[t] = n
	return n
}

func (e *emitter) typeDescs(b *bytes.Buffer) {
	var ts []*ir.Type
	for t := range e.tds {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for _, t := range ts {
		if t.Kind == ir.KString {
			fmt.Fprintf(b, "%s = rt.STRING_TYPE\n", e.tds[t])
			continue
		}
		name := ir.TypeString(t)
		u := t.U()
		basic := "None"
		switch u.Kind {
		case ir.KInt:
			basic = `"int"`
		case ir.KBool:
			basic = `"bool"`
		case ir.KString:
			basic = `"string"`
		case ir.KFloat:
			basic = strconv.Quote(u.Basic)
		}
		eq := "lambda _a, _b: " + e.eqExpr(t, "_a", "_b")
		key := "lambda _a: " + e.keyExpr(t, "_a")
		comparable := "True"
		if !t.Comparable() {
			eq = fmt.Sprintf("lambda _a, _b: rt.uncomparable_eq(%q)", name)
			key = fmt.Sprintf("lambda _a: rt.unhashable_key(%q)", name)
			comparable = "False"
		}
		var ms []string
		for _, m := range t.MethodSet {
			fn := e.symbol(m.Func.Sym)
			if t.IsAggregate() {
				fn = fmt.Sprintf("rt.closure(%d, lambda _r, *_a: %s(%s, *_a))", m.Func.ID, e.symbol(m.Func.Sym), e.cloneExpr(t, "_r"))
			}
			if e.p.SuspMethods[m.ID] && !m.Func.MaySuspend {
				fn = e.adapt(fn, len(m.Func.Results))
			}
			ms = append(ms, fmt.Sprintf("%q: %s", m.ID, fn))
		}
		fmt.Fprintf(b, "%s = rt.type_desc(name=%q, kind=%q, eq=%s, key=%s, methods={%s}, basic=%s, comparable=%s)\n",
			e.tds[t], name, u.Kind.String(), eq, key, strings.Join(ms, ", "), basic, comparable)
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
	// task is set when f is a coroutine of the native pause model, taking
	// the running task as its first parameter t.
	task   bool
	indent string
	folds  *structure.Folds
}

func (fe *fnEmitter) localName(l *ir.Local) string {
	if n, ok := fe.folds.Name(l); ok {
		return n
	}
	return fe.e.names.Local(l, "_")
}

func (fe *fnEmitter) w(format string, args ...any) {
	fe.b.WriteString(fe.indent)
	fmt.Fprintf(&fe.b, format, args...)
	fe.b.WriteString("\n")
}

func (fe *fnEmitter) local(l *ir.Local) string {
	if fe.frame {
		return "_fr." + fe.localName(l)
	}
	return fe.localName(l)
}

func cell(l *ir.Local) bool { return l.Boxed && !l.Type.IsAggregate() }

func (fe *fnEmitter) val(v ir.Value) string {
	switch v := v.(type) {
	case *ir.Local:
		if s, ok := fe.folds.Use(v); ok {
			return s
		}
		if cell(v) {
			return fe.local(v) + ".v"
		}
		return fe.local(v)
	case *ir.Const:
		return fe.e.constant(v)
	case *ir.FuncRef:
		if fe.e.susp(v.Type) && !v.Func.MaySuspend {
			return fe.e.adapt(fe.e.symbol(v.Func.Sym), len(v.Func.Results))
		}
		return fe.e.symbol(v.Func.Sym)
	}
	panic(fmt.Sprintf("py: operand %T", v))
}

func (e *emitter) constant(c *ir.Const) string {
	if c.Nil {
		if c.Type.U().Kind == ir.KSlice {
			return e.zero(c.Type)
		}
		return "None"
	}
	if c.Type.U().Kind == ir.KFloat {
		return ir.FloatLiteral(c)
	}
	switch c.Val.Kind() {
	case constant.Bool:
		if constant.BoolVal(c.Val) {
			return "True"
		}
		return "False"
	case constant.String:
		return pyBytes(constant.StringVal(c.Val))
	case constant.Int:
		return c.Val.ExactString()
	}
	panic("py: constant " + c.Val.String())
}

func pyBytes(s string) string {
	var b strings.Builder
	b.WriteString(`b"`)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "\\x%02x", c)
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

func (fe *fnEmitter) globalRef(g *ir.Global) string {
	if g.AddrTaken && !g.Type.IsAggregate() {
		return fe.e.symbol(g.Sym) + ".v"
	}
	return fe.e.symbol(g.Sym)
}

func (fe *fnEmitter) rootExpr(p *ir.Place) string {
	switch r := p.Root.(type) {
	case ir.LocalRoot:
		return fe.val(r.Local)
	case ir.GlobalRoot:
		return fe.globalRef(r.Global)
	case ir.DerefRoot:
		if r.Ptr.IRType().U().Elem.IsAggregate() {
			return "rt.nilchk(" + fe.val(r.Ptr) + ")"
		}
		return "rt.nilchk(" + fe.val(r.Ptr) + ").v"
	case ir.SliceRoot:
		fe.e.use("core.slice.index")
		return "rt.sget(" + fe.val(r.Slice) + ", " + fe.val(r.Index) + ")"
	case ir.ValueRoot:
		return fe.val(r.Value)
	}
	panic("py: place root")
}

func (fe *fnEmitter) proj(base string, cur *ir.Type, pr ir.Proj) string {
	if pr.Index != nil {
		return fmt.Sprintf("%s[rt.idx(%s, %d)]", base, fe.val(pr.Index), cur.U().Len)
	}
	return base + "." + fe.e.fieldProp(cur, pr.Field)
}

func (fe *fnEmitter) placeExpr(p *ir.Place) string {
	s := fe.rootExpr(p)
	cur := rootType(p)
	for _, pr := range p.Path {
		s = fe.proj(s, cur, pr)
		cur = pr.Type
	}
	return s
}

func (fe *fnEmitter) store(p *ir.Place, v string) string {
	t := p.Type
	if sr, ok := p.Root.(ir.SliceRoot); ok && len(p.Path) == 0 && !t.IsAggregate() {
		fe.e.use("core.slice.store")
		return fmt.Sprintf("rt.sset(%s, %s, %s)", fe.val(sr.Slice), fe.val(sr.Index), v)
	}
	if lr, ok := p.Root.(ir.LocalRoot); ok && len(p.Path) == 0 && t.IsAggregate() && lr.Local.Kind == ir.LTemp {
		return fe.local(lr.Local) + " = " + v
	}
	if t.IsAggregate() {
		return fe.e.setStmt(t, fe.placeExpr(p), v)
	}
	return fe.placeExpr(p) + " = " + v
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
		return fe.placeExpr(p)
	}
	last := p.Path[len(p.Path)-1]
	base := &ir.Place{Root: p.Root, Path: p.Path[:len(p.Path)-1]}
	cur := rootType(p)
	if len(p.Path) > 1 {
		cur = p.Path[len(p.Path)-2].Type
	}
	return fmt.Sprintf("rt.field_ref(%s, %q)", fe.placeExpr(base), fe.e.fieldProp(cur, last.Field))
}

func (e *emitter) function(f *ir.Func) string {
	if f.MaySuspend && !e.native() {
		return e.frameFunction(f)
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
	var params []string
	for _, l := range f.Env {
		params = append(params, fe.localName(l))
	}
	for _, l := range f.Params {
		params = append(params, fe.localName(l))
	}
	fe.analyze()
	if fe.task {
		e.use("core.task.spawn")
		fe.w("%sasync def %s(%s):", position(f.Pos), e.names.Derived("a", e.names.Symbol(f.Sym)), strings.Join(append([]string{"_t"}, params...), ", "))
	} else {
		fe.w("@rt.source_guard")
		fe.w("%sdef %s(%s):", position(f.Pos), e.names.Symbol(f.Sym), strings.Join(params, ", "))
	}
	fe.indent = "    "
	fe.globalDecl()
	for _, r := range f.Results {
		fe.w("%s = None", fe.localName(r))
	}
	if fe.defers {
		fe.w("_d = []")
		fe.w("_p = None")
		fe.w("try:")
		fe.structured(2)
		fe.w("except rt.GoPanic as _e:")
		fe.w("    _p = _e")
		if fe.pausingDefer() {
			fe.w("await rt.run_defers_async(_t, _d, _p)")
		} else {
			fe.w("rt.run_defers(_d, _p)")
		}
		fe.w("return %s", fe.results())
	} else {
		fe.structured(1)
	}
	fe.indent = ""
	fe.b.WriteString("\n\n")
	if fe.task {
		n := e.names.Symbol(f.Sym)
		fe.w("def %s(%s):", n, strings.Join(params, ", "))
		fe.w("    return rt.native_task(lambda _t: %s(%s), %d)", e.names.Derived("a", n), strings.Join(append([]string{"_t"}, params...), ", "), len(f.Results))
		fe.b.WriteString("\n\n")
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

func (fe *fnEmitter) analyze() {
	structure.Analyze(fe.f, structure.FoldOptions{Inline: fe.task, Order: structure.LeftToRight}, pyProbe{fe}, func(fs *structure.Folds) { fe.folds = fs })
}

var pySyntax = structure.Syntax{
	Indent: "    ", If: "if %s:", ElseIf: "elif %s:", Else: "else:",
	Loop: "while True:", While: "while %s:", Break: "break", Continue: "continue",
	Python: true, Flag: "_gx", Pass: "pass", Not: "not %s", And: "%s and %s", Or: "%s or %s",
	Default: []string{"else:", "    raise rt.fault(\"bad block\")"},
}

// structured writes the function body as structured statements; a
// function with defers leaves its body through a break, and a resumable
// frame dispatches on the block it resumes at.
func (fe *fnEmitter) structured(depth int) {
	body := structure.Build(fe.f, structure.Options{Inline: fe.task, ExitReturns: fe.defers, Empty: func(b *ir.Block) bool { return fe.folds.Empty(b, fe.task) }})
	syn := pySyntax
	pc := fe.pc()
	syn.DispatchOpen = []string{"while True:"}
	if !fe.frame {
		syn.DispatchOpen = []string{pc + " = 0", "while True:"}
	}
	syn.CaseFirst, syn.CaseOpen, syn.SetPC = "if "+pc+" == %d:", "elif "+pc+" == %d:", pc+" = %d"
	fe.b.WriteString(structure.Print(body, syn, pyEmitter{fe}, depth))
}

func (fe *fnEmitter) capture(fn func()) string {
	saved, indent := fe.b, fe.indent
	fe.b, fe.indent = strings.Builder{}, ""
	fn()
	out := fe.b.String()
	fe.b, fe.indent = saved, indent
	return out
}

// pyEmitter prints the target-specific statements of a structured body.
type pyEmitter struct{ fe *fnEmitter }

func (p pyEmitter) Code(b *ir.Block) string {
	fe := p.fe
	return fe.capture(func() {
		if b.ResumeOf != nil && fe.frame {
			fe.resume(b.ResumeOf)
		}
		for _, in := range b.Instrs {
			if !fe.folds.Folded(in) {
				fe.instr(in)
			}
		}
	})
}

func (p pyEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	return p.fe.capture(func() { p.fe.term(t) })
}

func (p pyEmitter) Pause(ps *ir.Pause) string {
	if p.fe.folds.FoldedPause(ps) {
		return ""
	}
	return p.fe.capture(func() { p.fe.nativePause(ps, p.fe.mark(ps.Position())) })
}

func (p pyEmitter) Cond(v ir.Value) string    { return p.fe.val(v) }
func (p pyEmitter) Mark(pos token.Pos) string { return p.fe.mark(pos) }

// pyProbe renders statements for fold analysis.
type pyProbe struct{ fe *fnEmitter }

func (p pyProbe) Instr(in ir.Instr) string { return p.fe.capture(func() { p.fe.instr(in) }) }
func (p pyProbe) Pause(ps *ir.Pause) string {
	return p.fe.capture(func() { p.fe.nativePause(ps, "") })
}
func (p pyProbe) Cond(v ir.Value) string { return p.fe.val(v) }
func (p pyProbe) Leaf(b *ir.Block, t ir.Terminator) string {
	return p.fe.capture(func() { p.fe.term(t) })
}
func (p pyProbe) Assign(l *ir.Local) (string, string) { return p.fe.val(l) + " = ", "" }

// globalDecl declares the globals the function rebinds.
func (fe *fnEmitter) globalDecl() {
	seen := map[string]bool{}
	var gs []string
	for _, b := range fe.f.Blocks {
		for _, in := range b.Instrs {
			st, ok := in.(*ir.Store)
			if !ok {
				continue
			}
			gr, ok := st.Place.Root.(ir.GlobalRoot)
			if !ok || len(st.Place.Path) > 0 || st.Place.Type.IsAggregate() || gr.Global.AddrTaken || fe.e.locations[gr.Global.Sym] != fe.e.current {
				continue
			}
			if !seen[fe.e.names.Symbol(gr.Global.Sym)] {
				seen[fe.e.names.Symbol(gr.Global.Sym)] = true
				gs = append(gs, fe.e.names.Symbol(gr.Global.Sym))
			}
		}
	}
	if len(gs) > 0 {
		sort.Strings(gs)
		fe.w("global %s", strings.Join(gs, ", "))
	}
}

func (fe *fnEmitter) pc() string {
	if fe.frame {
		return "_fr.pc"
	}
	return "_pc"
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
		return "None"
	case 1:
		return vs[0]
	}
	return "(" + strings.Join(vs, ", ") + ")"
}

func (fe *fnEmitter) mark(pos token.Pos) string {
	if !pos.IsValid() {
		return ""
	}
	return fmt.Sprintf("%s%d\x00", marker, int(pos))
}

func (fe *fnEmitter) term(t ir.Terminator) {
	m := fe.mark(t.Position())
	switch t := t.(type) {
	case *ir.Pause:
		fe.pause(t)
	case *ir.Return:
		switch {
		case fe.frame:
			fe.w("%srt.ret(_t, _fr)", m)
			fe.w("return")
		default:
			fe.w("%sreturn %s", m, fe.results())
		}
	case *ir.Panic:
		fe.w("%sraise rt.go_panic(%s)", m, fe.val(t.X))
	case *ir.PanicRuntime:
		fe.w("%sraise rt.plain_panic(%s)", m, pyBytes(t.Msg))
	case *ir.Unreachable:
		fe.w("%sraise rt.fault(\"unreachable\")", m)
	default:
		panic(fmt.Sprintf("py: terminator %T", t))
	}
}

// frameFunction emits a suspending function as a resumable frame class
// and a starter binding the arguments.
func (e *emitter) frameFunction(f *ir.Func) string {
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}, frame: true}
	for i, b := range f.Blocks {
		fe.order[b] = i
	}
	fe.analyze()
	cls := e.names.Derived("frame", e.names.Symbol(f.Sym))
	fe.w("%sclass %s(rt.Frame):", position(f.Pos), cls)
	fe.indent = "    "
	fe.w("def results(self):")
	fe.w("    _fr = self")
	fe.w("    return [%s]", fe.resultsList())
	fe.w("")
	fe.w("def step(self, _t):")
	fe.w("    _fr = self")
	fe.indent = "        "
	fe.globalDecl()
	fe.indent = ""
	fe.structured(2)
	fe.b.WriteString("\n\n")
	var params []string
	for _, l := range f.Env {
		params = append(params, fe.localName(l))
	}
	for _, l := range f.Params {
		params = append(params, fe.localName(l))
	}
	fe.w("%sdef %s(%s):", position(f.Pos), e.names.Symbol(f.Sym), strings.Join(params, ", "))
	fe.w("    _fr = %s()", cls)
	fe.w("    rt.Frame.__init__(_fr)")
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		fe.w("    _fr.%s = %s", fe.localName(l), fe.localName(l))
	}
	for _, r := range f.Results {
		fe.w("    _fr.%s = None", fe.localName(r))
	}
	fe.w("    return _fr")
	fe.b.WriteString("\n\n")
	return structure.Resolve(fe.b.String())
}

func (fe *fnEmitter) resultsList() string {
	var vs []string
	for _, r := range fe.f.Results {
		v := fe.val(r)
		if r.Boxed && r.Type.IsAggregate() {
			v = fe.e.cloneExpr(r.Type, v)
		}
		vs = append(vs, v)
	}
	return strings.Join(vs, ", ")
}

func (fe *fnEmitter) resume(op ir.Instr) {
	switch i := op.(type) {
	case *ir.Call:
		for k, d := range i.Dsts {
			if d != nil {
				fe.w("%s = _t.rv[%d]", fe.val(d), k)
			}
		}
	case *ir.Recv:
		if i.Dst != nil {
			fe.w("%s = _t.rv[0]", fe.val(i.Dst))
		}
		if i.Ok != nil {
			fe.w("%s = _t.rv[1]", fe.val(i.Ok))
		}
	case *ir.Select:
		fe.w("%s = _t.rv[0]", fe.val(i.Index))
		for k, c := range i.Cases {
			if c.Send || c.Dst == nil && c.Ok == nil {
				continue
			}
			fe.w("if _t.rv[0] == %d:", k)
			if c.Dst != nil {
				fe.w("    %s = _t.rv[1]", fe.val(c.Dst))
			}
			if c.Ok != nil {
				fe.w("    %s = _t.rv[2]", fe.val(c.Ok))
			}
		}
	}
}

func (fe *fnEmitter) pause(p *ir.Pause) {
	e := fe.e
	fe.w("%s_fr.pc = %d", fe.mark(p.Position()), fe.order[p.Next])
	switch i := p.Op.(type) {
	case *ir.Call:
		fn, args, _ := fe.callee(i)
		switch i.Kind {
		case ir.CallExtern:
			fe.w("%s(%s)", fn, strings.Join(append([]string{"_t"}, args...), ", "))
		case ir.CallValue:
			fe.w("rt.call(_t, rt.fnchk(%s)(%s))", fn, strings.Join(args, ", "))
		default:
			fe.w("rt.call(_t, %s(%s))", fn, strings.Join(args, ", "))
		}
	case *ir.Send:
		e.use("core.chan.send")
		fe.w("rt.chan_send(_t, %s, %s)", fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		e.use("core.chan.recv")
		fe.w("rt.chan_recv(_t, %s)", fe.val(i.Ch))
	case *ir.Select:
		e.use("core.select")
		fe.w("rt.select(_t, [%s], %s)", strings.Join(fe.selectCases(i), ", "), pyBool(i.Default))
	default:
		panic(fmt.Sprintf("py: pause on %T", p.Op))
	}
	fe.w("return")
}

// nativePause emits a pause point of a coroutine, prefixed with mark m: a
// generated callee is awaited for its results; a runtime primitive either
// completes at once or leaves the task blocked, and then the task waits for
// the scheduler to choose it again. Primitive, function value and method
// results arrive in t.rv.
func (fe *fnEmitter) nativePause(p *ir.Pause, m string) {
	e := fe.e
	const wait = "if _t.blocked: await rt.wait(_t)"
	switch i := p.Op.(type) {
	case *ir.Call:
		fn, args, _ := fe.callee(i)
		args = append([]string{"_t"}, args...)
		switch i.Kind {
		case ir.CallExtern:
			fe.w("%s%s(%s)", m, fn, strings.Join(args, ", "))
			fe.w(wait)
		case ir.CallStatic:
			call := fmt.Sprintf("await %s(%s)", e.qualify(i.Func.Sym, e.names.Derived("a", e.names.Symbol(i.Func.Sym))), strings.Join(args, ", "))
			if d := fe.resultTargets(i.Dsts); d != "" {
				fe.w("%s%s = %s", m, d, call)
			} else {
				fe.w("%s%s", m, call)
			}
			return
		default:
			if i.Kind == ir.CallValue {
				fn = "rt.fnchk(" + fn + ")"
			}
			fe.w("%sawait rt.await_frame(_t, %s(%s))", m, fn, strings.Join(args[1:], ", "))
		}
	case *ir.Send:
		e.use("core.chan.send")
		fe.w("%srt.chan_send(_t, %s, %s)", m, fe.val(i.Ch), fe.val(i.V))
		fe.w(wait)
	case *ir.Recv:
		e.use("core.chan.recv")
		fe.w("%srt.chan_recv(_t, %s)", m, fe.val(i.Ch))
		fe.w(wait)
	case *ir.Select:
		e.use("core.select")
		fe.w("%srt.select(_t, [%s], %s)", m, strings.Join(fe.selectCases(i), ", "), pyBool(i.Default))
		fe.w(wait)
	default:
		panic(fmt.Sprintf("py: pause on %T", p.Op))
	}
	fe.resume(p.Op)
}

// resultTargets renders the assignment targets of a call's results, with _
// for a discarded one, or "" when every result is discarded.
func (fe *fnEmitter) resultTargets(ds []*ir.Local) string {
	var s []string
	used := false
	for _, d := range ds {
		if d == nil {
			s = append(s, "_")
			continue
		}
		used = true
		s = append(s, fe.val(d))
	}
	if !used {
		return ""
	}
	return strings.Join(s, ", ")
}

func (fe *fnEmitter) selectCases(i *ir.Select) []string {
	var cs []string
	for _, c := range i.Cases {
		if c.Send {
			cs = append(cs, fmt.Sprintf("(%s, True, %s)", fe.val(c.Ch), fe.val(c.V)))
		} else {
			cs = append(cs, fmt.Sprintf("(%s, False, None)", fe.val(c.Ch)))
		}
	}
	return cs
}

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

var binName = map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Rem: "rem",
	ir.And: "and", ir.Or: "or", ir.Xor: "xor", ir.AndNot: "andnot", ir.Shl: "shl", ir.Shr: "shr"}

var binContract = map[ir.BinOpKind]string{ir.Add: "core.integer.add", ir.Sub: "core.integer.sub", ir.Mul: "core.integer.mul",
	ir.Div: "core.integer.div", ir.Rem: "core.integer.rem", ir.And: "core.integer.and", ir.Or: "core.integer.or",
	ir.Xor: "core.integer.xor", ir.AndNot: "core.integer.andnot", ir.Shl: "core.integer.shl", ir.Shr: "core.integer.shr"}

func (fe *fnEmitter) callee(c *ir.Call) (fn string, args []string, fid string) {
	for _, a := range c.Args {
		args = append(args, fe.val(a))
	}
	switch c.Kind {
	case ir.CallStatic:
		return fe.e.symbol(c.Func.Sym), args, strconv.Itoa(c.Func.ID)
	case ir.CallValue:
		f := fe.val(c.Fn)
		return f, args, "rt.fid(" + f + ")"
	case ir.CallInterface:
		x := fe.val(c.Recv)
		return fmt.Sprintf("rt.ichk(%s).t.methods[%q]", x, c.Method), append([]string{x + ".v"}, args...), "None"
	case ir.CallExtern:
		fe.e.use(c.Extern.Contract)
		for k, a := range c.Args {
			if u := a.IRType().U(); u.Kind == ir.KSlice && u.Elem.U().Kind == ir.KFunc && !fe.e.susp(u.Elem) {
				fe.e.use("core.task.spawn")
				args[k] = fmt.Sprintf("rt.adapt_slice(%s, %d)", args[k], len(u.Elem.U().Results))
			}
		}
		sym := "rt." + externSymbol(c.Extern.Contract)
		if m, ok := fe.e.symbols[c.Extern.Contract]; ok {
			sym = m
		}
		return sym, args, "None"
	}
	panic("py: call kind")
}

func externSymbol(contract string) string {
	return strings.ReplaceAll(contract, ".", "_")
}

func (fe *fnEmitter) dsts(ds []*ir.Local) string {
	var s []string
	for _, d := range ds {
		s = append(s, fe.val(d))
	}
	return strings.Join(s, ", ")
}

func (fe *fnEmitter) instr(in ir.Instr) {
	e := fe.e
	m := fe.mark(in.Position())
	fe.b.WriteString(strings.Repeat("", 0))
	w := func(format string, args ...any) {
		fe.w(m+format, args...)
		m = ""
	}
	switch i := in.(type) {
	case *ir.DeclVar:
		init := e.zero(i.L.Type)
		if i.Init != nil {
			init = fe.val(i.Init)
		}
		if cell(i.L) {
			w("%s = rt.Cell(%s)", fe.local(i.L), init)
		} else {
			w("%s = %s", fe.local(i.L), init)
		}
	case *ir.Assign:
		w("%s = %s", fe.val(i.Dst), fe.val(i.Src))
	case *ir.Zero:
		w("%s = %s", fe.val(i.Dst), e.zero(i.Dst.Type))
	case *ir.Load:
		x := fe.placeExpr(i.Place)
		if i.Dst.Type.IsAggregate() {
			x = e.cloneExpr(i.Dst.Type, x)
		}
		w("%s = %s", fe.val(i.Dst), x)
	case *ir.Store:
		w("%s", fe.store(i.Place, fe.val(i.V)))
	case *ir.AddrOf:
		w("%s = %s", fe.val(i.Dst), fe.addrOf(i.Place))
	case *ir.New:
		el := i.Dst.Type.U().Elem
		if el.IsAggregate() {
			w("%s = %s", fe.val(i.Dst), e.zero(el))
		} else {
			w("%s = rt.Cell(%s)", fe.val(i.Dst), e.zero(el))
		}
	case *ir.UnOp:
		x := fe.val(i.X)
		switch i.Op {
		case ir.Not:
			w("%s = not %s", fe.val(i.Dst), x)
		case ir.Neg:
			if i.Dst.Type.U().Kind == ir.KFloat {
				e.use("core.float.neg")
				w("%s = rt.float_neg_f%d(%s)", fe.val(i.Dst), i.Dst.Type.U().FloatBits, x)
				break
			}
			e.use("core.integer.neg")
			w("%s = rt.neg_%s(%s)", fe.val(i.Dst), intKind(i.Dst.Type), x)
		case ir.BitNot:
			e.use("core.integer.not")
			w("%s = rt.not_%s(%s)", fe.val(i.Dst), intKind(i.Dst.Type), x)
		}
	case *ir.BinOp:
		w("%s", fe.binop(i))
	case *ir.Convert:
		w("%s", fe.convert(i))
	case *ir.MakeInterface:
		w("%s = rt.box(%s, %s)", fe.val(i.Dst), e.td(i.X.IRType()), fe.val(i.X))
	case *ir.TypeAssert:
		fe.typeAssert(i, w)
	case *ir.Call:
		fn, args, _ := fe.callee(i)
		as := strings.Join(args, ", ")
		call := fn + "(" + as + ")"
		if i.Kind == ir.CallValue {
			call = "rt.fnchk(" + fn + ")(" + as + ")"
		}
		if i.Kind == ir.CallExtern && fe.adaptsResults(i) {
			w("_r = %s", call)
			for k, d := range i.Dsts {
				v := fmt.Sprintf("_r[%d]", k)
				if len(i.Dsts) == 1 {
					v = "_r"
				}
				if d.Type.U().Kind == ir.KFunc && e.susp(d.Type) {
					v = e.adapt(v, len(d.Type.U().Results))
				}
				w("%s = %s", fe.val(d), v)
			}
			return
		}
		if len(i.Dsts) > 0 {
			w("%s = %s", fe.dsts(i.Dsts), call)
		} else {
			w("%s", call)
		}
	case *ir.MakeClosure:
		var ps, as []string
		for j, l := range i.Env {
			ps = append(ps, fmt.Sprintf("_e%d", j))
			as = append(as, fe.local(l))
		}
		inner := append(append([]string(nil), ps...), "*_a")
		v := fmt.Sprintf("rt.closure(%d, (lambda %s: (lambda *_a: %s(%s)))(%s))", i.Func.ID, strings.Join(ps, ", "), fe.e.symbol(i.Func.Sym), strings.Join(inner, ", "), strings.Join(as, ", "))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		w("%s = %s", fe.val(i.Dst), v)
	case *ir.MakeBound:
		v := fmt.Sprintf("rt.bound(%d, %s, %s)", i.Func.ID, fe.e.symbol(i.Func.Sym), fe.val(i.Recv))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		w("%s = %s", fe.val(i.Dst), v)
	case *ir.MakeIfaceBound:
		v := fmt.Sprintf("rt.ibound(%s, %q)", fe.val(i.Recv), i.Method)
		if e.susp(i.Dst.Type) && !e.p.SuspMethods[i.Method] {
			v = e.adapt(v, len(i.Dst.Type.U().Results))
		}
		w("%s = %s", fe.val(i.Dst), v)
	case *ir.Len:
		w("%s = %s", fe.val(i.Dst), fe.lenExpr(i.X, false))
	case *ir.Cap:
		w("%s = %s", fe.val(i.Dst), fe.lenExpr(i.X, true))
	case *ir.MakeSlice:
		e.use("core.slice.make")
		w("%s = rt.make_slice(%s, %s, lambda: %s, %s)", fe.val(i.Dst), fe.val(i.Len), fe.val(i.Cap), e.zero(i.Dst.Type.U().Elem), pyBool(byteStorage(i.Dst.Type)))
	case *ir.MakeMap:
		e.use("core.map.make")
		w("%s = rt.make_map(%s)", fe.val(i.Dst), e.keyFn(i.Dst.Type.U().Key))
	case *ir.Append:
		e.use("core.slice.append")
		cl := e.cloneFn(i.Dst.Type.U().Elem)
		var v string
		switch {
		case i.Spread != nil && i.Spread.IRType().U().Kind == ir.KString:
			v = fmt.Sprintf("rt.append_string(%s, %s)", fe.val(i.S), fe.val(i.Spread))
		case i.Spread != nil:
			v = fmt.Sprintf("rt.append_slice(%s, %s%s)", fe.val(i.S), fe.val(i.Spread), optArg(cl))
		default:
			var es []string
			for _, x := range i.Elems {
				es = append(es, fe.val(x))
			}
			if byteStorage(i.Dst.Type) {
				v = fmt.Sprintf("rt.append(%s, bytearray((%s))%s)", fe.val(i.S), strings.Join(es, ", ")+comma(es), optArg(cl))
			} else {
				v = fmt.Sprintf("rt.append(%s, [%s]%s)", fe.val(i.S), strings.Join(es, ", "), optArg(cl))
			}
		}
		w("%s = rt.zero_append_growth(%s, %s, lambda: %s)", fe.val(i.Dst), v, fe.val(i.S), e.zero(i.Dst.Type.U().Elem))
	case *ir.Copy:
		e.use("core.slice.copy")
		if i.Src.IRType().U().Kind == ir.KString {
			w("%s = rt.copy_string(%s, %s)", fe.val(i.N), fe.val(i.Dst), fe.val(i.Src))
		} else {
			w("%s = rt.copy(%s, %s%s)", fe.val(i.N), fe.val(i.Dst), fe.val(i.Src), optArg(e.cloneFn(i.Dst.IRType().U().Elem)))
		}
	case *ir.IndexString:
		e.use("core.string.index")
		w("%s = rt.sindex(%s, %s)", fe.val(i.Dst), fe.val(i.S), fe.val(i.I))
	case *ir.SliceOp:
		w("%s", fe.sliceOp(i))
	case *ir.MapLookup:
		e.use("core.map.lookup")
		mt := i.M.IRType().U()
		ok := "_"
		if i.Ok != nil {
			ok = fe.val(i.Ok)
		}
		w("%s, %s = rt.map_get(%s, %s, %s, lambda: %s)", fe.val(i.Dst), ok, fe.val(i.M), fe.val(i.K), e.keyFn(mt.Key), e.zero(mt.Elem))
		if mt.Elem.IsAggregate() {
			w("%s = %s", fe.val(i.Dst), e.cloneExpr(mt.Elem, fe.val(i.Dst)))
		}
	case *ir.MapStore:
		e.use("core.map.store")
		w("rt.map_set(%s, %s, %s)", fe.val(i.M), fe.val(i.K), fe.val(i.V))
	case *ir.MapDelete:
		e.use("core.map.delete")
		w("rt.map_delete(%s, %s, %s)", fe.val(i.M), fe.val(i.K), e.keyFn(i.M.IRType().U().Key))
	case *ir.Clear:
		if i.X.IRType().U().Kind == ir.KMap {
			e.use("core.map.clear")
			w("rt.map_clear(%s)", fe.val(i.X))
		} else {
			e.use("core.slice.clear")
			w("rt.clear_slice(%s, lambda: %s)", fe.val(i.X), e.zero(i.X.IRType().U().Elem))
		}
	case *ir.DecodeRune:
		e.use("core.string.decode_rune")
		w("%s, %s = rt.decode_rune(%s, %s)", fe.val(i.Rune), fe.val(i.Width), fe.val(i.S), fe.val(i.I))
	case *ir.MapIterInit:
		e.use("core.map.iterate")
		w("%s = rt.map_iter(%s)", fe.val(i.Iter), fe.val(i.M))
	case *ir.MapIterNext:
		mt := i.Iter.Type.Elem.U()
		w("%s = rt.map_next(%s)", fe.val(i.Ok), fe.val(i.Iter))
		if i.Key != nil {
			w("if %s: %s = %s", fe.val(i.Ok), fe.val(i.Key), e.cloneExpr(mt.Key, fe.val(i.Iter)+".k"))
		}
		if i.Val != nil {
			w("if %s: %s = %s", fe.val(i.Ok), fe.val(i.Val), e.cloneExpr(mt.Elem, fe.val(i.Iter)+".v"))
		}
	case *ir.Print:
		e.use("core.print")
		var as []string
		for _, a := range i.Args {
			v := fe.val(a)
			if a.IRType().U().Kind == ir.KFloat {
				v = fmt.Sprintf("rt.float_print(%s, %d)", v, a.IRType().U().FloatBits)
			}
			as = append(as, v)
		}
		w("rt.go_print([%s], %s)", strings.Join(as, ", "), pyBool(i.Newline))
	case *ir.Defer:
		fn, args, fid := fe.callee(i.Call)
		if i.Call.Kind == ir.CallExtern && i.Call.Suspends {
			fn = "rt.await_native(" + fn + ")"
		}
		if i.Call.Kind == ir.CallInterface {
			panic("py: defer of interface call")
		}
		if fe.frame {
			w("_fr.defers.append(rt.Deferred(%s, [%s], %s, %s))", fn, strings.Join(args, ", "), fid, pyBool(i.Call.Suspends))
			return
		}
		if fe.task && i.Call.Suspends {
			w("_d.append(rt.Deferred(%s, [%s], %s, True))", fn, strings.Join(args, ", "), fid)
			return
		}
		w("_d.append(rt.Deferred(%s, [%s], %s))", fn, strings.Join(args, ", "), fid)
	case *ir.Recover:
		w("%s = rt.recover(%d)", fe.val(i.Dst), fe.f.ID)
	case *ir.Rebind:
		switch {
		case cell(i.L):
			w("%s = rt.Cell(%s.v)", fe.local(i.L), fe.local(i.L))
		case i.L.Type.IsAggregate():
			w("%s = %s", fe.local(i.L), e.cloneExpr(i.L.Type, fe.local(i.L)))
		default:
			w("pass")
		}
	case *ir.BoxParam:
		if cell(i.L) {
			w("%s = rt.Cell(%s)", fe.local(i.L), fe.local(i.L))
		}
	case *ir.MakeChan:
		e.use("core.chan.make")
		w("%s = rt.make_chan(%s, lambda: %s)", fe.val(i.Dst), fe.val(i.Size), e.zero(i.Dst.Type.U().Elem))
	case *ir.Close:
		e.use("core.chan.close")
		w("rt.chan_close(%s)", fe.val(i.Ch))
	case *ir.Go:
		e.use("core.task.spawn")
		fn, args, _ := fe.callee(i.Call)
		if i.Call.Kind == ir.CallExtern && i.Call.Suspends {
			fn = "rt.await_native(" + fn + ")"
		}
		if i.Call.Kind == ir.CallInterface {
			panic("py: go statement with interface call")
		}
		if i.Call.Kind == ir.CallValue {
			fn = "rt.fnchk(" + fn + ")"
		}
		var ps []string
		for k := range args {
			ps = append(ps, fmt.Sprintf("_a%d", k))
		}
		call := fmt.Sprintf("_f(%s)", strings.Join(ps, ", "))
		if !i.Call.Suspends {
			call = fmt.Sprintf("rt.sync(lambda: (_f(%s), [])[1])", strings.Join(ps, ", "))
		}
		w("rt.spawn((lambda %s: %s)(%s))", strings.Join(append([]string{"_f"}, ps...), ", "), call, strings.Join(append([]string{fn}, args...), ", "))
	default:
		panic(fmt.Sprintf("py: unsupported instruction %T", in))
	}
}

func (fe *fnEmitter) adaptsResults(c *ir.Call) bool {
	for _, d := range c.Dsts {
		if d != nil && d.Type.U().Kind == ir.KFunc && fe.e.susp(d.Type) {
			return true
		}
	}
	return false
}

func optArg(s string) string {
	if s == "" {
		return ""
	}
	return ", " + s
}

func (fe *fnEmitter) lenExpr(x ir.Value, isCap bool) string {
	t := x.IRType().U()
	v := fe.val(x)
	switch t.Kind {
	case ir.KString:
		return "len(" + v + ")"
	case ir.KSlice:
		if isCap {
			return v + ".c"
		}
		return v + ".l"
	case ir.KArray:
		return strconv.FormatInt(t.Len, 10)
	case ir.KPointer:
		return strconv.FormatInt(t.Elem.U().Len, 10)
	case ir.KMap:
		fe.e.use("core.map.len")
		return "rt.map_len(" + v + ")"
	case ir.KChan:
		if isCap {
			fe.e.use("core.chan.cap")
			return "rt.chan_cap(" + v + ")"
		}
		fe.e.use("core.chan.len")
		return "rt.chan_len(" + v + ")"
	}
	panic("py: len of " + t.String())
}

func (fe *fnEmitter) binop(i *ir.BinOp) string {
	e := fe.e
	d := fe.val(i.Dst)
	x, y := fe.val(i.X), fe.val(i.Y)
	t := i.X.IRType()
	if t.U().Kind == ir.KFloat && !i.Op.IsComparison() {
		op := map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Min: "min", ir.Max: "max"}[i.Op]
		e.use("core.float." + op)
		return fmt.Sprintf("%s = rt.float_%s_f%d(%s, %s)", d, op, t.U().FloatBits, x, y)
	}
	switch i.Op {
	case ir.Eq, ir.Ne:
		xc, xnil := i.X.(*ir.Const)
		yc, ynil := i.Y.(*ir.Const)
		xnil = xnil && xc.Nil
		ynil = ynil && yc.Nil
		var eq string
		switch {
		case t.U().Kind == ir.KSlice:
			if ynil {
				eq = x + ".a is None"
			} else {
				eq = y + ".a is None"
			}
		case (xnil || ynil) && t.U().Kind != ir.KInterface:
			eq = x + " is " + y
		default:
			eq = e.eqExpr(t, x, y)
		}
		if i.Op == ir.Ne {
			eq = "not (" + eq + ")"
		}
		return d + " = " + eq
	case ir.Lt, ir.Le, ir.Gt, ir.Ge:
		return fmt.Sprintf("%s = %s %s %s", d, x, i.Op, y)
	case ir.Min:
		return fmt.Sprintf("%s = %s if %s < %s else %s", d, y, y, x, x)
	case ir.Max:
		return fmt.Sprintf("%s = %s if %s > %s else %s", d, y, y, x, x)
	}
	if t.U().Kind == ir.KString {
		e.use("core.string.concat")
		return fmt.Sprintf("%s = %s + %s", d, x, y)
	}
	e.use(binContract[i.Op])
	return fmt.Sprintf("%s = rt.%s_%s(%s, %s)", d, binName[i.Op], intKind(i.Dst.Type), x, y)
}

func (fe *fnEmitter) convert(i *ir.Convert) string {
	e := fe.e
	d, x := fe.val(i.Dst), fe.val(i.X)
	switch i.Kind {
	case ir.ConvFloat:
		e.use("core.float.convert")
		u, from := i.Dst.Type.U(), i.X.IRType().U()
		if u.Kind == ir.KFloat {
			if from.Kind == ir.KFloat {
				return fmt.Sprintf("%s = rt.round_float(%s, %d)", d, x, u.FloatBits)
			}
			return fmt.Sprintf("%s = rt.integer_float(%s, %d)", d, x, u.FloatBits)
		}
		signed := "False"
		if u.Int.Signed() {
			signed = "True"
		}
		return fmt.Sprintf("%s = rt.float_integer(%s, %d, %s)", d, x, u.Int.Bits(), signed)
	case ir.ConvNop, ir.ConvIfaceToIface:
		return d + " = " + x
	case ir.ConvInt:
		e.use("core.integer.convert")
		return fmt.Sprintf("%s = rt.to_%s(%s)", d, intKind(i.Dst.Type), x)
	case ir.ConvIntToString:
		e.use("core.string.from_rune")
		return fmt.Sprintf("%s = rt.from_rune(%s)", d, x)
	case ir.ConvStringToBytes:
		e.use("core.string.to_bytes")
		return fmt.Sprintf("%s = rt.to_bytes(%s)", d, x)
	case ir.ConvBytesToString:
		e.use("core.string.from_bytes")
		return fmt.Sprintf("%s = rt.from_bytes(%s)", d, x)
	case ir.ConvStringToRunes:
		e.use("core.string.to_runes")
		return fmt.Sprintf("%s = rt.to_runes(%s)", d, x)
	case ir.ConvRunesToString:
		e.use("core.string.from_runes")
		return fmt.Sprintf("%s = rt.from_runes(%s)", d, x)
	case ir.ConvSliceToArray:
		e.use("core.slice.to_array")
		at := i.Dst.Type.U()
		return fmt.Sprintf("%s = rt.slice_to_array(%s, %d%s)", d, x, at.Len, optArg(e.cloneFn(at.Elem)))
	}
	panic(fmt.Sprintf("py: conversion %d", i.Kind))
}

func (fe *fnEmitter) sliceOp(i *ir.SliceOp) string {
	opt := func(v ir.Value) string {
		if v == nil {
			return "None"
		}
		return fe.val(v)
	}
	d, x := fe.val(i.Dst), fe.val(i.X)
	switch i.X.IRType().U().Kind {
	case ir.KString:
		fe.e.use("core.string.slice")
		return fmt.Sprintf("%s = rt.sslice(%s, %s, %s)", d, x, opt(i.Lo), opt(i.Hi))
	case ir.KPointer:
		fe.e.use("core.slice.slice")
		return fmt.Sprintf("%s = rt.slice_array(rt.nilchk(%s), %s, %s, %s)", d, x, opt(i.Lo), opt(i.Hi), opt(i.Max))
	}
	fe.e.use("core.slice.slice")
	return fmt.Sprintf("%s = rt.reslice(%s, %s, %s, %s)", d, x, opt(i.Lo), opt(i.Hi), opt(i.Max))
}

func (fe *fnEmitter) typeAssert(i *ir.TypeAssert, w func(string, ...any)) {
	e := fe.e
	x := fe.val(i.X)
	d := fe.val(i.Dst)
	if i.T.IsInterface() {
		var ids, names []string
		for _, m := range i.T.U().Methods {
			ids = append(ids, strconv.Quote(m.ID))
			names = append(names, strconv.Quote(m.ID)+": "+strconv.Quote(m.Name))
		}
		test := fmt.Sprintf("(%s is not None and rt.implements_all(%s.t, [%s]) is None)", x, x, strings.Join(ids, ", "))
		if i.Ok != nil {
			w("%s = %s", fe.val(i.Ok), test)
			w("%s = %s if %s else None", d, x, fe.val(i.Ok))
			return
		}
		w("if not %s:", test)
		w("    raise rt.assert_panic(%s, %q, %q, None if %s is None else {%s}[rt.implements_all(%s.t, [%s])])",
			x, ir.TypeString(i.X.IRType()), ir.TypeString(i.T), x, strings.Join(names, ", "), x, strings.Join(ids, ", "))
		w("%s = %s", d, x)
		return
	}
	td := e.td(i.T)
	test := fmt.Sprintf("(%s is not None and %s.t is %s)", x, x, td)
	val := e.cloneExpr(i.T, x+".v")
	if i.Ok != nil {
		w("%s = %s", fe.val(i.Ok), test)
		w("%s = %s if %s else %s", d, val, fe.val(i.Ok), e.zero(i.T))
		return
	}
	w("if not %s:", test)
	w("    raise rt.assert_panic(%s, %q, %q, None)", x, ir.TypeString(i.X.IRType()), ir.TypeString(i.T))
	w("%s = %s", d, val)
}

// A trailing comma keeps a single appended element a tuple.
func comma(es []string) string {
	if len(es) > 0 {
		return ","
	}
	return ""
}

func (e *emitter) symbol(sym string) string { return e.qualify(sym, e.names.Symbol(sym)) }

// qualify refers to name, declared next to sym, from the current module.
func (e *emitter) qualify(sym, name string) string {
	location, ok := e.locations[sym]
	if !ok || location == e.current {
		return name
	}
	if e.imports[e.current] == nil {
		e.imports[e.current] = map[string]bool{}
	}
	e.imports[e.current][location] = true
	return moduleName(location) + "." + name
}
