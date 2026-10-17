// Package rust emits Rust (edition 2021). Every Go value is a dynamic rt::V;
// shared storage lives in the runtime's traced heap. Each function keeps its
// locals in a registered root frame (Fr), so the collector sees every live
// value at safepoints: function entry and the top of each dispatch loop.
// Suspending functions compile to resumable frames with a step function.
package rust

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
	tds       map[*ir.Type]bool
	helperQ   []*ir.Type
	helperOut bytes.Buffer
	seenHelp  map[*ir.Type]bool
	zeros     map[*ir.Type]bool
	externs   map[string]*ir.Extern
	layout    *artifact.Layout
	current   string
	locations map[string]string
	helpers   map[string]*bytes.Buffer
}

const marker = "\x00@"

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

func intKind(t *ir.Type) string { return t.U().Int.String() }

func unsigned64(t *ir.Type) bool {
	u := t.U()
	return u.Kind == ir.KInt && u.Int == ir.U64
}

func (e *emitter) susp(t *ir.Type) bool { return e.p.SuspTypes[t.U()] }

func (e *emitter) adapt(f string, n int) string {
	e.use("core.task.spawn")
	return fmt.Sprintf("adapt(%s, %d)", f, n)
}

// ---- type helpers ----

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
		previous := e.current
		e.current = e.helperScope(t)
		e.helperOut.Reset()
		switch t.Kind {
		case ir.KStruct:
			e.structHelpers(t)
		case ir.KArray:
			e.arrayHelpers(t)
		}
		e.helpers[e.current].Write(e.helperOut.Bytes())
		e.current = previous
	}
}

func (e *emitter) structHelpers(t *ir.Type) {
	var b strings.Builder
	var zs, cs []string
	for k, f := range t.Fields {
		zs = append(zs, e.zero(f.Type))
		cs = append(cs, e.cloneExpr(f.Type, fmt.Sprintf("_v[%d]", k), true))
	}
	fmt.Fprintf(&b, "pub(super) fn %s() -> V {\n    vals(vec![%s])\n}\n\n", e.names.Type(t, "z_"), strings.Join(zs, ", "))
	fmt.Fprintf(&b, "pub(super) fn %s(_x: &V) -> V {\n    let _v = vals_copy(_x.h());\n    vals(vec![%s])\n}\n\n", e.names.Type(t, "c_"), strings.Join(cs, ", "))
	fmt.Fprintf(&b, "pub(super) fn %s(_d: &V, _s: &V) {\n", e.names.Type(t, "set_"))
	for k, f := range t.Fields {
		src := fmt.Sprintf("fld(_s, %d)", k)
		if f.Type.IsAggregate() {
			fmt.Fprintf(&b, "    %s;\n", e.setStmt(f.Type, fmt.Sprintf("fld(_d, %d)", k), src))
		} else {
			fmt.Fprintf(&b, "    fset(_d, %d, %s);\n", k, src)
		}
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "pub(super) fn %s(_a: &V, _b: &V) -> bool {\n    true", e.names.Type(t, "eq_"))
	for k, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		fmt.Fprintf(&b, " && %s", e.eqExpr(f.Type, fmt.Sprintf("fld(_a, %d)", k), fmt.Sprintf("fld(_b, %d)", k)))
	}
	b.WriteString("\n}\n\n")
	var ks []string
	for k, f := range t.Fields {
		if f.Name == "_" {
			continue
		}
		ks = append(ks, e.keyExpr(f.Type, fmt.Sprintf("fld(_a, %d)", k)))
	}
	fmt.Fprintf(&b, "pub(super) fn %s(_a: &V) -> Key {\n    key_list(vec![%s])\n}\n\n", e.names.Type(t, "k_"), strings.Join(ks, ", "))
	e.helperOut.WriteString(b.String())
}

func byteElem(t *ir.Type) bool {
	u := t.U()
	return u.Kind == ir.KInt && u.Int == ir.U8
}

func (e *emitter) arrayHelpers(t *ir.Type) {
	el, n := t.Elem, t.Len
	z, c, set, eq, k := e.names.Type(t, "z_"), e.names.Type(t, "c_"), e.names.Type(t, "set_"), e.names.Type(t, "eq_"), e.names.Type(t, "k_")
	var b strings.Builder
	if byteElem(el) {
		fmt.Fprintf(&b, "pub(super) fn %s() -> V { byte_array(vec![0; %d]) }\n", z, n)
		fmt.Fprintf(&b, "pub(super) fn %s(_x: &V) -> V { byte_array_clone(_x) }\n", c)
		fmt.Fprintf(&b, "pub(super) fn %s(_d: &V, _s: &V) { byte_array_set(_d, _s) }\n", set)
		fmt.Fprintf(&b, "pub(super) fn %s(_a: &V, _b: &V) -> bool { byte_array_eq(_a, _b) }\n", eq)
		fmt.Fprintf(&b, "pub(super) fn %s(_a: &V) -> Key { byte_array_key(_a) }\n\n", k)
		e.helperOut.WriteString(b.String())
		return
	}
	fmt.Fprintf(&b, "pub(super) fn %s() -> V {\n    vals((0..%d).map(|_| %s).collect())\n}\n\n", z, n, e.zero(el))
	fmt.Fprintf(&b, "pub(super) fn %s(_x: &V) -> V {\n    let _v = vals_copy(_x.h());\n    vals(_v.iter().map(|_x| %s).collect())\n}\n\n", c, e.cloneExpr(el, "_x", true))
	if el.IsAggregate() {
		fmt.Fprintf(&b, "pub(super) fn %s(_d: &V, _s: &V) {\n    for _i in 0..%d {\n        %s;\n    }\n}\n\n", set, n, e.setStmt(el, "slot(_d.h(), _i)", "slot(_s.h(), _i)"))
	} else {
		fmt.Fprintf(&b, "pub(super) fn %s(_d: &V, _s: &V) {\n    for _i in 0..%d {\n        set_slot(_d.h(), _i, slot(_s.h(), _i));\n    }\n}\n\n", set, n)
	}
	fmt.Fprintf(&b, "pub(super) fn %s(_a: &V, _b: &V) -> bool {\n    (0..%d).all(|_i| %s)\n}\n\n", eq, n, e.eqExpr(el, "slot(_a.h(), _i)", "slot(_b.h(), _i)"))
	fmt.Fprintf(&b, "pub(super) fn %s(_a: &V) -> Key {\n    key_list((0..%d).map(|_i| %s).collect())\n}\n\n", k, n, e.keyExpr(el, "slot(_a.h(), _i)"))
	e.helperOut.WriteString(b.String())
}

// zero renders an expression producing a fresh zero value of t.
func (e *emitter) zero(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "V::Bool(false)"
	case ir.KFloat:
		return "V::Float(0.0)"
	case ir.KInt:
		return "V::Int(0)"
	case ir.KString:
		return `s(b"")`
	case ir.KSlice:
		if byteElem(u.Elem) {
			return "BYTE_NIL"
		}
		return "NIL_SLICE"
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s()", e.helper(u, "z_"))
	case ir.KOpaque:
		e.use(opaqueContracts[u.Name])
		if !u.OpaqueRef {
			switch u.Name {
			case "sync.Mutex":
				return "new_mutex()"
			case "sync.WaitGroup":
				return "new_waitgroup()"
			}
		}
	}
	return "V::Nil"
}

