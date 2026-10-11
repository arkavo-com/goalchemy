// Package ts emits TypeScript for Node.js. Go value semantics are preserved
// with explicit copies, stable struct storage objects, cells for boxed
// scalars, BigInt for 64-bit integers, and binary strings for Go strings.
package ts

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
	"github.com/eugenioenko/goalchemy/internal/sourcemap"
)

type Output struct {
	Files     []artifact.File
	Packages  []artifact.PackageFiles
	Contracts []string
}

const marker = "\x00@"

type emitter struct {
	p         *ir.Program
	names     *naming.Names
	symbols   map[string]string
	contracts map[string]bool
	classes   map[*ir.Type]string // struct types (by underlying) to class names
	arrays    map[*ir.Type]bool   // array types needing helpers
	tds       map[*ir.Type]string // type descriptors
	helperQ   []*ir.Type
	helperOut bytes.Buffer
	seenHelp  map[*ir.Type]bool
	positions []ir.At
	current   string
	locations map[string]string
	globals   map[string]bool
	imports   map[string]map[string]bool
}

// resolveMarkers strips position markers, recording a mapping for each.
func (e *emitter) resolveMarkers(src string) (string, *sourcemap.Map) {
	sm := &sourcemap.Map{File: "main.ts"}
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		for {
			j := strings.Index(ln, marker)
			if j < 0 {
				break
			}
			k := strings.IndexByte(ln[j+len(marker):], 0)
			idx, _ := strconv.Atoi(ln[j+len(marker) : j+len(marker)+k])
			ln = ln[:j] + ln[j+len(marker)+k+1:]
			if e.p.Fset != nil {
				pos := e.p.Fset.Position(e.positions[idx].Pos)
				if pos.IsValid() {
					sm.Add(sourcemap.Mapping{GenLine: i, GenCol: j, Source: pos.Filename, SrcLine: pos.Line - 1, SrcCol: pos.Column - 1})
				}
			}
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n"), sm
}

func (e *emitter) use(c string) {
	if c != "" {
		e.contracts[c] = true
	}
}

// opaqueContracts names a contract whose implementation declares each
// opaque handle type.
var opaqueContracts = map[string]string{
	"sync.Mutex":      "std.sync.mutex.lock",
	"sync.WaitGroup":  "std.sync.waitgroup.add",
	"context.Context": "std.context.err",
	"crypto.Key":      "lib.crypto.close",
	"http.Exchange":   "lib.http.exchange_close",
}

func intKind(t *ir.Type) string { return t.U().Int.String() }

func wide(t *ir.Type) bool {
	k := t.U()
	return k.Kind == ir.KInt && k.Int.Bits() == 64
}

// ---- type helpers ----

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
	n := e.classes[t]
	if n == "" {
		n = e.class(t)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "export class %s {\n", n)
	for i := range t.Fields {
		fmt.Fprintf(&b, "  %s: any;\n", e.fieldProp(t, i))
	}
	b.WriteString("  constructor() {\n")
	for i, f := range t.Fields {
		fmt.Fprintf(&b, "    this.%s = %s;\n", e.fieldProp(t, i), e.zero(f.Type))
	}
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  $clone(): %s {\n    const r = new %s();\n    r.$set(this);\n    return r;\n  }\n", n, n)
	b.WriteString("  $set(o: any): void {\n")
	for i, f := range t.Fields {
		p := e.fieldProp(t, i)
		if f.Type.IsAggregate() {
			fmt.Fprintf(&b, "    %s;\n", e.setStmt(f.Type, "this."+p, "o."+p))
		} else {
			fmt.Fprintf(&b, "    this.%s = o.%s;\n", p, p)
		}
	}
	b.WriteString("  }\n")
	b.WriteString("  $eq(o: any): boolean {\n    return true")
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		p := e.fieldProp(t, i)
		fmt.Fprintf(&b, " && %s", e.eqExpr(f.Type, "this."+p, "o."+p))
	}
	b.WriteString(";\n  }\n")
	b.WriteString("  $key(): string {\n    return \"{\"")
	for i, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		p := e.fieldProp(t, i)
		fmt.Fprintf(&b, " + %s + \",\"", e.keyExpr(f.Type, "this."+p))
	}
	b.WriteString(" + \"}\";\n  }\n}\n\n")
	e.helperOut.WriteString(b.String())
}

func byteElem(t *ir.Type) bool {
	u := t.U()
	return u.Kind == ir.KInt && u.Int == ir.U8
}

func sliceNil(t *ir.Type) string {
	if byteElem(t.U().Elem) {
		return "rt.BYTE_NIL"
	}
	return "rt.NIL"
}

func (e *emitter) arrayHelpers(t *ir.Type) {
	el := t.Elem
	var b strings.Builder
	if byteElem(el) {
		fmt.Fprintf(&b, "export function %s(): Uint8Array { return new Uint8Array(%d); }\n", e.names.Type(t, "zero$"), t.Len)
		fmt.Fprintf(&b, "export function %s(s: Uint8Array): Uint8Array { return s.slice(); }\n", e.names.Type(t, "clone$"))
		fmt.Fprintf(&b, "export function %s(d: Uint8Array, s: Uint8Array): void { d.set(s); }\n", e.names.Type(t, "set$"))
	} else {
		fmt.Fprintf(&b, "export function %s(): any[] {\n  const a = new Array(%d);\n  for (let i = 0; i < %d; i++) a[i] = %s;\n  return a;\n}\n", e.names.Type(t, "zero$"), t.Len, t.Len, e.zero(el))
		fmt.Fprintf(&b, "export function %s(s: any[]): any[] {\n  const a = new Array(%d);\n  for (let i = 0; i < %d; i++) a[i] = %s;\n  return a;\n}\n", e.names.Type(t, "clone$"), t.Len, t.Len, e.cloneExpr(el, "s[i]"))
		fmt.Fprintf(&b, "export function %s(d: any[], s: any[]): void {\n  for (let i = 0; i < %d; i++) ", e.names.Type(t, "set$"), t.Len)
		if el.IsAggregate() {
			fmt.Fprintf(&b, "%s;\n}\n", e.setStmt(el, "d[i]", "s[i]"))
		} else {
			b.WriteString("d[i] = s[i];\n}\n")
		}
	}
	fmt.Fprintf(&b, "export function %s(x: any, y: any): boolean {\n  for (let i = 0; i < %d; i++) if (!(%s)) return false;\n  return true;\n}\n", e.names.Type(t, "eq$"), t.Len, e.eqExpr(el, "x[i]", "y[i]"))
	fmt.Fprintf(&b, "export function %s(x: any): string {\n  let s = \"[\";\n  for (let i = 0; i < %d; i++) s += %s + \",\";\n  return s + \"]\";\n}\n\n", e.names.Type(t, "key$"), t.Len, e.keyExpr(el, "x[i]"))
	e.helperOut.WriteString(b.String())
}