// zeroFn renders a fn() -> V producing zero values of t.
func (e *emitter) zeroFn(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KBool:
		return "zero_bool"
	case ir.KFloat:
		return "zero_float"
	case ir.KInt:
		return "zero_int"
	case ir.KString:
		return "zero_string"
	case ir.KSlice:
		if byteElem(u.Elem) {
			return "zero_byte_slice"
		}
		return "zero_slice"
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return e.helper(u, "z_")
	case ir.KOpaque:
		if !u.OpaqueRef {
			e.use(opaqueContracts[u.Name])
			if u.Name == "sync.Mutex" {
				return "new_mutex"
			}
			return "new_waitgroup"
		}
	}
	return "zero_nil"
}

// cloneExpr copies a value of t; ref reports that x is a place expression
// that must be cloned for non-aggregates.
func (e *emitter) cloneExpr(t *ir.Type, x string, ref bool) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(&%s)", e.helper(u, "c_"), x)
	case ir.KOpaque:
		if !u.OpaqueRef {
			return "opaque_clone(&" + x + ")"
		}
	}
	if ref {
		return x + ".clone()"
	}
	return x
}

func (e *emitter) cloneFn(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("Some(%s as fn(&V) -> V)", e.helper(u, "c_"))
	case ir.KOpaque:
		if !u.OpaqueRef {
			return "Some(opaque_clone as fn(&V) -> V)"
		}
	}
	return "None"
}

func (e *emitter) setStmt(t *ir.Type, dst, src string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(&%s, &%s)", e.helper(u, "set_"), dst, src)
	case ir.KOpaque:
		return fmt.Sprintf("opaque_set(&%s, &%s)", dst, src)
	}
	panic("rust: set of non-aggregate")
}

// eqExpr renders a Rust bool comparing two values of t.
func (e *emitter) eqExpr(t *ir.Type, a, b string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(&%s, &%s)", e.helper(u, "eq_"), a, b)
	case ir.KInterface:
		return fmt.Sprintf("ifeq(&%s, &%s)", a, b)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("eq_uncomparable(%q)", ir.TypeString(t))
	}
	return fmt.Sprintf("veq(&%s, &%s)", a, b)
}

func (e *emitter) keyExpr(t *ir.Type, x string) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return fmt.Sprintf("%s(&%s)", e.helper(u, "k_"), x)
	case ir.KInterface:
		return fmt.Sprintf("ikey(&%s)", x)
	case ir.KSlice, ir.KMap, ir.KFunc:
		return fmt.Sprintf("key_unhashable(%q)", ir.TypeString(t))
	}
	return fmt.Sprintf("vkey(&%s)", x)
}

func (e *emitter) keyFn(t *ir.Type) string {
	u := t.U()
	switch u.Kind {
	case ir.KStruct, ir.KArray:
		e.needHelpers(u)
		return e.helper(u, "k_")
	case ir.KInterface:
		return "ikey"
	}
	return "vkey"
}

// td renders a reference to the descriptor of t.
func (e *emitter) td(t *ir.Type) string {
	if t.Kind == ir.KString {
		return "&STRING_TYPE"
	}
	if !e.tds[t] {
		e.tds[t] = true
	}
	return "&" + e.reference("shared", e.names.Type(t, "td_"))
}

func (e *emitter) typeDescs(b *bytes.Buffer) {
	var ts []*ir.Type
	for t := range e.tds {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for _, t := range ts {
		if t.Kind == ir.KString {
			continue
		}
		tdeq, tdkey := e.names.Type(t, "tdeq_"), e.names.Type(t, "tdkey_")
		name := ir.TypeString(t)
		u := t.U()
		basic := ""
		switch u.Kind {
		case ir.KInt:
			basic = "int"
			if u.Int == ir.U64 {
				basic = "uint"
			}
		case ir.KBool:
			basic = "bool"
		case ir.KString:
			basic = "string"
		case ir.KFloat:
			basic = u.Basic
		}
		eq := e.eqExpr(t, "(*_a)", "(*_b)")
		key := e.keyExpr(t, "(*_a)")
		if !t.Comparable() {
			eq = fmt.Sprintf("eq_uncomparable(%q)", name)
			key = fmt.Sprintf("key_unhashable(%q)", name)
		}
		fmt.Fprintf(b, "fn %s(_a: &V, _b: &V) -> bool {\n    %s\n}\n\nfn %s(_a: &V) -> Key {\n    %s\n}\n\n", tdeq, eq, tdkey, key)
		var ms []string
		for k, m := range t.MethodSet {
			wn := fmt.Sprintf("%s_%d", e.names.Type(t, "mw_"), k)
			var as []string
			for j := range m.Func.Params {
				a := fmt.Sprintf("_a[%d].clone()", j)
				if j == 0 && t.IsAggregate() {
					a = e.cloneExpr(t, fmt.Sprintf("_a[%d]", j), true)
				}
				as = append(as, a)
			}
			body := fmt.Sprintf("%s(%s)", e.symbol(m.Func.Sym, "f_"), strings.Join(as, ", "))
			if e.p.SuspMethods[m.ID] && !m.Func.MaySuspend {
				e.use("core.task.spawn")
				body = fmt.Sprintf("sync_frame(func(%d, %s, Vec::new()), vec![%s], %d)", m.Func.ID, e.symbol(m.Func.Sym, "w_"), strings.Join(as, ", "), len(m.Func.Results))
			}
			fmt.Fprintf(b, "fn %s(_e: &[V], _a: Vec<V>) -> V {\n    %s\n}\n\n", wn, body)
			ms = append(ms, fmt.Sprintf("(%q, %d, %s)", m.ID, m.Func.ID, wn))
		}
		fmt.Fprintf(b, "pub(super) static %s: TypeDesc = TypeDesc {\n    id: %d,\n    name: %q,\n    kind: %q,\n    eq: %s,\n    key: %s,\n    methods: &[%s],\n    basic: %q,\n    comparable: %v,\n};\n\n",
			e.names.Type(t, "td_"), t.ID, name, u.Kind.String(), tdeq, tdkey, strings.Join(ms, ", "), basic, t.Comparable())
	}
	e.flushHelpers()
}

// wrapper renders the Code adapter for f: environment then arguments.
func (e *emitter) wrapper(f *ir.Func) string {
	var as []string
	for j := range f.Env {
		as = append(as, fmt.Sprintf("_e[%d].clone()", j))
	}
	for j := range f.Params {
		as = append(as, fmt.Sprintf("_a[%d].clone()", j))
	}
	return fmt.Sprintf("%sfn %s(_e: &[V], _a: Vec<V>) -> V {\n    %s(%s)\n}\n\n", e.visibility(), e.fnName(f.Sym, "w_"), e.fnName(f.Sym, "f_"), strings.Join(as, ", "))
}

func (e *emitter) externSym(contract string) string {
	e.use(contract)
	if s, ok := e.symbols[contract]; ok {
		return s
	}
	return strings.ReplaceAll(contract, ".", "_")
}

// externFn renders a function value calling a capability.
func (e *emitter) externFn(x *ir.Extern) string {
	e.externs[x.Contract] = x
	return fmt.Sprintf("func(-1, %s, Vec::new())", e.reference("", "_x_"+strings.ReplaceAll(x.Contract, ".", "_")))
}

func (e *emitter) externWrappers(b *bytes.Buffer) {
	var ids []string
	for id := range e.externs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		x := e.externs[id]

		if x.MaySuspend {
			name := strings.ReplaceAll(id, ".", "_")
			n := len(x.Sig.Params)
			var args, stores []string
			for j := range x.Sig.Params {
				args = append(args, fmt.Sprintf("_f.l.g(%d)", j))
				stores = append(stores, fmt.Sprintf("_f.l.s(%d,_a[%d].clone());", j, j))
			}
			fmt.Fprintf(b, "fn _xstep_%s(_t:&Rc<Task>, _f:&Rc<Frame>) {if _f.pc.get()==0 {_f.pc.set(1);%s(_t%s);return} let _rv=_t.rv.borrow().clone();_f.l.s(%d,tuple(_rv));ret(_t,_f); }\n", name, e.externSym(id), func() string {
				if len(args) > 0 {
					return "," + strings.Join(args, ",")
				}
				return ""
			}(), n)
			fmt.Fprintf(b, "fn _xr_%s(_f:&Frame)->Vec<V>{match _f.l.g(%d){V::Tuple(_v)=>_v.to_vec(),_=>vec![]}}\nfn _x_%s(_e:&[V],_a:Vec<V>)->V{let _f=Frame::new(%d,_xstep_%s,Some(_xr_%s));%s V::Frame(_f)}\n", name, n, name, n+1, name, name, strings.Join(stores, ""))
			continue
		}
		var as []string
		for j := range x.Sig.Params {
			as = append(as, fmt.Sprintf("_a[%d].clone()", j))
		}
		call := e.externSym(id) + "(" + strings.Join(as, ", ") + ")"
		if len(x.Sig.Results) == 0 {
			call = "{\n        " + call + ";\n        V::Nil\n    }"
		}
		fmt.Fprintf(b, "fn _x_%s(_e: &[V], _a: Vec<V>) -> V {\n    %s\n}\n\n", strings.ReplaceAll(id, ".", "_"), call)
	}
}