// zero renders an expression producing a fresh zero value of t.
func (e *emitter) zero(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "false"
	case ir.KFloat:
		return "0.0"
	case ir.KInt:
		if u.Int.Bits() == 64 {
			return "0n"
		}
		return "0"
	case ir.KString:
		return `""`
	case ir.KSlice:
		return sliceNil(t)
	case ir.KStruct:
		return "new " + e.class(u) + "()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s()", e.names.Type(u, "zero$"))
	case ir.KOpaque:
		if !u.OpaqueRef {
			e.use(opaqueContracts[u.Name])
			return "new rt." + u.Obj + "()"
		}
	}
	return "null"
}

func (e *emitter) cloneExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KOpaque:
		if u.Kind == ir.KStruct {
			e.class(u)
		}
		return x + ".$clone()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s)", e.names.Type(u, "clone$"), x)
	}
	return x
}

// cloneFn renders a clone function for aggregate element types, or "".
func (e *emitter) cloneFn(t *ir.Type) string {
	if !t.IsAggregate() {
		return ""
	}
	return "(v: any) => " + e.cloneExpr(t, "v")
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
		return fmt.Sprintf("%s(%s, %s)", e.names.Type(u, "set$"), dst, src)
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
		return fmt.Sprintf("%s(%s, %s)", e.names.Type(u, "eq$"), a, b)
	case ir.KInterface:
		return fmt.Sprintf("rt.ifaceEq(%s, %s)", a, b)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("rt.uncomparableEq(%q)", ir.TypeString(t))
	}
	return a + " === " + b
}

func (e *emitter) keyExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KFloat:
		return "rt.floatKey(" + x + ")"
	case ir.KStruct:
		e.class(u)
		return x + ".$key()"
	case ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(%s)", e.names.Type(u, "key$"), x)
	case ir.KInterface:
		return fmt.Sprintf("String(rt.ifaceKey(%s))", x)
	case ir.KString:
		return "JSON.stringify(" + x + ")"
	case ir.KPointer, ir.KChan:
		return fmt.Sprintf("String(rt.objId(%s))", x)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("rt.unhashableKey(%q)", ir.TypeString(t))
	}
	return "String(" + x + ")"
}

// keyFn renders the map key function for key type t.
func (e *emitter) keyFn(t *ir.Type) string {
	switch t.U().Kind {
	case ir.KFloat, ir.KStruct, ir.KArray, ir.KInterface:
		return "(k: any) => " + e.keyExpr(t, "k")
	}
	return "rt.identityKey"
}

func (e *emitter) td(t *ir.Type) string {
	if n, ok := e.tds[t]; ok {
		return n
	}
	n := "$types." + e.names.Type(t, "TD$")
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
		name := ir.TypeString(t)
		u := t.U()
		basic := ""
		switch u.Kind {
		case ir.KInt:
			basic = "int"
		case ir.KBool:
			basic = "bool"
		case ir.KString:
			basic = "string"
		case ir.KFloat:
			basic = u.Basic
		}
		eq := "(a: any, b: any) => " + e.eqExpr(t, "a", "b")
		key := "(a: any) => " + e.keyExpr(t, "a")
		if !t.Comparable() {
			eq = fmt.Sprintf("() => { throw rt.uncomparable(%q); }", name)
			key = fmt.Sprintf("() => { throw rt.unhashable(%q); }", name)
		}
		if t.Kind == ir.KString {
			fmt.Fprintf(b, "%s = rt.STRING_TYPE;\n", e.tds[t])
			continue
		}
		fmt.Fprintf(b, "%s = rt.typeDesc({name: %q, kind: %q, eq: %s, key: %s, methods: {", e.tds[t], name, u.Kind.String(), eq, key)
		for i, m := range t.MethodSet {
			if i > 0 {
				b.WriteString(", ")
			}
			fn := e.symbol(m.Func.Sym)
			if t.IsAggregate() {
				fn = fmt.Sprintf("rt.closure(%d, (r: any, ...a: any[]) => %s(%s, ...a))", m.Func.ID, e.symbol(m.Func.Sym), e.cloneExpr(t, "r"))
			}
			if e.p.SuspMethods[m.ID] && !m.Func.MaySuspend {
				fn = e.adapt(fn, len(m.Func.Results))
			}
			fmt.Fprintf(b, "%q: %s", m.ID, fn)
		}
		b.WriteString("}")
		if basic != "" {
			fmt.Fprintf(b, ", basic: %q", basic)
		}
		if !t.Comparable() {
			b.WriteString(", comparable: false")
		}
		b.WriteString("});\n")
	}
	e.flushHelpers()
}

// ---- functions ----

type fnEmitter struct {
	e      *emitter
	f      *ir.Func
	b      strings.Builder
	names  map[*ir.Local]string
	order  map[*ir.Block]int
	defers bool
	// frame is set when f is emitted as a resumable frame; locals are
	// fields of fr and pc is fr.pc.
	frame bool
}

// susp reports whether values of function type t use the resumable
// calling convention: calling one returns a frame.
func (e *emitter) susp(t *ir.Type) bool { return e.p.SuspTypes[t.U()] }

// adapt wraps an ordinary function value with n results in the resumable
// form.
func (e *emitter) adapt(f string, n int) string {
	e.use("core.task.spawn")
	return fmt.Sprintf("rt.adapt(%s, %d)", f, n)
}

func (fe *fnEmitter) pc() string {
	if fe.frame {
		return "fr.pc"
	}
	return "$pc"
}

func (e *emitter) localName(l *ir.Local) string {
	return e.names.Local(l, "$")
}

func (e *emitter) symbol(sym string) string {
	name := e.names.Symbol(sym)
	location, ok := e.locations[sym]
	if !ok {
		return name
	}
	if e.globals[sym] {
		name = "$globals." + name
	}
	if location == e.current {
		return name
	}
	if e.imports[e.current] == nil {
		e.imports[e.current] = map[string]bool{}
	}
	e.imports[e.current][location] = true
	return moduleName(location) + "." + name
}

func (fe *fnEmitter) w(format string, args ...any) { fmt.Fprintf(&fe.b, format, args...) }

func (fe *fnEmitter) local(l *ir.Local) string {
	if fe.frame {
		return "fr." + fe.e.localName(l)
	}
	return fe.e.localName(l)
}

// cell reports whether l lives in a rt.Cell.
func cell(l *ir.Local) bool { return l.Boxed && !l.Type.IsAggregate() }

func (fe *fnEmitter) val(v ir.Value) string {
	switch v := v.(type) {
	case *ir.Local:
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
	panic(fmt.Sprintf("ts: operand %T", v))
}

func (e *emitter) constant(c *ir.Const) string {
	if c.Nil {
		if c.Type.U().Kind == ir.KSlice {
			return sliceNil(c.Type)
		}
		return "null"
	}
	if c.Type.U().Kind == ir.KFloat {
		return ir.FloatLiteral(c)
	}
	switch c.Val.Kind() {
	case constant.Bool:
		return c.Val.ExactString()
	case constant.String:
		return jsString(constant.StringVal(c.Val))
	case constant.Int:
		if wide(c.Type) {
			return c.Val.ExactString() + "n"
		}
		return c.Val.ExactString()
	}
	panic("ts: constant " + c.Val.String())
}

// jsString renders Go string bytes as a JavaScript binary string literal.
func jsString(s string) string {
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

// rootExpr renders the root of a place as an lvalue-capable expression.
func (fe *fnEmitter) rootExpr(p *ir.Place) string {
	switch r := p.Root.(type) {
	case ir.LocalRoot:
		return fe.val(r.Local)
	case ir.GlobalRoot:
		return fe.globalRef(r.Global)
	case ir.DerefRoot:
		pt := r.Ptr.IRType().U().Elem
		if pt.IsAggregate() {
			return "rt.nilchk(" + fe.val(r.Ptr) + ")"
		}
		return "rt.nilchk(" + fe.val(r.Ptr) + ").v"
	case ir.SliceRoot:
		fe.e.use("core.slice.index")
		return "rt.sget(" + fe.val(r.Slice) + ", " + fe.val(r.Index) + ")"
	case ir.ValueRoot:
		return fe.val(r.Value)
	}
	panic("ts: place root")
}

func (fe *fnEmitter) proj(base string, cur *ir.Type, pr ir.Proj) string {
	if pr.Index != nil {
		return fmt.Sprintf("%s[rt.idx(%s, %d)]", base, fe.val(pr.Index), cur.U().Len)
	}
	return base + "." + fe.e.fieldProp(cur, pr.Field)
}

// placeExpr renders the full place as an expression designating the value.
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
	if last.Index != nil {
		panic("ts: address of array element")
	}
	base := &ir.Place{Root: p.Root, Path: p.Path[:len(p.Path)-1]}
	cur := rootType(p)
	if len(p.Path) > 1 {
		cur = p.Path[len(p.Path)-2].Type
	}
	return fmt.Sprintf("rt.fieldRef(%s, %q)", fe.placeExpr(base), fe.e.fieldProp(cur, last.Field))
}

// frameFunction emits a suspending function as a resumable frame: a class
// holding its locals and resume point, a starter that binds the arguments,
// and a step method running the blocks until return or a pause point.
func (e *emitter) frameFunction(f *ir.Func) string {
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}}
	for i, b := range f.Blocks {
		fe.order[b] = i
	}
	cls := "F$" + e.symbol(f.Sym)
	fe.w("class %s extends rt.Frame {\n", cls)
	for _, l := range f.Locals {
		fe.w("  %s: any;\n", e.localName(l))
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
	fe.w("  results(): any[] {\n    const fr = this;\n    return [%s];\n  }\n", strings.Join(rs, ", "))
	fe.w("  step(t: rt.Task): void {\n    const fr = this;\n    for (;;) switch (fr.pc) {\n")
	for i, b := range f.Blocks {
		fe.w("  case %d:\n", i)
		if b.ResumeOf != nil {
			fe.w("    ")
			fe.resume(b.ResumeOf)
		}
		next := -1
		if i+1 < len(f.Blocks) {
			next = i + 1
		}
		fe.block(b, next)
	}
	fe.w("  default: throw rt.fault(\"bad block\");\n    }\n  }\n}\n")
	var params []string
	for _, l := range f.Env {
		params = append(params, e.localName(l)+": any")
	}
	for _, l := range f.Params {
		params = append(params, e.localName(l)+": any")
	}
	fe.w(e.functionDeclaration(f)+" %s(%s): any {\n  const fr = new %s();\n", e.names.Symbol(f.Sym), strings.Join(params, ", "), cls)
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		fe.w("  fr.%s = %s;\n", e.localName(l), e.localName(l))
	}
	fe.w("  return fr;\n}\n\n")
	return fe.b.String()
}