// ---- functions ----

type fnEmitter struct {
	e      *emitter
	f      *ir.Func
	b      strings.Builder
	order  map[*ir.Block]int
	slots  map[*ir.Local]int
	defers bool
	frame  bool
	native bool
	indent string
	folds  *structure.Folds
}

func (fe *fnEmitter) w(format string, args ...any) {
	fe.b.WriteString(fe.indent)
	fmt.Fprintf(&fe.b, format, args...)
	fe.b.WriteString("\n")
}

func cell(l *ir.Local) bool { return l.Boxed && !l.Type.IsAggregate() }

// raw reads a local's storage: the cell itself for boxed scalars.
func (fe *fnEmitter) raw(l *ir.Local) string {
	return fmt.Sprintf("_l.g(%s)", fe.slot(l))
}

func (fe *fnEmitter) val(v ir.Value) string {
	switch v := v.(type) {
	case *ir.Local:
		if s, ok := fe.folds.Use(v); ok {
			return s
		}
		if cell(v) {
			return "pget(&" + fe.raw(v) + ")"
		}
		return fe.raw(v)
	case *ir.Const:
		return fe.e.constant(v)
	case *ir.FuncRef:
		f := fmt.Sprintf("func(%d, %s, Vec::new())", v.Func.ID, fe.e.symbol(v.Func.Sym, "w_"))
		if fe.e.susp(v.Type) && !v.Func.MaySuspend {
			return fe.e.adapt(f, len(v.Func.Results))
		}
		return f
	}
	panic(fmt.Sprintf("rust: operand %T", v))
}

// set renders an assignment to a local's value.
func (fe *fnEmitter) set(l *ir.Local, x string) string {
	if cell(l) {
		return fmt.Sprintf("pset(&%s, %s)", fe.raw(l), x)
	}
	return fmt.Sprintf("_l.s(%s, %s)", fe.slot(l), x)
}

func (e *emitter) constant(c *ir.Const) string {
	if c.Nil {
		if c.Type.U().Kind == ir.KSlice {
			return e.zero(c.Type)
		}
		return "V::Nil"
	}
	if c.Type.U().Kind == ir.KFloat {
		return "V::Float(" + ir.FloatLiteral(c) + "f64)"
	}
	switch c.Val.Kind() {
	case constant.Bool:
		return fmt.Sprintf("V::Bool(%v)", constant.BoolVal(c.Val))
	case constant.String:
		return "s(" + rustBytes(constant.StringVal(c.Val)) + ")"
	case constant.Int:
		if v, ok := constant.Int64Val(c.Val); ok {
			if v == -9223372036854775808 {
				return "V::Int(i64::MIN)"
			}
			return fmt.Sprintf("V::Int(%d)", v)
		}
		u, _ := constant.Uint64Val(c.Val)
		return fmt.Sprintf("V::Int(0x%xu64 as i64)", u)
	}
	panic("rust: constant " + c.Val.String())
}