// resume assigns a paused operation's results from the task.
func (fe *fnEmitter) resume(op ir.Instr) {
	switch i := op.(type) {
	case *ir.Call:
		for k, d := range i.Dsts {
			if d != nil {
				fe.w("%s = t.rv[%d]; ", fe.val(d), k)
			}
		}
	case *ir.Recv:
		if i.Dst != nil {
			fe.w("%s = t.rv[0]; ", fe.val(i.Dst))
		}
		if i.Ok != nil {
			fe.w("%s = t.rv[1]; ", fe.val(i.Ok))
		}
	case *ir.Select:
		fe.w("%s = BigInt(t.rv[0]); ", fe.val(i.Index))
		for k, c := range i.Cases {
			if c.Send || c.Dst == nil && c.Ok == nil {
				continue
			}
			fe.w("if (t.rv[0] === %d) {", k)
			if c.Dst != nil {
				fe.w(" %s = t.rv[1];", fe.val(c.Dst))
			}
			if c.Ok != nil {
				fe.w(" %s = t.rv[2];", fe.val(c.Ok))
			}
			fe.w(" } ")
		}
	}
	fe.w("\n")
}

// pause emits a pause point: record the resume block, start the
// operation, and return to the trampoline.
func (fe *fnEmitter) pause(p *ir.Pause) {
	e := fe.e
	fe.w("    fr.pc = %d;\n    ", fe.order[p.Next])
	switch i := p.Op.(type) {
	case *ir.Call:
		fn, args, _ := fe.callee(i)
		switch i.Kind {
		case ir.CallExtern:
			fe.w("%s(%s);\n", fn, strings.Join(append([]string{"t"}, args...), ", "))
		case ir.CallValue:
			fe.w("rt.call(t, rt.fnchk(%s)(%s));\n", fn, strings.Join(args, ", "))
		default:
			fe.w("rt.call(t, %s(%s));\n", fn, strings.Join(args, ", "))
		}
	case *ir.Send:
		e.use("core.chan.send")
		fe.w("rt.chanSend(t, %s, %s);\n", fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		e.use("core.chan.recv")
		fe.w("rt.chanRecv(t, %s);\n", fe.val(i.Ch))
	case *ir.Select:
		e.use("core.select")
		var cs []string
		for _, c := range i.Cases {
			if c.Send {
				cs = append(cs, fmt.Sprintf("{ch: %s, send: true, val: %s}", fe.val(c.Ch), fe.val(c.V)))
			} else {
				cs = append(cs, fmt.Sprintf("{ch: %s, send: false}", fe.val(c.Ch)))
			}
		}
		fe.w("rt.select(t, [%s], %v);\n", strings.Join(cs, ", "), i.Default)
	default:
		panic(fmt.Sprintf("ts: pause on %T", p.Op))
	}
	fe.w("    return;\n")
}

func (e *emitter) function(f *ir.Func) string {
	if f.MaySuspend {
		return e.frameFunction(f)
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
	var params []string
	for _, l := range f.Env {
		params = append(params, fe.local(l)+": any")
	}
	for _, l := range f.Params {
		params = append(params, fe.local(l)+": any")
	}
	fe.w(e.functionDeclaration(f)+" %s(%s): any {\n", e.names.Symbol(f.Sym), strings.Join(params, ", "))
	isParam := map[*ir.Local]bool{}
	for _, l := range f.Env {
		isParam[l] = true
	}
	for _, l := range f.Params {
		isParam[l] = true
	}
	var decls []string
	for _, l := range f.Locals {
		if !isParam[l] {
			decls = append(decls, fe.local(l))
		}
	}
	if len(decls) > 0 {
		fe.w("  let %s: any;\n", strings.Join(decls, ", "))
	}
	if fe.defers {
		fe.w("  const $d: rt.Deferred[] = [];\n  let $p: any = null;\n  try {\n")
	}
	if len(f.Blocks) == 1 {
		fe.w("  $body: {\n")
		fe.block(f.Blocks[0], -1)
		fe.w("  }\n")
	} else {
		fe.w("  let $pc = 0;\n  $body: for (;;) switch ($pc) {\n")
		for i, b := range f.Blocks {
			fe.w("  case %d:\n", i)
			next := -1
			if i+1 < len(f.Blocks) {
				next = i + 1
			}
			fe.block(b, next)
		}
		fe.w("  default: throw rt.fault(\"bad block\");\n  }\n")
	}
	if fe.defers {
		fe.w("  } catch (e) {\n    $p = rt.catchPanic(e);\n  }\n  rt.runDefers($d, $p);\n  return %s;\n", fe.results())
	}
	fe.w("}\n\n")
	return fe.b.String()
}

func (fe *fnEmitter) results() string {
	rs := fe.f.Results
	var vs []string
	for _, r := range rs {
		v := fe.val(r)
		if r.Boxed && r.Type.IsAggregate() {
			v = fe.e.cloneExpr(r.Type, v)
		}
		vs = append(vs, v)
	}
	switch len(vs) {
	case 0:
		return "undefined"
	case 1:
		return vs[0]
	}
	return "[" + strings.Join(vs, ", ") + "]"
}

func (fe *fnEmitter) block(b *ir.Block, next int) {
	for _, in := range b.Instrs {
		fe.w("    ")
		fe.mark(in.Position())
		fe.instr(in)
	}
	fe.mark(b.Term.Position())
	fe.term(b.Term, next)
}

func (fe *fnEmitter) mark(pos token.Pos) {
	if !pos.IsValid() {
		return
	}
	fe.e.positions = append(fe.e.positions, ir.At{Pos: pos})
	fe.w("%s%d\x00", marker, len(fe.e.positions)-1)
}

func (fe *fnEmitter) goTo(target *ir.Block, next int) {
	if fe.order[target] == next {
		return
	}
	fe.w("    %s = %d; continue;\n", fe.pc(), fe.order[target])
}

func (fe *fnEmitter) term(t ir.Terminator, next int) {
	switch t := t.(type) {
	case *ir.Jump:
		fe.goTo(t.Target, next)
	case *ir.If:
		fe.w("    if (%s) { %s = %d; continue; }\n", fe.val(t.Cond), fe.pc(), fe.order[t.Then])
		fe.goTo(t.Else, next)
	case *ir.Pause:
		fe.pause(t)
	case *ir.Return:
		if fe.frame {
			fe.w("    rt.ret(t, fr);\n    return;\n")
			return
		}
		if fe.defers {
			fe.w("    break $body;\n")
		} else {
			fe.w("    return %s;\n", fe.results())
		}
	case *ir.Panic:
		fe.w("    throw rt.goPanic(%s);\n", fe.val(t.X))
	case *ir.PanicRuntime:
		fe.w("    throw rt.plainPanic(%s);\n", jsString(t.Msg))
	case *ir.Unreachable:
		fe.w("    throw rt.fault(\"unreachable\");\n")
	default:
		panic(fmt.Sprintf("ts: terminator %T", t))
	}
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
		return fmt.Sprintf("rt.ichk(%s).t.methods[%q]", x, c.Method), append([]string{x + ".v"}, args...), "undefined"
	case ir.CallExtern:
		fe.e.use(c.Extern.Contract)
		for k, a := range c.Args {
			if u := a.IRType().U(); u.Kind == ir.KSlice && u.Elem.U().Kind == ir.KFunc && !fe.e.susp(u.Elem) {
				fe.e.use("core.task.spawn")
				args[k] = fmt.Sprintf("rt.adaptSlice(%s, %d)", args[k], len(u.Elem.U().Results))
			}
		}
		sym := "rt." + externSymbol(c.Extern.Contract)
		if m, ok := fe.e.symbols[c.Extern.Contract]; ok {
			sym = m
		}
		return sym, args, "undefined"
	}
	panic("ts: call kind")
}

func externSymbol(contract string) string {
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
	return b.String()
}

func (fe *fnEmitter) dsts(ds []*ir.Local) string {
	if len(ds) == 1 {
		return fe.val(ds[0])
	}
	var s []string
	for _, d := range ds {
		s = append(s, fe.val(d))
	}
	return "[" + strings.Join(s, ", ") + "]"
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
			fe.w("%s = new rt.Cell(%s);\n", fe.local(i.L), init)
		} else {
			fe.w("%s = %s;\n", fe.local(i.L), init)
		}
	case *ir.Assign:
		fe.w("%s = %s;\n", fe.val(i.Dst), fe.val(i.Src))
	case *ir.Zero:
		fe.w("%s = %s;\n", fe.val(i.Dst), e.zero(i.Dst.Type))
	case *ir.Load:
		x := fe.placeExpr(i.Place)
		if i.Dst.Type.IsAggregate() {
			x = e.cloneExpr(i.Dst.Type, x)
		}
		fe.w("%s = %s;\n", fe.val(i.Dst), x)
	case *ir.Store:
		fe.w("%s;\n", fe.store(i.Place, fe.val(i.V)))
	case *ir.AddrOf:
		fe.w("%s = %s;\n", fe.val(i.Dst), fe.addrOf(i.Place))
	case *ir.New:
		el := i.Dst.Type.U().Elem
		if el.IsAggregate() {
			fe.w("%s = %s;\n", fe.val(i.Dst), e.zero(el))
		} else {
			fe.w("%s = new rt.Cell(%s);\n", fe.val(i.Dst), e.zero(el))
		}
	case *ir.UnOp:
		x := fe.val(i.X)
		switch i.Op {
		case ir.Not:
			fe.w("%s = !%s;\n", fe.val(i.Dst), x)
		case ir.Neg:
			if i.Dst.Type.U().Kind == ir.KFloat {
				e.use("core.float.neg")
				fe.w("%s = rt.floatNeg_f%d(%s);\n", fe.val(i.Dst), i.Dst.Type.U().FloatBits, x)
				break
			}
			e.use("core.integer.neg")
			fe.w("%s = rt.neg_%s(%s);\n", fe.val(i.Dst), intKind(i.Dst.Type), x)
		case ir.BitNot:
			e.use("core.integer.not")
			fe.w("%s = rt.not_%s(%s);\n", fe.val(i.Dst), intKind(i.Dst.Type), x)
		}
	case *ir.BinOp:
		fe.binop(i)
	case *ir.Convert:
		fe.convert(i)
	case *ir.MakeInterface:
		fe.w("%s = rt.box(%s, %s);\n", fe.val(i.Dst), e.td(i.X.IRType()), fe.val(i.X))
	case *ir.TypeAssert:
		fe.typeAssert(i)
	case *ir.Call:
		fn, args, _ := fe.callee(i)
		as := strings.Join(args, ", ")
		call := fn + "(" + as + ")"
		if i.Kind == ir.CallValue {
			call = "rt.fnchk(" + fn + ")(" + as + ")"
		}
		if i.Kind == ir.CallExtern {
			for k, d := range i.Dsts {
				if d.Type.U().Kind == ir.KFunc && e.susp(d.Type) {
					fe.w("{ const $r = %s; ", call)
					for k2, d2 := range i.Dsts {
						v := fmt.Sprintf("$r[%d]", k2)
						if len(i.Dsts) == 1 {
							v = "$r"
						}
						if d2.Type.U().Kind == ir.KFunc && e.susp(d2.Type) {
							v = e.adapt(v, len(d2.Type.U().Results))
						}
						fe.w("%s = %s; ", fe.val(d2), v)
					}
					fe.w("}\n")
					_ = k
					return
				}
			}
		}
		if len(i.Dsts) > 0 {
			fe.w("%s = %s;\n", fe.dsts(i.Dsts), call)
		} else {
			fe.w("%s;\n", call)
		}
	case *ir.MakeClosure:
		var ps, as []string
		for j, l := range i.Env {
			ps = append(ps, fmt.Sprintf("e%d: any", j))
			as = append(as, fe.local(l))
		}
		inner := make([]string, 0, len(i.Env)+1)
		for j := range i.Env {
			inner = append(inner, fmt.Sprintf("e%d", j))
		}
		inner = append(inner, "...a")
		v := fmt.Sprintf("rt.closure(%d, ((%s) => (...a: any[]) => %s(%s))(%s))", i.Func.ID,
			strings.Join(ps, ", "), fe.e.symbol(i.Func.Sym), strings.Join(inner, ", "), strings.Join(as, ", "))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		fe.w("%s = %s;\n", fe.val(i.Dst), v)
	case *ir.MakeBound:
		v := fmt.Sprintf("rt.bound(%d, %s, %s)", i.Func.ID, fe.e.symbol(i.Func.Sym), fe.val(i.Recv))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		fe.w("%s = %s;\n", fe.val(i.Dst), v)
	case *ir.MakeIfaceBound:
		v := fmt.Sprintf("rt.ibound(%s, %q)", fe.val(i.Recv), i.Method)
		if e.susp(i.Dst.Type) && !e.p.SuspMethods[i.Method] {
			v = e.adapt(v, len(i.Dst.Type.U().Results))
		}
		fe.w("%s = %s;\n", fe.val(i.Dst), v)
	case *ir.Len:
		fe.w("%s = %s;\n", fe.val(i.Dst), fe.lenExpr(i.X, false))
	case *ir.Cap:
		fe.w("%s = %s;\n", fe.val(i.Dst), fe.lenExpr(i.X, true))
	case *ir.MakeSlice:
		e.use("core.slice.make")
		fe.w("%s = rt.makeSlice(%s, %s, () => %s, %t);\n", fe.val(i.Dst), fe.val(i.Len), fe.val(i.Cap), e.zero(i.Dst.Type.U().Elem), byteElem(i.Dst.Type.U().Elem))
	case *ir.MakeMap:
		e.use("core.map.make")
		fe.w("%s = rt.makeMap(%s);\n", fe.val(i.Dst), e.keyFn(i.Dst.Type.U().Key))
	case *ir.Append:
		e.use("core.slice.append")
		el := i.Dst.Type.U().Elem
		cl := e.cloneFn(el)
		var v string
		switch {
		case i.Spread != nil && i.Spread.IRType().U().Kind == ir.KString:
			v = fmt.Sprintf("rt.appendString(%s, %s)", fe.val(i.S), fe.val(i.Spread))
		case i.Spread != nil:
			v = fmt.Sprintf("rt.appendSlice(%s, %s%s)", fe.val(i.S), fe.val(i.Spread), optArg(cl))
		default:
			var es []string
			for _, x := range i.Elems {
				es = append(es, fe.val(x))
			}
			v = fmt.Sprintf("rt.append(%s, [%s]%s)", fe.val(i.S), strings.Join(es, ", "), optArg(cl))
		}
		fe.w("%s = rt.zeroAppendGrowth(%s, %s, () => %s);\n", fe.val(i.Dst), v, fe.val(i.S), e.zero(el))
	case *ir.Copy:
		e.use("core.slice.copy")
		if i.Src.IRType().U().Kind == ir.KString {
			fe.w("%s = rt.copyString(%s, %s);\n", fe.val(i.N), fe.val(i.Dst), fe.val(i.Src))
		} else {
			fe.w("%s = rt.copy(%s, %s%s);\n", fe.val(i.N), fe.val(i.Dst), fe.val(i.Src), optArg(e.cloneFn(i.Dst.IRType().U().Elem)))
		}
	case *ir.IndexString:
		e.use("core.string.index")
		fe.w("%s = rt.sindex(%s, %s);\n", fe.val(i.Dst), fe.val(i.S), fe.val(i.I))
	case *ir.SliceOp:
		fe.sliceOp(i)
	case *ir.MapLookup:
		e.use("core.map.lookup")
		m := i.M.IRType().U()
		ok := "$ok"
		if i.Ok != nil {
			ok = fe.val(i.Ok)
		}
		dst := fe.val(i.Dst)
		fe.w("{ let $ok; [%s, %s] = rt.mapGet(%s, %s, %s, () => %s); }\n", dst, ok, fe.val(i.M), fe.val(i.K), e.keyFn(m.Key), e.zero(m.Elem))
		if m.Elem.IsAggregate() {
			fe.w("    %s = %s;\n", dst, e.cloneExpr(m.Elem, dst))
		}
	case *ir.MapStore:
		e.use("core.map.store")
		fe.w("rt.mapSet(%s, %s, %s);\n", fe.val(i.M), fe.val(i.K), fe.val(i.V))
	case *ir.MapDelete:
		e.use("core.map.delete")
		fe.w("rt.mapDelete(%s, %s, %s);\n", fe.val(i.M), fe.val(i.K), e.keyFn(i.M.IRType().U().Key))
	case *ir.Clear:
		if i.X.IRType().U().Kind == ir.KMap {
			e.use("core.map.clear")
			fe.w("rt.mapClear(%s);\n", fe.val(i.X))
		} else {
			e.use("core.slice.clear")
			fe.w("rt.clearSlice(%s, () => %s);\n", fe.val(i.X), e.zero(i.X.IRType().U().Elem))
		}
	case *ir.DecodeRune:
		e.use("core.string.decode_rune")
		fe.w("[%s, %s] = rt.decodeRune(%s, %s);\n", fe.val(i.Rune), fe.val(i.Width), fe.val(i.S), fe.val(i.I))
	case *ir.MapIterInit:
		e.use("core.map.iterate")
		fe.w("%s = rt.mapIter(%s);\n", fe.val(i.Iter), fe.val(i.M))
	case *ir.MapIterNext:
		m := i.Iter.Type.Elem.U()
		fe.w("%s = rt.mapNext(%s);\n", fe.val(i.Ok), fe.val(i.Iter))
		if i.Key != nil {
			fe.w("    %s = %s;\n", fe.val(i.Key), e.cloneExpr(m.Key, fe.val(i.Iter)+".k"))
		}
		if i.Val != nil {
			fe.w("    %s = %s;\n", fe.val(i.Val), e.cloneExpr(m.Elem, fe.val(i.Iter)+".v"))
		}
	case *ir.Print:
		e.use("core.print")
		var as []string
		for _, a := range i.Args {
			v := fe.val(a)
			if a.IRType().U().Kind == ir.KFloat {
				v = fmt.Sprintf("rt.floatPrint(%s, %d)", v, a.IRType().U().FloatBits)
			}
			as = append(as, v)
		}
		fe.w("rt.print([%s], %v);\n", strings.Join(as, ", "), i.Newline)
	case *ir.Defer:
		fn, args, fid := fe.callee(i.Call)
		if i.Call.Kind == ir.CallInterface {
			panic("ts: defer of interface call")
		}
		if fe.frame {
			if i.Call.Kind == ir.CallExtern && i.Call.Suspends {
				fn = "(...a:any[])=>rt.nativeFrame((t:rt.Task)=>" + fn + "(t,...a))"
			}
			fe.w("fr.defers.push({f: %s, args: [%s], fid: %s, start: %v});\n", fn, strings.Join(args, ", "), fid, i.Call.Suspends)
			return
		}
		fe.w("$d.push({f: %s, args: [%s], fid: %s});\n", fn, strings.Join(args, ", "), fid)
	case *ir.Recover:
		fe.w("%s = rt.recover(%d);\n", fe.val(i.Dst), fe.f.ID)
	case *ir.Rebind:
		if cell(i.L) {
			fe.w("%s = new rt.Cell(%s.v);\n", fe.local(i.L), fe.local(i.L))
		} else if i.L.Type.IsAggregate() {
			fe.w("%s = %s;\n", fe.local(i.L), e.cloneExpr(i.L.Type, fe.local(i.L)))
		} else {
			fe.w(";\n")
		}
	case *ir.MakeChan:
		e.use("core.chan.make")
		fe.w("%s = rt.makeChan(%s, () => %s);\n", fe.val(i.Dst), fe.val(i.Size), e.zero(i.Dst.Type.U().Elem))
	case *ir.Close:
		e.use("core.chan.close")
		fe.w("rt.chanClose(%s);\n", fe.val(i.Ch))
	case *ir.Go:
		e.use("core.task.spawn")
		fn, args, _ := fe.callee(i.Call)
		if i.Call.Kind == ir.CallInterface {
			panic("ts: go statement with interface call")
		}
		if i.Call.Kind == ir.CallValue {
			fn = "rt.fnchk(" + fn + ")"
		}
		var ps []string
		for k := range args {
			ps = append(ps, fmt.Sprintf("a%d", k))
		}
		call := fmt.Sprintf("f(%s)", strings.Join(ps, ", "))
		if i.Call.Kind == ir.CallExtern && i.Call.Suspends {
			call = fmt.Sprintf("rt.nativeFrame((t:rt.Task)=>f(t%s))", optArg(strings.Join(ps, ", ")))
		} else if !i.Call.Suspends {
			call = fmt.Sprintf("rt.sync(() => { f(%s); return []; })", strings.Join(ps, ", "))
		}
		fe.w("rt.spawn(((f: any, %s) => %s)(%s, %s));\n", strings.Join(append([]string{}, ps...), ", "), call, fn, strings.Join(args, ", "))
	case *ir.BoxParam:
		if cell(i.L) {
			fe.w("%s = new rt.Cell(%s);\n", fe.local(i.L), fe.local(i.L))
		} else {
			fe.w(";\n")
		}
	default:
		panic(fmt.Sprintf("ts: unsupported instruction %T", in))
	}
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
		return "BigInt(" + v + ".length)"
	case ir.KSlice:
		if isCap {
			return "BigInt(" + v + ".c)"
		}
		return "BigInt(" + v + ".l)"
	case ir.KArray:
		return fmt.Sprintf("%dn", t.Len)
	case ir.KPointer:
		return fmt.Sprintf("%dn", t.Elem.U().Len)
	case ir.KMap:
		fe.e.use("core.map.len")
		return "rt.mapLen(" + v + ")"
	case ir.KChan:
		if isCap {
			fe.e.use("core.chan.cap")
			return "rt.chanCap(" + v + ")"
		}
		fe.e.use("core.chan.len")
		return "rt.chanLen(" + v + ")"
	}
	panic("ts: len of " + t.String())
}

func (fe *fnEmitter) binop(i *ir.BinOp) {
	e := fe.e
	d := fe.val(i.Dst)
	x, y := fe.val(i.X), fe.val(i.Y)
	t := i.X.IRType()
	if t.U().Kind == ir.KFloat && !i.Op.IsComparison() {
		op := map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Min: "min", ir.Max: "max"}[i.Op]
		e.use("core.float." + op)
		fe.w("%s = rt.float%s_f%d(%s, %s);\n", d, strings.ToUpper(op[:1])+op[1:], t.U().FloatBits, x, y)
		return
	}
	switch i.Op {
	case ir.Eq, ir.Ne:
		var eq string
		xc, xnil := i.X.(*ir.Const)
		yc, ynil := i.Y.(*ir.Const)
		xnil = xnil && xc.Nil
		ynil = ynil && yc.Nil
		switch {
		case (xnil || ynil) && t.U().Kind != ir.KSlice && t.U().Kind != ir.KInterface:
			eq = x + " === " + y
		case t.U().Kind == ir.KSlice:
			if c, ok := i.Y.(*ir.Const); ok && c.Nil {
				eq = x + ".a === null"
			} else {
				eq = y + ".a === null"
			}
		default:
			eq = e.eqExpr(t, x, y)
		}
		if i.Op == ir.Ne {
			eq = "!(" + eq + ")"
		}
		fe.w("%s = %s;\n", d, eq)
		return
	case ir.Lt, ir.Le, ir.Gt, ir.Ge:
		fe.w("%s = %s %s %s;\n", d, x, i.Op, y)
		return
	case ir.Min:
		fe.w("%s = %s < %s ? %s : %s;\n", d, y, x, y, x)
		return
	case ir.Max:
		fe.w("%s = %s > %s ? %s : %s;\n", d, y, x, y, x)
		return
	}
	if t.U().Kind == ir.KString {
		e.use("core.string.concat")
		fe.w("%s = %s + %s;\n", d, x, y)
		return
	}
	e.use(binContract[i.Op])
	fe.w("%s = rt.%s_%s(%s, %s);\n", d, binName[i.Op], intKind(i.Dst.Type), x, y)
}