func rustBytes(s string) string {
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

func scalarGlobal(g *ir.Global) bool { return g.AddrTaken && !g.Type.IsAggregate() }

func (fe *fnEmitter) global(g *ir.Global) string {
	return fmt.Sprintf("gg(%s)", fe.e.symbol(g.Sym, ""))
}

func (fe *fnEmitter) rootExpr(p *ir.Place) string {
	switch r := p.Root.(type) {
	case ir.LocalRoot:
		return fe.val(r.Local)
	case ir.GlobalRoot:
		if scalarGlobal(r.Global) {
			return "pget(&" + fe.global(r.Global) + ")"
		}
		return fe.global(r.Global)
	case ir.DerefRoot:
		if r.Ptr.IRType().U().Elem.IsAggregate() {
			return "nilchk(" + fe.val(r.Ptr) + ")"
		}
		return "pget(&" + fe.val(r.Ptr) + ")"
	case ir.SliceRoot:
		fe.e.use("core.slice.index")
		fn := "sget"
		if unsigned64(r.Index.IRType()) {
			fn = "sgetu"
		}
		return fmt.Sprintf("%s(%s, %s)", fn, fe.val(r.Slice), fe.val(r.Index))
	case ir.ValueRoot:
		return fe.val(r.Value)
	}
	panic("rust: place root")
}

func (fe *fnEmitter) proj(base string, cur *ir.Type, pr ir.Proj) string {
	if pr.Index != nil {
		fn := "aget"
		if unsigned64(pr.Index.IRType()) {
			fn = "agetu"
		}
		return fmt.Sprintf("%s(&%s, &%s, %d)", fn, base, fe.val(pr.Index), cur.U().Len)
	}
	return fmt.Sprintf("fld(&%s, %d)", base, pr.Field)
}

func (fe *fnEmitter) walk(p *ir.Place, drop int) (string, *ir.Type) {
	s := fe.rootExpr(p)
	cur := rootType(p)
	for _, pr := range p.Path[:len(p.Path)-drop] {
		s = fe.proj(s, cur, pr)
		cur = pr.Type
	}
	return s, cur
}

func (fe *fnEmitter) placeExpr(p *ir.Place) string {
	s, _ := fe.walk(p, 0)
	return s
}

func (fe *fnEmitter) store(p *ir.Place, v string) string {
	t := p.Type
	e := fe.e
	if len(p.Path) == 0 {
		switch r := p.Root.(type) {
		case ir.SliceRoot:
			if !t.IsAggregate() {
				e.use("core.slice.store")
				fn := "sset"
				if unsigned64(r.Index.IRType()) {
					fn = "ssetu"
				}
				return fmt.Sprintf("%s(%s, %s, %s)", fn, fe.val(r.Slice), fe.val(r.Index), v)
			}
		case ir.DerefRoot:
			if !t.IsAggregate() {
				return fmt.Sprintf("pset(&%s, %s)", fe.val(r.Ptr), v)
			}
		case ir.LocalRoot:
			if !t.IsAggregate() || r.Local.Kind == ir.LTemp {
				return fe.set(r.Local, v)
			}
		case ir.GlobalRoot:
			if scalarGlobal(r.Global) {
				return fmt.Sprintf("pset(&%s, %s)", fe.global(r.Global), v)
			}
			if !t.IsAggregate() {
				return fmt.Sprintf("gs(%s, %s)", e.symbol(r.Global.Sym, ""), v)
			}
		}
	}
	if t.IsAggregate() {
		return e.setStmt(t, fe.placeExpr(p), v)
	}
	base, bt := fe.walk(p, 1)
	last := p.Path[len(p.Path)-1]
	if last.Index != nil {
		fn := "aset"
		if unsigned64(last.Index.IRType()) {
			fn = "asetu"
		}
		return fmt.Sprintf("%s(&%s, &%s, %d, %s)", fn, base, fe.val(last.Index), bt.U().Len, v)
	}
	return fmt.Sprintf("fset(&%s, %d, %s)", base, last.Field, v)
}

func (fe *fnEmitter) addrOf(p *ir.Place) string {
	t := p.Type
	if len(p.Path) == 0 {
		switch r := p.Root.(type) {
		case ir.DerefRoot:
			return fe.val(r.Ptr)
		case ir.LocalRoot:
			if t.IsAggregate() || r.Local.Boxed {
				return fe.raw(r.Local)
			}
		case ir.GlobalRoot:
			return fe.global(r.Global)
		}
	}
	if t.IsAggregate() {
		return fe.placeExpr(p)
	}
	base, _ := fe.walk(p, 1)
	last := p.Path[len(p.Path)-1]
	return fmt.Sprintf("fptr(&%s, %d)", base, last.Field)
}

func (fe *fnEmitter) slot(l *ir.Local) string {
	if n, ok := fe.folds.Name(l); ok {
		return n
	}
	return fe.e.names.Local(l, "_")
}

func (fe *fnEmitter) slotDecls() {
	for _, l := range fe.f.Locals {
		if !fe.folds.Temp(l) {
			fe.w("const %s: usize = %d;", fe.slot(l), fe.slots[l])
		}
	}
}

func (fe *fnEmitter) allLocals() {
	fe.slots = map[*ir.Local]int{}
	for i, l := range fe.f.Locals {
		fe.slots[l] = i
	}
}

func (e *emitter) function(f *ir.Func) string {
	if f.MaySuspend {
		if e.p.PauseModel == "native" {
			return e.nativeFunction(f)
		}
		return e.frameFunction(f)
	}
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}}
	fe.allLocals()
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
	fe.analyze()
	var params []string
	for _, l := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		params = append(params, "_arg_"+fe.slot(l)+": V")
	}
	fe.w("%s%sfn %s(%s) -> V {", position(f.Pos), e.visibility(), e.fnName(f.Sym, "f_"), strings.Join(params, ", "))
	fe.indent = "    "
	fe.w("let _source_depth = source_guard();")
	fe.slotDecls()
	fe.w("let _l = Fr::new(%d);", len(f.Locals))
	for _, p := range append(append([]*ir.Local(nil), f.Env...), f.Params...) {
		fe.w("_l.s(%s, _arg_%s);", fe.slot(p), fe.slot(p))
	}
	fe.w("safepoint();")
	if fe.defers {
		fe.w("let _p = match catch(|| -> () {")
		fe.indent = "        "
		fe.body()
		fe.indent = "    "
		fe.w("}) {")
		fe.w("    Ok(()) => V::Nil,")
		fe.w("    Err(_p) => _p,")
		fe.w("};")
		fe.w("run_defers(&_l, _p);")
		fe.w("%s", fe.results())
	} else {
		fe.body()
	}
	fe.indent = ""
	fe.b.WriteString("}\n\n")
	return structure.Resolve(fe.b.String())
}

func (fe *fnEmitter) body() {
	body := structure.Build(fe.f, structure.Options{Inline: fe.native, Empty: func(b *ir.Block) bool { return fe.folds.Empty(b, fe.native) }})
	syn := rustSyntax
	syn.DispatchOpen = []string{"%sloop {", "\tsafepoint();", "\tmatch " + fe.pc() + " {"}
	if !fe.frame {
		syn.DispatchOpen = append([]string{"let mut _pc: u32 = 0;"}, syn.DispatchOpen...)
		syn.SetPC = "_pc = %d;"
	} else {
		syn.SetPC = "_fr.pc.set(%d);"
	}
	depth := len(fe.indent) / 4
	indent := fe.indent
	fe.indent = ""
	fe.b.WriteString(structure.Print(body, syn, rustEmitter{fe}, depth))
	fe.indent = indent
}

var rustSyntax = structure.Syntax{
	Indent: "    ", If: "if %s {", ElseIf: "} else if %s {", Else: "} else {", End: "}",
	Loop: "loop {", While: "while %s {", Block: "%s: {", LoopLabel: "%s: ", LabelName: "'l%d",
	Break: "break;", BreakTo: "break %s;", Continue: "continue;", ContinueTo: "continue %s;", LabelInBlocks: true,
	Not: "!%s", And: "%s && %s", Or: "%s || %s", LoopStart: "safepoint();",
	CaseOpen: "%d => {", CaseClose: "}", Default: []string{`_ => fault("bad block"),`}, DispatchClose: []string{"\t}", "}"},
}

func (fe *fnEmitter) analyze() {
	structure.Analyze(fe.f, structure.FoldOptions{Inline: fe.native, PauseAlone: fe.native, CallAlone: true, Order: structure.LeftToRight}, rustProbe{fe}, func(fs *structure.Folds) { fe.folds = fs })
}

func (fe *fnEmitter) capture(fn func()) string {
	saved, indent := fe.b, fe.indent
	fe.b, fe.indent = strings.Builder{}, ""
	fn()
	out := fe.b.String()
	fe.b, fe.indent = saved, indent
	return out
}

// rustEmitter prints the target-specific statements of a structured body.
type rustEmitter struct{ fe *fnEmitter }

func (r rustEmitter) Code(b *ir.Block) string {
	fe := r.fe
	return fe.capture(func() {
		if b.ResumeOf != nil && !fe.native {
			fe.resume(b.ResumeOf)
		}
		for _, in := range b.Instrs {
			if !fe.folds.Folded(in) {
				fe.instr(in)
			}
		}
	})
}

func (r rustEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	return r.fe.capture(func() { r.fe.term(t) })
}

func (r rustEmitter) Pause(p *ir.Pause) string {
	if r.fe.folds.FoldedPause(p) {
		return ""
	}
	return r.fe.capture(func() { r.fe.nativePause(p) })
}
func (r rustEmitter) Cond(v ir.Value) string    { return r.fe.val(v) + ".b()" }
func (r rustEmitter) Mark(pos token.Pos) string { return r.fe.mark(pos) }

// rustProbe renders statements for fold analysis.
type rustProbe struct{ fe *fnEmitter }

func (r rustProbe) Instr(in ir.Instr) string { return r.fe.capture(func() { r.fe.instr(in) }) }
func (r rustProbe) Pause(p *ir.Pause) string { return r.fe.capture(func() { r.fe.nativePause(p) }) }
func (r rustProbe) Cond(v ir.Value) string   { return r.fe.val(v) }
func (r rustProbe) Leaf(b *ir.Block, t ir.Terminator) string {
	return r.fe.capture(func() { r.fe.term(t) })
}
func (r rustProbe) Assign(l *ir.Local) (string, string) {
	s := r.fe.set(l, "\x01")
	i := strings.Index(s, "\x01")
	return s[:i], s[i+1:] + ";"
}

func (fe *fnEmitter) pc() string {
	if fe.frame {
		return "_fr.pc.get()"
	}
	return "_pc"
}

func (fe *fnEmitter) results() string {
	var vs []string
	for _, r := range fe.f.Results {
		v := fe.val(r)
		if r.Boxed && r.Type.IsAggregate() {
			v = fe.e.cloneExpr(r.Type, v, false)
		}
		vs = append(vs, v)
	}
	switch len(vs) {
	case 0:
		return "V::Nil"
	case 1:
		return vs[0]
	}
	return "tuple(vec![" + strings.Join(vs, ", ") + "])"
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
			fe.w("%sret(_t, _fr);", m)
			fe.w("return;")
		case fe.defers:
			fe.w("%sreturn;", m)
		default:
			fe.w("%sreturn %s;", m, fe.results())
		}
	case *ir.Panic:
		fe.w("%sthrow(%s);", m, fe.val(t.X))
	case *ir.PanicRuntime:
		fe.w("%splain_panic(%s);", m, rustBytes(t.Msg))
	case *ir.Unreachable:
		fe.w("%sfault(\"unreachable\");", m)
	default:
		panic(fmt.Sprintf("rust: terminator %T", t))
	}
}

// frameFunction emits a suspending function as a step function over a
// resumable frame and a starter binding the arguments.
func (e *emitter) frameFunction(f *ir.Func) string {
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}, frame: true}
	fe.allLocals()
	for i, b := range f.Blocks {
		fe.order[b] = i
	}
	fe.analyze()
	fe.w("%sfn %s(_t: &Rc<Task>, _fr: &Rc<Frame>) {", position(f.Pos), e.fnName(f.Sym, "st_"))
	fe.indent = "    "
	fe.slotDecls()
	fe.w("let _l = &_fr.l;")
	fe.body()
	fe.indent = ""
	fe.w("}\n")
	var rs []string
	for _, r := range f.Results {
		v := fe.val(r)
		if r.Boxed && r.Type.IsAggregate() {
			v = e.cloneExpr(r.Type, v, false)
		}
		rs = append(rs, v)
	}
	fe.w("fn %s(_fr: &Frame) -> Vec<V> {", e.fnName(f.Sym, "rs_"))
	fe.slotDecls()
	fe.w("    let _l = &_fr.l;\n    vec![%s]\n}\n", strings.Join(rs, ", "))
	var params []string
	ps := append(append([]*ir.Local(nil), f.Env...), f.Params...)
	for _, l := range ps {
		params = append(params, "_arg_"+fe.slot(l)+": V")
	}
	fe.w("%s%sfn %s(%s) -> V {", position(f.Pos), e.visibility(), e.fnName(f.Sym, "f_"), strings.Join(params, ", "))
	fe.slotDecls()
	fe.w("    let _fr = Frame::new(%d, %s, Some(%s));", len(f.Locals), e.fnName(f.Sym, "st_"), e.fnName(f.Sym, "rs_"))
	for _, p := range ps {
		fe.w("    _fr.l.s(%s, _arg_%s);", fe.slot(p), fe.slot(p))
	}
	fe.w("    V::Frame(_fr)\n}\n")
	return structure.Resolve(fe.b.String())
}

func (fe *fnEmitter) resume(op ir.Instr) {
	switch i := op.(type) {
	case *ir.Call:
		for k, d := range i.Dsts {
			if d != nil {
				fe.w("%s;", fe.set(d, fmt.Sprintf("_t.rv(%d)", k)))
			}
		}
	case *ir.Recv:
		if i.Dst != nil {
			fe.w("%s;", fe.set(i.Dst, "_t.rv(0)"))
		}
		if i.Ok != nil {
			fe.w("%s;", fe.set(i.Ok, "_t.rv(1)"))
		}
	case *ir.Select:
		fe.w("%s;", fe.set(i.Index, "_t.rv(0)"))
		for k, c := range i.Cases {
			if c.Send || c.Dst == nil && c.Ok == nil {
				continue
			}
			fe.w("if _t.rv(0).i() == %d {", k)
			if c.Dst != nil {
				fe.w("    %s;", fe.set(c.Dst, "_t.rv(1)"))
			}
			if c.Ok != nil {
				fe.w("    %s;", fe.set(c.Ok, "_t.rv(2)"))
			}
			fe.w("}")
		}
	}
}

func (fe *fnEmitter) pause(p *ir.Pause) {
	e := fe.e
	fe.w("%s_fr.pc.set(%d);", fe.mark(p.Position()), fe.order[p.Next])
	switch i := p.Op.(type) {
	case *ir.Call:
		if i.Kind == ir.CallExtern {
			args := append([]string{"_t"}, fe.externArgs(i)...)
			fe.w("%s(%s);", e.externSym(i.Extern.Contract), strings.Join(args, ", "))
		} else {
			fe.w("call(_t, %s);", fe.callExpr(i))
		}
	case *ir.Send:
		e.use("core.chan.send")
		fe.w("chan_send(_t, %s, %s);", fe.val(i.Ch), fe.val(i.V))
	case *ir.Recv:
		e.use("core.chan.recv")
		fe.w("chan_recv(_t, %s);", fe.val(i.Ch))
	case *ir.Select:
		e.use("core.select")
		var cs []string
		for _, c := range i.Cases {
			if c.Send {
				cs = append(cs, fmt.Sprintf("scase(%s, true, %s)", fe.val(c.Ch), fe.val(c.V)))
			} else {
				cs = append(cs, fmt.Sprintf("scase(%s, false, V::Nil)", fe.val(c.Ch)))
			}
		}
		fe.w("select(_t, %v, vec![%s]);", i.Default, strings.Join(cs, ", "))
	default:
		panic(fmt.Sprintf("rust: pause on %T", p.Op))
	}
	fe.w("return;")
}

var binName = map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Rem: "rem",
	ir.And: "and", ir.Or: "or", ir.Xor: "xor", ir.AndNot: "andnot", ir.Shl: "shl", ir.Shr: "shr"}

var binContract = map[ir.BinOpKind]string{ir.Add: "core.integer.add", ir.Sub: "core.integer.sub", ir.Mul: "core.integer.mul",
	ir.Div: "core.integer.div", ir.Rem: "core.integer.rem", ir.And: "core.integer.and", ir.Or: "core.integer.or",
	ir.Xor: "core.integer.xor", ir.AndNot: "core.integer.andnot", ir.Shl: "core.integer.shl", ir.Shr: "core.integer.shr"}

func (fe *fnEmitter) args(c *ir.Call) []string {
	var as []string
	for _, a := range c.Args {
		as = append(as, fe.val(a))
	}
	return as
}

// externArgs renders capability arguments, adapting function slices to the
// resumable form the runtime expects.
func (fe *fnEmitter) externArgs(c *ir.Call) []string {
	var args []string
	for _, a := range c.Args {
		v := fe.val(a)
		if u := a.IRType().U(); u.Kind == ir.KSlice && u.Elem.U().Kind == ir.KFunc && !fe.e.susp(u.Elem) {
			fe.e.use("core.task.spawn")
			v = fmt.Sprintf("adapt_slice(%s, %d)", v, len(u.Elem.U().Results))
		}
		args = append(args, v)
	}
	return args
}