func (fe *fnEmitter) convert(i *ir.Convert) {
	e := fe.e
	d, x := fe.val(i.Dst), fe.val(i.X)
	switch i.Kind {
	case ir.ConvFloat:
		e.use("core.float.convert")
		u, from := i.Dst.Type.U(), i.X.IRType().U()
		if u.Kind == ir.KFloat {
			if from.Kind == ir.KFloat {
				fe.w("%s = rt.roundFloat(%s, %d);\n", d, x, u.FloatBits)
			} else {
				fe.w("%s = rt.integerFloat(%s, %d);\n", d, x, u.FloatBits)
			}
		} else {
			fe.w("%s = rt.floatInteger(%s, %d, %t);\n", d, x, u.Int.Bits(), u.Int.Signed())
		}
	case ir.ConvNop, ir.ConvIfaceToIface:
		fe.w("%s = %s;\n", d, x)
	case ir.ConvInt:
		e.use("core.integer.convert")
		fe.w("%s = rt.to_%s(%s);\n", d, intKind(i.Dst.Type), x)
	case ir.ConvIntToString:
		e.use("core.string.from_rune")
		fe.w("%s = rt.fromRune(%s);\n", d, x)
	case ir.ConvStringToBytes:
		e.use("core.string.to_bytes")
		fe.w("%s = rt.toBytes(%s);\n", d, x)
	case ir.ConvBytesToString:
		e.use("core.string.from_bytes")
		fe.w("%s = rt.fromBytes(%s);\n", d, x)
	case ir.ConvStringToRunes:
		e.use("core.string.to_runes")
		fe.w("%s = rt.toRunes(%s);\n", d, x)
	case ir.ConvRunesToString:
		e.use("core.string.from_runes")
		fe.w("%s = rt.fromRunes(%s);\n", d, x)
	case ir.ConvSliceToArray:
		e.use("core.slice.to_array")
		at := i.Dst.Type.U()
		fe.w("%s = rt.sliceToArray(%s, %d%s);\n", d, x, at.Len, optArg(e.cloneFn(at.Elem)))
	default:
		panic(fmt.Sprintf("ts: conversion %d", i.Kind))
	}
}