// callExpr renders a call; the result is Nil, one value, or a Tuple.
func (fe *fnEmitter) callExpr(c *ir.Call) string {
	as := fe.args(c)
	switch c.Kind {
	case ir.CallStatic:
		return fmt.Sprintf("%s(%s)", fe.e.symbol(c.Func.Sym, "f_"), strings.Join(as, ", "))
	case ir.CallValue:
		return fmt.Sprintf("callv(&%s, vec![%s])", fe.val(c.Fn), strings.Join(as, ", "))
	case ir.CallInterface:
		return fmt.Sprintf("icall(&%s, %q, vec![%s])", fe.val(c.Recv), c.Method, strings.Join(as, ", "))
	case ir.CallExtern:
		return fmt.Sprintf("%s(%s)", fe.e.externSym(c.Extern.Contract), strings.Join(fe.externArgs(c), ", "))
	}
	panic("rust: call kind")
}

// fnValue renders the callee of a deferred or spawned call as a function
// value, and its recover identity.
func (fe *fnEmitter) fnValue(c *ir.Call) (string, string) {
	switch c.Kind {
	case ir.CallStatic:
		return fmt.Sprintf("func(%d, %s, Vec::new())", c.Func.ID, fe.e.symbol(c.Func.Sym, "w_")), strconv.Itoa(c.Func.ID)
	case ir.CallValue:
		f := fe.val(c.Fn)
		return f, "fid_of(&" + f + ")"
	case ir.CallExtern:
		return fe.e.externFn(c.Extern), "-1"
	}
	panic("rust: deferred interface call")
}

func (fe *fnEmitter) instr(in ir.Instr) {
	e := fe.e
	m := fe.mark(in.Position())
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
			w("_l.s(%s, cellv(%s));", fe.slot(i.L), init)
		} else {
			w("_l.s(%s, %s);", fe.slot(i.L), init)
		}
	case *ir.Assign:
		w("%s;", fe.set(i.Dst, fe.val(i.Src)))
	case *ir.Zero:
		w("%s;", fe.set(i.Dst, e.zero(i.Dst.Type)))
	case *ir.Load:
		x := fe.placeExpr(i.Place)
		if i.Dst.Type.IsAggregate() {
			x = e.cloneExpr(i.Dst.Type, x, false)
		}
		w("%s;", fe.set(i.Dst, x))
	case *ir.Store:
		w("%s;", fe.store(i.Place, fe.val(i.V)))
	case *ir.AddrOf:
		w("%s;", fe.set(i.Dst, fe.addrOf(i.Place)))
	case *ir.New:
		el := i.Dst.Type.U().Elem
		if el.IsAggregate() {
			w("%s;", fe.set(i.Dst, e.zero(el)))
		} else {
			w("%s;", fe.set(i.Dst, "cellv("+e.zero(el)+")"))
		}
	case *ir.UnOp:
		x := fe.val(i.X)
		switch i.Op {
		case ir.Not:
			w("%s;", fe.set(i.Dst, "V::Bool(!"+x+".b())"))
		case ir.Neg:
			if i.Dst.Type.U().Kind == ir.KFloat {
				e.use("core.float.neg")
				w("%s;", fe.set(i.Dst, fmt.Sprintf("float_neg_f%d(%s)", i.Dst.Type.U().FloatBits, x)))
				break
			}
			e.use("core.integer.neg")
			w("%s;", fe.set(i.Dst, fmt.Sprintf("neg_%s(%s)", intKind(i.Dst.Type), x)))
		case ir.BitNot:
			e.use("core.integer.not")
			w("%s;", fe.set(i.Dst, fmt.Sprintf("not_%s(%s)", intKind(i.Dst.Type), x)))
		}
	case *ir.BinOp:
		w("%s;", fe.set(i.Dst, fe.binop(i)))
	case *ir.Convert:
		w("%s;", fe.set(i.Dst, fe.convert(i)))
	case *ir.MakeInterface:
		w("%s;", fe.set(i.Dst, fmt.Sprintf("boxv(%s, %s)", e.td(i.X.IRType()), fe.val(i.X))))
	case *ir.TypeAssert:
		fe.typeAssert(i, w)
	case *ir.Call:
		call := fe.callExpr(i)
		switch {
		case len(i.Dsts) == 0:
			w("%s;", call)
		case len(i.Dsts) == 1:
			v := call
			if d := i.Dsts[0]; i.Kind == ir.CallExtern && d.Type.U().Kind == ir.KFunc && e.susp(d.Type) {
				v = e.adapt(v, len(d.Type.U().Results))
			}
			w("%s;", fe.set(i.Dsts[0], v))
		default:
			w("let _r = %s;", call)
			for k, d := range i.Dsts {
				if d == nil {
					continue
				}
				v := fmt.Sprintf("_r.at(%d)", k)
				if i.Kind == ir.CallExtern && d.Type.U().Kind == ir.KFunc && e.susp(d.Type) {
					v = e.adapt(v, len(d.Type.U().Results))
				}
				w("%s;", fe.set(d, v))
			}
		}
	case *ir.MakeClosure:
		var env []string
		for _, l := range i.Env {
			env = append(env, fe.raw(l))
		}
		v := fmt.Sprintf("func(%d, %s, vec![%s])", i.Func.ID, fe.e.symbol(i.Func.Sym, "w_"), strings.Join(env, ", "))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		w("%s;", fe.set(i.Dst, v))
	case *ir.MakeBound:
		v := fmt.Sprintf("bound(%d, func(%d, %s, Vec::new()), %s)", i.Func.ID, i.Func.ID, fe.e.symbol(i.Func.Sym, "w_"), fe.val(i.Recv))
		if e.susp(i.Dst.Type) && !i.Func.MaySuspend {
			v = e.adapt(v, len(i.Func.Results))
		}
		w("%s;", fe.set(i.Dst, v))
	case *ir.MakeIfaceBound:
		v := fmt.Sprintf("ibound(&%s, %q)", fe.val(i.Recv), i.Method)
		if e.susp(i.Dst.Type) && !e.p.SuspMethods[i.Method] {
			v = e.adapt(v, len(i.Dst.Type.U().Results))
		}
		w("%s;", fe.set(i.Dst, v))
	case *ir.Len:
		w("%s;", fe.set(i.Dst, fe.lenExpr(i.X, false)))
	case *ir.Cap:
		w("%s;", fe.set(i.Dst, fe.lenExpr(i.X, true)))
	case *ir.MakeSlice:
		e.use("core.slice.make")
		if byteElem(i.Dst.Type.U().Elem) {
			w("%s;", fe.set(i.Dst, fmt.Sprintf("make_byte_slice(%s, %s)", fe.val(i.Len), fe.val(i.Cap))))
		} else {
			w("%s;", fe.set(i.Dst, fmt.Sprintf("make_slice(%s, %s, %s, false)", fe.val(i.Len), fe.val(i.Cap), e.zeroFn(i.Dst.Type.U().Elem))))
		}
	case *ir.MakeMap:
		e.use("core.map.make")
		w("%s;", fe.set(i.Dst, fmt.Sprintf("make_map(%s)", e.keyFn(i.Dst.Type.U().Key))))
	case *ir.Append:
		e.use("core.slice.append")
		cl := e.cloneFn(i.Dst.Type.U().Elem)
		var v string
		switch {
		case i.Spread != nil && i.Spread.IRType().U().Kind == ir.KString:
			v = fmt.Sprintf("append_string(%s, %s)", fe.val(i.S), fe.val(i.Spread))
		case i.Spread != nil:
			v = fmt.Sprintf("append_slice(%s, %s, %s)", fe.val(i.S), fe.val(i.Spread), cl)
		default:
			var es []string
			for _, x := range i.Elems {
				v := fe.val(x)
				if byteElem(i.Dst.Type.U().Elem) {
					v = "(" + v + ").i() as u8"
				}
				es = append(es, v)
			}
			if byteElem(i.Dst.Type.U().Elem) {
				v = fmt.Sprintf("append_bytes(%s, &[%s])", fe.val(i.S), strings.Join(es, ", "))
			} else {
				v = fmt.Sprintf("append(%s, vec![%s], %s)", fe.val(i.S), strings.Join(es, ", "), cl)
			}
		}
		v = fmt.Sprintf("zero_append_growth(%s, &%s, %s)", v, fe.val(i.S), e.zeroFn(i.Dst.Type.U().Elem))
		w("%s;", fe.set(i.Dst, v))
	case *ir.Copy:
		e.use("core.slice.copy")
		if i.Src.IRType().U().Kind == ir.KString {
			w("%s;", fe.set(i.N, fmt.Sprintf("copy_string(%s, %s)", fe.val(i.Dst), fe.val(i.Src))))
		} else {
			w("%s;", fe.set(i.N, fmt.Sprintf("copy(%s, %s, %s)", fe.val(i.Dst), fe.val(i.Src), e.cloneFn(i.Dst.IRType().U().Elem))))
		}
	case *ir.IndexString:
		e.use("core.string.index")
		fn := "sindex"
		if unsigned64(i.I.IRType()) {
			fn = "sindexu"
		}
		w("%s;", fe.set(i.Dst, fmt.Sprintf("%s(%s, %s)", fn, fe.val(i.S), fe.val(i.I))))
	case *ir.SliceOp:
		w("%s;", fe.set(i.Dst, fe.sliceOp(i)))
	case *ir.MapLookup:
		e.use("core.map.lookup")
		mt := i.M.IRType().U()
		w("let _r = map_get(%s, %s, %s, %s);", fe.val(i.M), fe.val(i.K), e.keyFn(mt.Key), e.zeroFn(mt.Elem))
		w("%s;", fe.set(i.Dst, e.cloneExpr(mt.Elem, "_r.at(0)", false)))
		if i.Ok != nil {
			w("%s;", fe.set(i.Ok, "_r.at(1)"))
		}
	case *ir.MapStore:
		e.use("core.map.store")
		w("map_set(%s, %s, %s);", fe.val(i.M), fe.val(i.K), fe.val(i.V))
	case *ir.MapDelete:
		e.use("core.map.delete")
		w("map_delete(%s, %s, %s);", fe.val(i.M), fe.val(i.K), e.keyFn(i.M.IRType().U().Key))
	case *ir.Clear:
		if i.X.IRType().U().Kind == ir.KMap {
			e.use("core.map.clear")
			w("map_clear(%s);", fe.val(i.X))
		} else {
			e.use("core.slice.clear")
			w("clear_slice(%s, %s);", fe.val(i.X), e.zeroFn(i.X.IRType().U().Elem))
		}
	case *ir.DecodeRune:
		e.use("core.string.decode_rune")
		w("let _r = decode_rune(%s, %s);", fe.val(i.S), fe.val(i.I))
		w("%s;", fe.set(i.Rune, "_r.at(0)"))
		w("%s;", fe.set(i.Width, "_r.at(1)"))
	case *ir.MapIterInit:
		e.use("core.map.iterate")
		w("%s;", fe.set(i.Iter, fmt.Sprintf("map_iter(%s)", fe.val(i.M))))
	case *ir.MapIterNext:
		mt := i.Iter.Type.Elem.U()
		it := fe.val(i.Iter)
		w("%s;", fe.set(i.Ok, fmt.Sprintf("map_next(%s)", it)))
		if i.Key != nil {
			w("if %s.b() {", fe.val(i.Ok))
			w("    %s;", fe.set(i.Key, e.cloneExpr(mt.Key, "iter_key("+it+")", false)))
			w("}")
		}
		if i.Val != nil {
			w("if %s.b() {", fe.val(i.Ok))
			w("    %s;", fe.set(i.Val, e.cloneExpr(mt.Elem, "iter_val("+it+")", false)))
			w("}")
		}
	case *ir.Print:
		e.use("core.print")
		var as []string
		for _, a := range i.Args {
			v := fe.val(a)
			if a.IRType().U().Kind == ir.KFloat {
				v = fmt.Sprintf("s(float_print(%s.f(), %d).as_bytes())", v, a.IRType().U().FloatBits)
			} else if unsigned64(a.IRType()) {
				v = "u64s(" + v + ")"
			}
			as = append(as, v)
		}
		w("go_print(vec![%s], %v);", strings.Join(as, ", "), i.Newline)
	case *ir.Defer:
		if i.Call.Kind == ir.CallInterface {
			panic("rust: defer of interface call")
		}
		fn, fid := fe.fnValue(i.Call)
		args := fe.args(i.Call)
		if i.Call.Kind == ir.CallExtern {
			args = fe.externArgs(i.Call)
		}
		w("_l.defer(Deferred { f: %s, args: vec![%s], fid: %s, start: %v });", fn, strings.Join(args, ", "), fid, i.Call.Suspends && (fe.frame || fe.native))
	case *ir.Recover:
		w("%s;", fe.set(i.Dst, fmt.Sprintf("recover(%d)", fe.f.ID)))
	case *ir.Rebind:
		switch {
		case cell(i.L):
			w("_l.s(%s, cellv(pget(&%s)));", fe.slot(i.L), fe.raw(i.L))
		case i.L.Type.IsAggregate():
			w("_l.s(%s, %s);", fe.slot(i.L), e.cloneExpr(i.L.Type, fe.raw(i.L), false))
		}
	case *ir.BoxParam:
		if cell(i.L) {
			w("_l.s(%s, cellv(%s));", fe.slot(i.L), fe.raw(i.L))
		}
	case *ir.MakeChan:
		e.use("core.chan.make")
		w("%s;", fe.set(i.Dst, fmt.Sprintf("make_chan(%s, %s)", fe.val(i.Size), e.zeroFn(i.Dst.Type.U().Elem))))
	case *ir.Close:
		e.use("core.chan.close")
		w("chan_close(%s);", fe.val(i.Ch))
	case *ir.Go:
		e.use("core.task.spawn")
		c := i.Call
		if c.Kind == ir.CallInterface {
			panic("rust: go statement with interface call")
		}
		if c.Suspends {
			if c.Kind == ir.CallExtern {
				panic("rust: go statement with suspending capability")
			}
			w("spawn(%s);", fe.callExpr(c))
			return
		}
		fn, _ := fe.fnValue(c)
		args := fe.args(c)
		if c.Kind == ir.CallExtern {
			args = fe.externArgs(c)
		}
		w("spawn_call(%s, vec![%s]);", fn, strings.Join(args, ", "))
	default:
		panic(fmt.Sprintf("rust: unsupported instruction %T", in))
	}
}

func (fe *fnEmitter) lenExpr(x ir.Value, isCap bool) string {
	t := x.IRType().U()
	v := fe.val(x)
	switch t.Kind {
	case ir.KString:
		return "strlen(&" + v + ")"
	case ir.KSlice:
		if isCap {
			return "scap(&" + v + ")"
		}
		return "slen(&" + v + ")"
	case ir.KArray:
		return fmt.Sprintf("V::Int(%d)", t.Len)
	case ir.KPointer:
		return fmt.Sprintf("V::Int(%d)", t.Elem.U().Len)
	case ir.KMap:
		fe.e.use("core.map.len")
		return "map_len(" + v + ")"
	case ir.KChan:
		if isCap {
			fe.e.use("core.chan.cap")
			return "chan_cap(" + v + ")"
		}
		fe.e.use("core.chan.len")
		return "chan_len(" + v + ")"
	}
	panic("rust: len of " + t.String())
}