func (fe *fnEmitter) sliceOp(i *ir.SliceOp) {
	opt := func(v ir.Value) string {
		if v == nil {
			return "undefined"
		}
		return fe.val(v)
	}
	d, x := fe.val(i.Dst), fe.val(i.X)
	switch i.X.IRType().U().Kind {
	case ir.KString:
		fe.e.use("core.string.slice")
		fe.w("%s = rt.sslice(%s, %s, %s);\n", d, x, opt(i.Lo), opt(i.Hi))
	case ir.KPointer:
		fe.e.use("core.slice.slice")
		fe.w("%s = rt.sliceArray(rt.nilchk(%s), %s, %s, %s);\n", d, x, opt(i.Lo), opt(i.Hi), opt(i.Max))
	default:
		fe.e.use("core.slice.slice")
		fe.w("%s = rt.reslice(%s, %s, %s, %s);\n", d, x, opt(i.Lo), opt(i.Hi), opt(i.Max))
	}
}

func (fe *fnEmitter) typeAssert(i *ir.TypeAssert) {
	e := fe.e
	x := fe.val(i.X)
	d := fe.val(i.Dst)
	if i.T.IsInterface() {
		var ids, names []string
		for _, m := range i.T.U().Methods {
			ids = append(ids, strconv.Quote(m.ID))
			names = append(names, strconv.Quote(m.ID)+": "+strconv.Quote(m.Name))
		}
		test := fmt.Sprintf("(%s !== null && rt.implementsAll(%s.t, [%s]) === null)", x, x, strings.Join(ids, ", "))
		if i.Ok != nil {
			fe.w("%s = %s;\n    %s = %s ? %s : null;\n", fe.val(i.Ok), test, d, fe.val(i.Ok), x)
			return
		}
		fe.w("if (!%s) throw rt.assertPanic(%s, %q, %q, %s === null ? null : ({%s} as any)[rt.implementsAll(%s.t, [%s])!]);\n",
			test, x, ir.TypeString(i.X.IRType()), ir.TypeString(i.T), x, strings.Join(names, ", "), x, strings.Join(ids, ", "))
		fe.w("    %s = %s;\n", d, x)
		return
	}
	td := e.td(i.T)
	test := fmt.Sprintf("(%s !== null && %s.t === %s)", x, x, td)
	val := e.cloneExpr(i.T, x+".v")
	if i.Ok != nil {
		fe.w("%s = %s;\n    %s = %s ? %s : %s;\n", fe.val(i.Ok), test, d, fe.val(i.Ok), val, e.zero(i.T))
		return
	}
	fe.w("if (!%s) throw rt.assertPanic(%s, %q, %q, null);\n    %s = %s;\n", test, x, ir.TypeString(i.X.IRType()), ir.TypeString(i.T), d, val)
}

func (e *emitter) functionDeclaration(f *ir.Func) string {
	if f == e.p.Init || f == e.p.Entry {
		return "function"
	}
	return "export function"
}