func (fe *fnEmitter) binop(i *ir.BinOp) string {
	e := fe.e
	x, y := fe.val(i.X), fe.val(i.Y)
	t := i.X.IRType()
	if t.U().Kind == ir.KFloat && !i.Op.IsComparison() {
		op := map[ir.BinOpKind]string{ir.Add: "add", ir.Sub: "sub", ir.Mul: "mul", ir.Div: "div", ir.Min: "min", ir.Max: "max"}[i.Op]
		e.use("core.float." + op)
		return fmt.Sprintf("float_%s_f%d(%s, %s)", op, t.U().FloatBits, x, y)
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
				eq = "nil_slice(&" + x + ")"
			} else {
				eq = "nil_slice(&" + y + ")"
			}
		case (xnil || ynil) && u.Kind != ir.KInterface:
			eq = fmt.Sprintf("veq(&%s, &%s)", x, y)
		default:
			eq = e.eqExpr(t, x, y)
		}
		if i.Op == ir.Ne {
			eq = "!" + eq
		}
		return "V::Bool(" + eq + ")"
	case ir.Lt, ir.Le, ir.Gt, ir.Ge:
		return "V::Bool(" + cmpExpr(u, unsigned64(t), x, i.Op.String(), y) + ")"
	case ir.Min, ir.Max:
		op := "<"
		if i.Op == ir.Max {
			op = ">"
		}
		return fmt.Sprintf("{ let (_x, _y) = (%s, %s); if %s { _y } else { _x } }", x, y, cmpExpr(u, unsigned64(t), "_y", op, "_x"))
	}
	if u.Kind == ir.KString {
		e.use("core.string.concat")
		return fmt.Sprintf("concat(%s, %s)", x, y)
	}
	e.use(binContract[i.Op])
	if i.Op == ir.Shl || i.Op == ir.Shr {
		cnt := "count(&" + y + ")"
		if unsigned64(i.Y.IRType()) {
			cnt = "countu(&" + y + ")"
		}
		return fmt.Sprintf("%s_%s(%s, %s)", binName[i.Op], intKind(i.Dst.Type), x, cnt)
	}
	return fmt.Sprintf("%s_%s(%s, %s)", binName[i.Op], intKind(i.Dst.Type), x, y)
}

func cmpExpr(u *ir.Type, unsigned bool, x, op, y string) string {
	switch {
	case u.Kind == ir.KFloat:
		return fmt.Sprintf("%s.f() %s %s.f()", x, op, y)

	case u.Kind == ir.KString:
		return fmt.Sprintf("%s.bytes()[..] %s %s.bytes()[..]", x, op, y)
	case unsigned:
		return fmt.Sprintf("(%s.i() as u64) %s (%s.i() as u64)", x, op, y)
	}
	return fmt.Sprintf("%s.i() %s %s.i()", x, op, y)
}

func (fe *fnEmitter) convert(i *ir.Convert) string {
	e := fe.e
	x := fe.val(i.X)
	switch i.Kind {
	case ir.ConvFloat:
		e.use("core.float.convert")
		u, from := i.Dst.Type.U(), i.X.IRType().U()
		if u.Kind == ir.KFloat {
			if from.Kind == ir.KFloat {
				return fmt.Sprintf("float_convert(%s, %d)", x, u.FloatBits)
			}
			return fmt.Sprintf("integer_float_convert(%s, %t, %d)", x, !from.Int.Signed(), u.FloatBits)
		}
		return fmt.Sprintf("float_integer_convert(%s, %d, %t)", x, u.Int.Bits(), u.Int.Signed())
	case ir.ConvNop, ir.ConvIfaceToIface:
		return x
	case ir.ConvInt:
		e.use("core.integer.convert")
		return fmt.Sprintf("to_%s(%s)", intKind(i.Dst.Type), x)
	case ir.ConvIntToString:
		e.use("core.string.from_rune")
		if unsigned64(i.X.IRType()) {
			return "from_rune_u(" + x + ")"
		}
		return "from_rune(" + x + ")"
	case ir.ConvStringToBytes:
		e.use("core.string.to_bytes")
		return "to_bytes(" + x + ")"
	case ir.ConvBytesToString:
		e.use("core.string.from_bytes")
		return "from_bytes(" + x + ")"
	case ir.ConvStringToRunes:
		e.use("core.string.to_runes")
		return "to_runes(" + x + ")"
	case ir.ConvRunesToString:
		e.use("core.string.from_runes")
		return "from_runes(" + x + ")"
	case ir.ConvSliceToArray:
		e.use("core.slice.to_array")
		at := i.Dst.Type.U()
		return fmt.Sprintf("slice_to_array(%s, %d, %s)", x, at.Len, e.cloneFn(at.Elem))
	}
	panic(fmt.Sprintf("rust: conversion %d", i.Kind))
}

func (fe *fnEmitter) sliceOp(i *ir.SliceOp) string {
	u := false
	opt := func(v ir.Value) string {
		if v == nil {
			return "V::Nil"
		}
		if unsigned64(v.IRType()) {
			u = true
		}
		return fe.val(v)
	}
	lo, hi, mx := opt(i.Lo), opt(i.Hi), opt(i.Max)
	x := fe.val(i.X)
	switch i.X.IRType().U().Kind {
	case ir.KString:
		fe.e.use("core.string.slice")
		return fmt.Sprintf("sslice(%s, %s, %s, %v)", x, lo, hi, u)
	case ir.KPointer:
		fe.e.use("core.slice.slice")
		return fmt.Sprintf("slice_array(%s, %s, %s, %s, %v)", x, lo, hi, mx, u)
	}
	fe.e.use("core.slice.slice")
	return fmt.Sprintf("reslice(%s, %s, %s, %s, %v)", x, lo, hi, mx, u)
}

func (fe *fnEmitter) typeAssert(i *ir.TypeAssert, w func(string, ...any)) {
	e := fe.e
	x := fe.val(i.X)
	if i.T.IsInterface() {
		var ids, names []string
		for _, m := range i.T.U().Methods {
			ids = append(ids, strconv.Quote(m.ID))
			names = append(names, strconv.Quote(m.Name))
		}
		w("let _x = %s;", x)
		test := fmt.Sprintf("implements(&_x, &[%s])", strings.Join(ids, ", "))
		if i.Ok != nil {
			w("let _ok = %s;", test)
			w("%s;", fe.set(i.Ok, "V::Bool(_ok)"))
			w("%s;", fe.set(i.Dst, "if _ok { _x } else { V::Nil }"))
			return
		}
		w("if !%s {", test)
		w("    assert_panic(&_x, %q, %q, missing_method(&_x, &[%s], &[%s]));", ir.TypeString(i.X.IRType()), ir.TypeString(i.T), strings.Join(ids, ", "), strings.Join(names, ", "))
		w("}")
		w("%s;", fe.set(i.Dst, "_x"))
		return
	}
	td := e.td(i.T)
	w("let _x = %s;", x)
	test := fmt.Sprintf("is_type(&_x, %s)", td)
	val := e.cloneExpr(i.T, "unboxed(&_x)", false)
	if i.Ok != nil {
		w("let _ok = %s;", test)
		w("%s;", fe.set(i.Ok, "V::Bool(_ok)"))
		w("%s;", fe.set(i.Dst, fmt.Sprintf("if _ok { %s } else { %s }", val, e.zero(i.T))))
		return
	}
	w("if !%s {", test)
	w("    assert_panic(&_x, %q, %q, None);", ir.TypeString(i.X.IRType()), ir.TypeString(i.T))
	w("}")
	w("%s;", fe.set(i.Dst, val))
}
