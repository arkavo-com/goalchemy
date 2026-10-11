package lower

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/jsontype"
)

// jsonMarshal lowers json.Marshal and json.MarshalIndent. Operands of a
// static non-interface type are encoded through a generated descriptor of
// that type; interface operands are encoded from their dynamic value.
func (fl *fnLowerer) jsonMarshal(e *ast.CallExpr, obj types.Object, name string) []ir.Value {
	pkg := obj.Pkg()
	if name == "MarshalStream" || name == "UnmarshalStream" {
		return fl.jsonDynamicStream(e, name)
	}
	if pkg.Path() != catalog.JSONPackage {
		fl.term(&ir.PanicRuntime{At: at(e), Msg: "json: the native encoder is unavailable"})
		var zero []ir.Value
		res := obj.(*types.Func).Signature().Results()
		for i := 0; i < res.Len(); i++ {
			zero = append(zero, fl.zeroValue(fl.typ(res.At(i).Type())))
		}
		return zero
	}
	g := &jsonGen{l: fl.l, pkg: pkg}
	if name == "Unmarshal" {
		return fl.jsonUnmarshal(e, g)
	}
	anyT := fl.ts().Any()
	arg := e.Args[0]
	tv := fl.info.Types[arg]
	var args []ir.Value
	helper := "marshalAny"
	if tv.IsNil() || types.IsInterface(tv.Type) {
		args = append(args, fl.exprTo(arg, anyT))
	} else {
		args = append(args, fl.jsonBox(arg, e))
		helper = "marshalTyped"
	}
	if name == "MarshalIndent" {
		helper = map[string]string{"marshalAny": "marshalIndentAny", "marshalTyped": "marshalIndentTyped"}[helper]
		args = append(args, fl.exprTo(e.Args[1], fl.ts().String()), fl.exprTo(e.Args[2], fl.ts().String()))
	}
	if helper == "marshalTyped" || helper == "marshalIndentTyped" {
		desc := g.desc(tv.Type, false)
		d := fl.temp(desc.Sig.U().Results[0])
		fl.emit(&ir.Call{At: at(e), Kind: ir.CallStatic, Func: desc, Dsts: []*ir.Local{d}})
		args = append(args[:1], append([]ir.Value{d}, args[1:]...)...)
	}
	f := g.helper(helper)
	sig := f.Sig.U()
	var out []ir.Value
	c := &ir.Call{At: at(e), Kind: ir.CallStatic, Func: f, Args: args}
	for _, r := range sig.Results {
		d := fl.temp(r)
		c.Dsts = append(c.Dsts, d)
		out = append(out, d)
	}
	fl.emit(c)
	return out
}

// jsonUnmarshal lowers json.Unmarshal. A target of static pointer type
// decodes through a generated descriptor of the pointed-to type; interface
// targets decode into their dynamic value.
func (fl *fnLowerer) jsonUnmarshal(e *ast.CallExpr, g *jsonGen) []ir.Value {
	anyT := fl.ts().Any()
	data := fl.exprTo(e.Args[0], g.paramType("unmarshalAny", 0))
	arg := e.Args[1]
	tv := fl.info.Types[arg]
	var f *ir.Func
	var args []ir.Value
	if tv.IsNil() || types.IsInterface(tv.Type) {
		f = g.helper("unmarshalAny")
		args = []ir.Value{data, fl.exprTo(arg, anyT)}
	} else {
		f = g.helper("unmarshalTyped")
		args = append([]ir.Value{data}, fl.jsonTarget(arg, e, g)...)
	}
	out := fl.temp(f.Sig.U().Results[0])
	fl.emit(&ir.Call{At: at(e), Kind: ir.CallStatic, Func: f, Args: args, Dsts: []*ir.Local{out}})
	return []ir.Value{out}
}

// jsonBox evaluates a json.Marshal operand of static non-interface type into
// a fresh variable and returns a pointer to it as an any.
func (fl *fnLowerer) jsonBox(arg ast.Expr, e *ast.CallExpr) ir.Value {
	t := fl.typ(fl.info.Types[arg].Type)
	v := fl.exprTo(arg, t)
	p := fl.temp(fl.ts().PointerTo(t))
	fl.emit(&ir.New{At: at(e), Dst: p})
	fl.emit(&ir.Store{At: at(e), Place: &ir.Place{Root: ir.DerefRoot{Ptr: p}, Type: t}, V: v})
	return fl.conv(p, fl.ts().Any(), e)
}

// jsonTarget evaluates a json.Unmarshal target of static pointer type into
// the pointer as an any, the decoding descriptor of the pointed-to type,
// whether the pointer is nil and the root type name for errors.
func (fl *fnLowerer) jsonTarget(arg ast.Expr, e *ast.CallExpr, g *jsonGen) []ir.Value {
	tv := fl.info.Types[arg]
	elem := tv.Type.Underlying().(*types.Pointer).Elem()
	pt := fl.ts().PointerTo(fl.typ(elem))
	var x ir.Value = fl.snap(fl.exprTo(arg, fl.typ(tv.Type)))
	if x.IRType() != pt {
		c := fl.temp(pt)
		fl.emit(&ir.Convert{At: at(e), Dst: c, X: x, Kind: ir.ConvNop})
		x = c
	}
	isNil := fl.temp(fl.ts().Bool())
	fl.emit(&ir.BinOp{At: at(e), Dst: isNil, Op: ir.Eq, X: x, Y: &ir.Const{Type: pt, Nil: true}})
	desc := g.desc(elem, true)
	d := fl.temp(desc.Sig.U().Results[0])
	fl.emit(&ir.Call{At: at(e), Kind: ir.CallStatic, Func: desc, Dsts: []*ir.Local{d}})
	return []ir.Value{fl.conv(x, fl.ts().Any(), e), d, isNil, strConst(fl.ts(), jsontype.RootName(elem))}
}

// jsonStream lowers Encoder.Encode and Decoder.Decode like json.Marshal and
// json.Unmarshal, passing the receiver to the matching source helper.
func (fl *fnLowerer) jsonStream(e *ast.CallExpr, f *ast.SelectorExpr, sel *types.Selection, name string) []ir.Value {
	m := sel.Obj().(*types.Func)
	g := &jsonGen{l: fl.l, pkg: m.Pkg()}
	recv, _ := fl.receiver(f.X, sel.Index()[:len(sel.Index())-1], m, f)
	recv = fl.snap(recv)
	arg := e.Args[0]
	tv := fl.info.Types[arg]
	dynamic := tv.IsNil() || types.IsInterface(tv.Type)
	args := []ir.Value{recv}
	var helper *ir.Func
	switch {
	case name == "Encode" && dynamic:
		helper = g.helper("encoderAny")
		args = append(args, fl.exprTo(arg, fl.ts().Any()))
	case name == "Encode":
		helper = g.helper("encoderTyped")
		args = append(args, fl.jsonBox(arg, e))
		desc := g.desc(tv.Type, false)
		d := fl.temp(desc.Sig.U().Results[0])
		fl.emit(&ir.Call{At: at(e), Kind: ir.CallStatic, Func: desc, Dsts: []*ir.Local{d}})
		args = append(args, d)
	case dynamic:
		helper = g.helper("decoderAny")
		args = append(args, fl.exprTo(arg, fl.ts().Any()))
	default:
		helper = g.helper("decoderTyped")
		args = append(args, fl.jsonTarget(arg, e, g)...)
	}
	out := fl.temp(helper.Sig.U().Results[0])
	fl.emit(&ir.Call{At: at(e), Kind: ir.CallStatic, Func: helper, Args: args, Dsts: []*ir.Local{out}})
	return []ir.Value{out}
}

// jsonDynamicStream lowers the native lib/json stream calls in the bodies of
// Encoder.Encode and Decoder.Decode, which compiled programs reach only
// through interfaces, to their source counterparts for dynamic values.
func (fl *fnLowerer) jsonDynamicStream(e *ast.CallExpr, name string) []ir.Value {
	g := &jsonGen{l: fl.l, pkg: fl.p.Types}
	helper := g.helper("dynamic" + name)
	sig := helper.Sig.U()
	var args []ir.Value
	for i, a := range e.Args {
		args = append(args, fl.exprTo(a, sig.Params[i]))
	}
	c := &ir.Call{At: at(e), Kind: ir.CallStatic, Func: helper, Args: args}
	var out []ir.Value
	for _, r := range sig.Results {
		d := fl.temp(r)
		c.Dsts = append(c.Dsts, d)
		out = append(out, d)
	}
	fl.emit(c)
	return out
}

type jsonGen struct {
	l   *Lowerer
	pkg *types.Package
}

func (g *jsonGen) helper(name string) *ir.Func {
	obj, ok := g.pkg.Scope().Lookup(name).(*types.Func)
	if !ok {
		fail(token.NoPos, "json.%s is missing", name)
	}
	f := g.l.funcs[obj]
	if f == nil {
		fail(token.NoPos, "json.%s is not lowered", name)
	}
	return f
}

func (g *jsonGen) paramType(helper string, i int) *ir.Type {
	obj := g.pkg.Scope().Lookup(helper).(*types.Func)
	return g.l.ts.Of(obj.Signature().Params().At(i).Type())
}

func (g *jsonGen) typeID(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Path() })
}

func strConst(ts *ir.Types, s string) ir.Value {
	return &ir.Const{Type: ts.String(), Val: constant.MakeString(s)}
}

func intConst(ts *ir.Types, n int64) ir.Value {
	return &ir.Const{Type: ts.IntT(), Val: constant.MakeInt64(n)}
}

func boolConst(ts *ir.Types, b bool) ir.Value {
	return &ir.Const{Type: ts.Bool(), Val: constant.MakeBool(b)}
}

// desc returns the function that builds, once, the std/encoding/json
// descriptor of t for encoding or, with dec, for decoding. The descriptor is
// cached in a global before its children are described, so recursive types
// terminate.
func (g *jsonGen) desc(t types.Type, dec bool) *ir.Func {
	l := g.l
	mode := "desc"
	if dec {
		mode = "ddesc"
	}
	key := "json." + mode + "|" + g.typeID(t)
	if f, ok := l.wrappers[key]; ok {
		return f
	}
	ts := l.ts
	newType := g.helper("newType")
	infoT := newType.Sig.U().Results[0]
	sig := ts.Of(types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", infoT.Go)), false))
	base := sanitize(jsontype.TypeString(t))
	f := l.addFunc(&ir.Func{Name: "json." + mode + "[" + jsontype.TypeString(t) + "]", Sym: l.sym("json_" + mode + "_" + base),
		Sig: sig, Pkg: g.pkg.Path(), Wrapper: true})
	l.wrappers[key] = f
	cache := &ir.Global{ID: len(l.out.Globals), Name: mode + "_" + base, Pkg: g.pkg.Path(), Sym: l.sym("json_" + mode + "_cache_" + base), Type: infoT}
	l.out.Globals = append(l.out.Globals, cache)

	fl := l.newFn(nil, f)
	fl.start()
	result := f.NewLocal("", infoT, ir.LResult)
	f.Results = append(f.Results, result)
	fl.emit(&ir.DeclVar{L: result})
	cachePlace := &ir.Place{Root: ir.GlobalRoot{Global: cache}, Type: infoT}
	cached := fl.load(cachePlace, nil)
	missing := fl.temp(ts.Bool())
	fl.emit(&ir.BinOp{Dst: missing, Op: ir.Eq, X: cached, Y: &ir.Const{Type: infoT, Nil: true}})
	hit := f.NewBlock("hit")
	build := f.NewBlock("build")
	fl.term(&ir.If{Cond: missing, Then: build, Else: hit})
	fl.b = hit
	fl.emit(&ir.Store{Place: localPlace(result), V: cached})
	fl.term(&ir.Return{})
	fl.b = build

	kind := jsontype.KindOf(t)
	flags := jsontype.Flags(t)
	leaf := jsontype.Leaf(t)
	if dec {
		flags = jsontype.DecodeFlags(t)
		leaf = jsontype.DecodeLeaf(t)
	}
	var n int64
	if a, ok := t.Underlying().(*types.Array); ok {
		n = a.Len()
	}
	info := fl.temp(infoT)
	fl.emit(&ir.Call{Kind: ir.CallStatic, Func: newType, Dsts: []*ir.Local{info},
		Args: []ir.Value{strConst(ts, jsontype.TypeString(t)), intConst(ts, int64(kind)), intConst(ts, int64(flags)), intConst(ts, n),
			intConst(ts, int64(jsontype.Bits(t)))}})
	fl.emit(&ir.Store{Place: cachePlace, V: info})

	nilInfo := &ir.Const{Type: infoT, Nil: true}
	elem, keyDesc := ir.Value(nilInfo), ir.Value(nilInfo)
	none := func(helper string, i int) ir.Value { return &ir.Const{Type: g.paramType(helper, i), Nil: true} }
	acc := func(helper string, i int, what string) ir.Value { return g.accessor(t, what, g.paramType(helper, i)) }
	var enc [6]ir.Value
	for i := range enc {
		enc[i] = none("describe", i+3)
	}
	var decAcc [10]ir.Value
	for i := range decAcc {
		decAcc[i] = none("describeDecode", i+1)
	}
	call := func(d *ir.Func) ir.Value {
		dst := fl.temp(infoT)
		fl.emit(&ir.Call{Kind: ir.CallStatic, Func: d, Dsts: []*ir.Local{dst}})
		return dst
	}
	if !dec && flags&(jsontype.MarshalJSON|jsontype.MarshalText) != 0 && kind != jsontype.Pointer {
		enc[1] = acc("describe", 4, "value")
	}
	const (
		dStore = iota
		dSetNil
		dAlloc
		dResize
		dSetIndex
		dNewElem
		dNewKey
		dMapInit
		dMapStore
		dSetIface
	)
	decodeChildren := dec && !leaf
	switch kind {
	case jsontype.Bool, jsontype.Int, jsontype.Uint, jsontype.Float32, jsontype.Float64, jsontype.String:
		if dec {
			decAcc[dStore] = acc("describeDecode", 1, "store")
		} else {
			enc[0] = acc("describe", 3, "load")
		}
	case jsontype.Bytes:
		if dec {
			decAcc[dStore] = acc("describeDecode", 1, "store")
			decAcc[dSetNil] = acc("describeDecode", 2, "setNil")
			if decodeChildren {
				enc[3] = acc("describe", 6, "length")
				enc[4] = acc("describe", 7, "index")
				decAcc[dResize] = acc("describeDecode", 4, "resize")
				decAcc[dSetIndex] = acc("describeDecode", 5, "setIndex")
				elem = call(g.desc(t.Underlying().(*types.Slice).Elem(), true))
			}
		} else {
			enc[0] = acc("describe", 3, "load")
		}
	case jsontype.Interface:
		enc[1] = acc("describe", 4, "value")
		if dec {
			decAcc[dSetNil] = acc("describeDecode", 2, "setNil")
			if flags&jsontype.NonEmptyInterface == 0 {
				decAcc[dSetIface] = acc("describeDecode", 10, "setIface")
			}
		}
	case jsontype.Pointer:
		enc[2] = acc("describe", 5, "deref")
		if dec {
			decAcc[dSetNil] = acc("describeDecode", 2, "setNil")
			decAcc[dAlloc] = acc("describeDecode", 3, "alloc")
		}
		if !leaf {
			elem = call(g.desc(t.Underlying().(*types.Pointer).Elem(), dec))
		}
	case jsontype.Slice:
		enc[3] = acc("describe", 6, "length")
		if dec {
			decAcc[dSetNil] = acc("describeDecode", 2, "setNil")
		}
		if !leaf {
			enc[4] = acc("describe", 7, "index")
			elem = call(g.desc(t.Underlying().(*types.Slice).Elem(), dec))
			if dec {
				decAcc[dResize] = acc("describeDecode", 4, "resize")
				decAcc[dSetIndex] = acc("describeDecode", 5, "setIndex")
			}
		}
	case jsontype.Array:
		if !leaf {
			enc[4] = acc("describe", 7, "index")
			elem = call(g.desc(t.Underlying().(*types.Array).Elem(), dec))
			if dec {
				decAcc[dSetIndex] = acc("describeDecode", 5, "setIndex")
				decAcc[dNewElem] = acc("describeDecode", 6, "newElem")
			}
		}
	case jsontype.Map:
		enc[3] = acc("describe", 6, "length")
		if dec {
			decAcc[dSetNil] = acc("describeDecode", 2, "setNil")
		}
		if !leaf {
			m := t.Underlying().(*types.Map)
			keyDesc = call(g.desc(m.Key(), dec))
			elem = call(g.desc(m.Elem(), dec))
			if dec {
				decAcc[dNewElem] = acc("describeDecode", 6, "newElem")
				decAcc[dNewKey] = acc("describeDecode", 7, "newKey")
				decAcc[dMapInit] = acc("describeDecode", 8, "mapInit")
				decAcc[dMapStore] = acc("describeDecode", 9, "mapStore")
			} else {
				enc[5] = acc("describe", 8, "entries")
			}
		}
	}
	fl.emit(&ir.Call{Kind: ir.CallStatic, Func: g.helper("describe"),
		Args: append([]ir.Value{info, elem, keyDesc}, enc[:]...)})
	if dec {
		fl.emit(&ir.Call{Kind: ir.CallStatic, Func: g.helper("describeDecode"), Args: append([]ir.Value{info}, decAcc[:]...)})
	}
	if kind == jsontype.Struct && !leaf {
		addField := g.helper("addField")
		addrT := g.paramType("addField", 7)
		for _, fd := range jsontype.Fields(t) {
			ft := call(g.desc(fd.Type, dec))
			fl.emit(&ir.Call{Kind: ir.CallStatic, Func: addField, Args: []ir.Value{info, strConst(ts, fd.Name), strConst(ts, fd.Key),
				boolConst(ts, fd.OmitEmpty), boolConst(ts, fd.Quoted), boolConst(ts, fd.ViaPtr), ft, g.fieldAccessor(t, fd, addrT, dec)}})
		}
	}
	fl.emit(&ir.Store{Place: localPlace(result), V: info})
	fl.term(&ir.Return{})
	fl.finish()
	return f
}

func (g *jsonGen) newAccessor(t types.Type, what string, sig *ir.Type) (*fnLowerer, *ir.Func, *ir.Local) {
	l := g.l
	base := sanitize(jsontype.TypeString(t))
	f := l.addFunc(&ir.Func{Name: "json." + what + "[" + jsontype.TypeString(t) + "]", Sym: l.sym("json_" + what + "_" + base),
		Sig: sig, Pkg: g.pkg.Path(), Wrapper: true})
	fl := l.newFn(nil, f)
	fl.start()
	u := sig.U()
	for i, pt := range u.Params {
		name := "p"
		if i > 0 {
			name = "i"
		}
		f.Params = append(f.Params, f.NewLocal(name, pt, ir.LParam))
	}
	for _, rt := range u.Results {
		r := f.NewLocal("", rt, ir.LResult)
		f.Results = append(f.Results, r)
		fl.emit(&ir.DeclVar{L: r})
	}
	ptrT := l.ts.Of(types.NewPointer(t))
	fl.markBoxed(ptrT)
	ptr := fl.temp(ptrT)
	fl.emit(&ir.TypeAssert{Dst: ptr, X: f.Params[0], T: ptrT})
	return fl, f, ptr
}

func (g *jsonGen) accessor(t types.Type, what string, sig *ir.Type) ir.Value {
	key := "json." + what + "|" + g.typeID(t)
	if f, ok := g.l.wrappers[key]; ok {
		return &ir.FuncRef{Func: f, Type: sig}
	}
	if what == "newElem" || what == "newKey" {
		return g.allocator(t, what, sig, key)
	}
	fl, f, ptr := g.newAccessor(t, what, sig)
	g.l.wrappers[key] = f
	ts := g.l.ts
	tt := ts.Of(t)
	anyT := ts.Any()
	v := fl.load(&ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, nil)
	res := f.Results
	ret := func(x ir.Value) {
		fl.emit(&ir.Store{Place: localPlace(res[0]), V: x})
		fl.term(&ir.Return{})
	}
	isNil := func(x ir.Value) (*ir.Block, *ir.Block) {
		c := fl.temp(ts.Bool())
		fl.emit(&ir.BinOp{Dst: c, Op: ir.Eq, X: x, Y: &ir.Const{Type: x.IRType(), Nil: true}})
		yes, no := f.NewBlock("nil"), f.NewBlock("value")
		fl.term(&ir.If{Cond: c, Then: yes, Else: no})
		return yes, no
	}
	switch what {
	case "load":
		ret(fl.conv(g.canonical(fl, v, t), anyT, nil))
	case "value":
		ret(fl.conv(v, anyT, nil))
	case "deref":
		yes, no := isNil(v)
		fl.b = yes
		fl.term(&ir.Return{})
		fl.b = no
		ret(fl.conv(v, anyT, nil))
	case "length":
		yes, no := isNil(v)
		fl.b = yes
		ret(intConst(ts, -1))
		fl.b = no
		n := fl.temp(ts.IntT())
		fl.emit(&ir.Len{Dst: n, X: v})
		ret(n)
	case "index":
		var el *ir.Local
		switch u := t.Underlying().(type) {
		case *types.Slice:
			el = fl.load(&ir.Place{Root: ir.SliceRoot{Slice: v, Index: f.Params[1]}, Type: ts.Of(u.Elem())}, nil)
		case *types.Array:
			et := ts.Of(u.Elem())
			el = fl.load(&ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Path: []ir.Proj{{Index: f.Params[1], Type: et}}, Type: et}, nil)
		}
		ret(fl.conv(g.copyOf(fl, el), anyT, nil))
	case "entries":
		m := t.Underlying().(*types.Map)
		yes, no := isNil(v)
		fl.b = yes
		fl.emit(&ir.Store{Place: localPlace(res[2]), V: boolConst(ts, true)})
		fl.term(&ir.Return{})
		fl.b = no
		mt := ts.Of(t)
		iter := fl.temp(ts.MapIter(mt))
		ok := fl.temp(ts.Bool())
		k := fl.temp(ts.Of(m.Key()))
		val := fl.temp(ts.Of(m.Elem()))
		fl.emit(&ir.MapIterInit{Iter: iter, M: v})
		head, body, done := f.NewBlock("range.head"), f.NewBlock("range.body"), f.NewBlock("range.done")
		fl.jump(head)
		fl.b = head
		fl.emit(&ir.MapIterNext{Ok: ok, Key: k, Val: val, Iter: iter})
		fl.term(&ir.If{Cond: ok, Then: body, Else: done})
		fl.b = body
		for i, x := range []*ir.Local{k, val} {
			s := fl.temp(res[i].Type)
			fl.emit(&ir.Append{Dst: s, S: res[i], Elems: []ir.Value{fl.conv(g.copyOf(fl, x), anyT, nil)}})
			fl.emit(&ir.Store{Place: localPlace(res[i]), V: s})
		}
		fl.jump(head)
		fl.b = done
		fl.term(&ir.Return{})
	case "store":
		fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, V: g.fromCanonical(fl, f.Params[1], t)})
		fl.term(&ir.Return{})
	case "setNil":
		fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, V: &ir.Const{Type: tt, Nil: true}})
		fl.term(&ir.Return{})
	case "setIface":
		fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, V: fl.conv(f.Params[1], tt, nil)})
		fl.term(&ir.Return{})
	case "alloc":
		e := t.Underlying().(*types.Pointer).Elem()
		np := fl.temp(ts.PointerTo(ts.Of(e)))
		fl.emit(&ir.New{Dst: np})
		var pv ir.Value = np
		if np.Type != tt {
			c := fl.temp(tt)
			fl.emit(&ir.Convert{Dst: c, X: np, Kind: ir.ConvNop})
			pv = c
		}
		fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, V: pv})
		ret(fl.conv(np, anyT, nil))
	case "resize":
		n := f.Params[1]
		store := func(x ir.Value) {
			fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, V: x})
			fl.term(&ir.Return{})
		}
		c := fl.temp(ts.IntT())
		fl.emit(&ir.Cap{Dst: c, X: v})
		branch := func(op ir.BinOpKind, x, y ir.Value) *ir.Block {
			cond := fl.temp(ts.Bool())
			fl.emit(&ir.BinOp{Dst: cond, Op: op, X: x, Y: y})
			yes, no := f.NewBlock("then"), f.NewBlock("else")
			fl.term(&ir.If{Cond: cond, Then: yes, Else: no})
			fl.b = no
			return yes
		}
		neg := branch(ir.Lt, n, intConst(ts, 0))
		zero := branch(ir.Eq, n, intConst(ts, 0))
		fits := branch(ir.Le, n, c)
		size := fl.temp(ts.IntT())
		fl.emit(&ir.BinOp{Dst: size, Op: ir.Add, X: n, Y: c})
		grown := fl.temp(tt)
		fl.emit(&ir.MakeSlice{Dst: grown, Len: n, Cap: size})
		copied := fl.temp(ts.IntT())
		fl.emit(&ir.Copy{N: copied, Dst: grown, Src: v})
		store(grown)
		fl.b = neg
		full := fl.temp(tt)
		fl.emit(&ir.SliceOp{Dst: full, X: v, Hi: c})
		store(full)
		fl.b = zero
		empty := fl.temp(tt)
		fl.emit(&ir.MakeSlice{Dst: empty, Len: intConst(ts, 0), Cap: intConst(ts, 0)})
		store(empty)
		fl.b = fits
		cut := fl.temp(tt)
		fl.emit(&ir.SliceOp{Dst: cut, X: v, Hi: n})
		store(cut)
	case "setIndex":
		var et *ir.Type
		switch u := t.Underlying().(type) {
		case *types.Slice:
			et = ts.Of(u.Elem())
		case *types.Array:
			et = ts.Of(u.Elem())
		}
		ep := g.assertPointer(fl, f.Params[2], et)
		ev := fl.load(&ir.Place{Root: ir.DerefRoot{Ptr: ep}, Type: et}, nil)
		if _, ok := t.Underlying().(*types.Slice); ok {
			fl.emit(&ir.Store{Place: &ir.Place{Root: ir.SliceRoot{Slice: v, Index: f.Params[1]}, Type: et}, V: ev})
		} else {
			fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Path: []ir.Proj{{Index: f.Params[1], Type: et}}, Type: et}, V: ev})
		}
		fl.term(&ir.Return{})
	case "mapInit":
		yes, no := isNil(v)
		fl.b = yes
		m := fl.temp(tt)
		fl.emit(&ir.MakeMap{Dst: m})
		fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: tt}, V: m})
		fl.term(&ir.Return{})
		fl.b = no
		fl.term(&ir.Return{})
	case "mapStore":
		m := t.Underlying().(*types.Map)
		kt, vt := ts.Of(m.Key()), ts.Of(m.Elem())
		k := fl.load(&ir.Place{Root: ir.DerefRoot{Ptr: g.assertPointer(fl, f.Params[1], kt)}, Type: kt}, nil)
		val := fl.load(&ir.Place{Root: ir.DerefRoot{Ptr: g.assertPointer(fl, f.Params[2], vt)}, Type: vt}, nil)
		fl.emit(&ir.MapStore{M: v, K: k, V: val})
		fl.term(&ir.Return{})
	}
	fl.finish()
	return &ir.FuncRef{Func: f, Type: sig}
}

func (g *jsonGen) assertPointer(fl *fnLowerer, x ir.Value, elem *ir.Type) *ir.Local {
	pt := fl.ts().PointerTo(elem)
	fl.markBoxed(pt)
	p := fl.temp(pt)
	fl.emit(&ir.TypeAssert{Dst: p, X: x, T: pt})
	return p
}

// allocator returns func() any yielding a pointer to a new zero element or
// key of the slice, array or map type t.
func (g *jsonGen) allocator(t types.Type, what string, sig *ir.Type, key string) ir.Value {
	l := g.l
	var e types.Type
	switch u := t.Underlying().(type) {
	case *types.Map:
		e = u.Elem()
		if what == "newKey" {
			e = u.Key()
		}
	case *types.Slice:
		e = u.Elem()
	case *types.Array:
		e = u.Elem()
	}
	f := l.addFunc(&ir.Func{Name: "json." + what + "[" + jsontype.TypeString(t) + "]", Sym: l.sym("json_" + what + "_" + sanitize(jsontype.TypeString(t))),
		Sig: sig, Pkg: g.pkg.Path(), Wrapper: true})
	l.wrappers[key] = f
	fl := l.newFn(nil, f)
	fl.start()
	r := f.NewLocal("", sig.U().Results[0], ir.LResult)
	f.Results = append(f.Results, r)
	fl.emit(&ir.DeclVar{L: r})
	np := fl.temp(l.ts.PointerTo(l.ts.Of(e)))
	fl.emit(&ir.New{Dst: np})
	fl.emit(&ir.Store{Place: localPlace(r), V: fl.conv(np, l.ts.Any(), nil)})
	fl.term(&ir.Return{})
	fl.finish()
	return &ir.FuncRef{Func: f, Type: sig}
}

// fromCanonical converts the canonical value boxed in v (bool, int64,
// uint64, float64, string or []byte) to t.
func (g *jsonGen) fromCanonical(fl *fnLowerer, v ir.Value, t types.Type) ir.Value {
	tt := fl.typ(t)
	ct := g.canonicalType(t)
	fl.markBoxed(ct)
	c := fl.temp(ct)
	fl.emit(&ir.TypeAssert{Dst: c, X: v, T: ct})
	if ct == tt {
		return c
	}
	fu, tu := ct.U(), tt.U()
	kind := ir.ConvNop
	switch {
	case fu.Kind == ir.KFloat:
		kind = ir.ConvFloat
	case fu.Kind == ir.KInt && tu.Kind == ir.KInt && fu.Int != tu.Int:
		kind = ir.ConvInt
	}
	dst := fl.temp(tt)
	fl.emit(&ir.Convert{Dst: dst, X: c, Kind: kind})
	return dst
}

func (g *jsonGen) canonicalType(t types.Type) *ir.Type {
	var to types.Type
	switch jsontype.KindOf(t) {
	case jsontype.Bool:
		to = types.Typ[types.Bool]
	case jsontype.Int:
		to = types.Typ[types.Int64]
	case jsontype.Uint:
		to = types.Typ[types.Uint64]
	case jsontype.Float32, jsontype.Float64:
		to = types.Typ[types.Float64]
	case jsontype.String:
		to = types.Typ[types.String]
	case jsontype.Bytes:
		to = types.NewSlice(types.Typ[types.Uint8])
	}
	return g.l.ts.Of(to)
}

func (g *jsonGen) copyOf(fl *fnLowerer, v *ir.Local) ir.Value {
	pt := fl.ts().PointerTo(v.Type)
	p := fl.temp(pt)
	fl.emit(&ir.New{Dst: p})
	fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: p}, Type: v.Type}, V: v})
	return p
}

// canonical converts a basic or byte-slice value to the representation
// std/encoding/json reads: bool, int64, uint64, float64, string or []byte.
func (g *jsonGen) canonical(fl *fnLowerer, v ir.Value, t types.Type) ir.Value {
	var to types.Type
	switch jsontype.KindOf(t) {
	case jsontype.Bool:
		to = types.Typ[types.Bool]
	case jsontype.Int:
		to = types.Typ[types.Int64]
	case jsontype.Uint:
		to = types.Typ[types.Uint64]
	case jsontype.Float32, jsontype.Float64:
		to = types.Typ[types.Float64]
	case jsontype.String:
		to = types.Typ[types.String]
	case jsontype.Bytes:
		to = types.NewSlice(types.Typ[types.Uint8])
	}
	tt := fl.typ(to)
	if v.IRType() == tt {
		return v
	}
	fu, tu := v.IRType().U(), tt.U()
	kind := ir.ConvNop
	switch {
	case fu.Kind == ir.KFloat:
		kind = ir.ConvFloat
	case fu.Kind == ir.KInt && tu.Kind == ir.KInt && fu.Int != tu.Int:
		kind = ir.ConvInt
	}
	dst := fl.temp(tt)
	fl.emit(&ir.Convert{Dst: dst, X: v, Kind: kind})
	return dst
}

// fieldAccessor returns func(p any) any yielding a pointer to the field at
// fd.Index within *p, or nil when an embedded pointer on the path is nil.
func (g *jsonGen) fieldAccessor(t types.Type, fd jsontype.Field, sig *ir.Type, dec bool) ir.Value {
	what := "field_" + fd.Name
	if dec && fd.ViaPtr {
		what = "dfield_" + fd.Name
	}
	fl, f, ptr := g.newAccessor(t, what, sig)
	ts := g.l.ts
	place := &ir.Place{Root: ir.DerefRoot{Ptr: ptr}, Type: ts.Of(t)}
	st := t
	for i, idx := range fd.Index {
		if i > 0 {
			if p, ok := types.Unalias(st).(*types.Pointer); ok {
				pv := fl.load(place, nil)
				c := fl.temp(ts.Bool())
				fl.emit(&ir.BinOp{Dst: c, Op: ir.Eq, X: pv, Y: &ir.Const{Type: pv.Type, Nil: true}})
				yes, no := f.NewBlock("nil"), f.NewBlock("embedded")
				fl.term(&ir.If{Cond: c, Then: yes, Else: no})
				fl.b = yes
				if dec {
					np := fl.temp(ts.PointerTo(ts.Of(p.Elem())))
					fl.emit(&ir.New{Dst: np})
					fl.emit(&ir.Store{Place: place, V: fl.conv(np, place.Type, nil)})
					fl.jump(no)
				} else {
					fl.term(&ir.Return{})
				}
				fl.b = no
				pv = fl.load(place, nil)
				st = p.Elem()
				place = &ir.Place{Root: ir.DerefRoot{Ptr: pv}, Type: ts.Of(st)}
			}
		}
		field := st.Underlying().(*types.Struct).Field(idx)
		place = withProj(place, ir.Proj{Field: idx, Type: ts.Of(field.Type())})
		st = field.Type()
	}
	addr := fl.temp(ts.PointerTo(place.Type))
	fl.emit(&ir.AddrOf{Dst: addr, Place: place})
	fl.emit(&ir.Store{Place: localPlace(f.Results[0]), V: fl.conv(addr, ts.Any(), nil)})
	fl.term(&ir.Return{})
	fl.finish()
	return &ir.FuncRef{Func: f, Type: sig}
}

func (fl *fnLowerer) zeroValue(t *ir.Type) ir.Value {
	switch t.U().Kind {
	case ir.KBool:
		return &ir.Const{Type: t, Val: constant.MakeBool(false)}
	case ir.KInt, ir.KFloat:
		return &ir.Const{Type: t, Val: constant.MakeInt64(0)}
	case ir.KString:
		return &ir.Const{Type: t, Val: constant.MakeString("")}
	}
	return &ir.Const{Type: t, Nil: true}
}

// jsonNilPointers fills std/encoding/json.isNilPointer once every function is
// lowered: it reports whether an interface holds a nil pointer of a type,
// converted to an interface somewhere in the program, that implements
// Marshaler or encoding.TextMarshaler, which encoding/json encodes as null.
func (l *Lowerer) jsonNilPointers() {
	var fn *ir.Func
	for obj, f := range l.funcs {
		if obj.Name() == "isNilPointer" && obj.Pkg() != nil && obj.Pkg().Path() == catalog.JSONPackage && obj.Signature().Recv() == nil {
			fn = f
		}
	}
	if fn == nil {
		return
	}
	var ptrs []*ir.Type
	for _, t := range l.ts.All {
		if !t.Boxed || t.U().Kind != ir.KPointer {
			continue
		}
		if jsontype.HasMethods(t.Go) {
			ptrs = append(ptrs, t)
		}
	}
	fn.Blocks = nil
	fl := l.newFn(nil, fn)
	fl.start()
	result := fn.Results[0]
	fl.emit(&ir.DeclVar{L: result})
	for _, t := range ptrs {
		v := fl.temp(t)
		ok := fl.temp(l.ts.Bool())
		fl.emit(&ir.TypeAssert{Dst: v, Ok: ok, X: fn.Params[0], T: t})
		match, next := fn.NewBlock("match"), fn.NewBlock("next")
		fl.term(&ir.If{Cond: ok, Then: match, Else: next})
		fl.b = match
		isNil := fl.temp(l.ts.Bool())
		fl.emit(&ir.BinOp{Dst: isNil, Op: ir.Eq, X: v, Y: &ir.Const{Type: t, Nil: true}})
		fl.emit(&ir.Store{Place: localPlace(result), V: isNil})
		fl.term(&ir.Return{})
		fl.b = next
	}
	fl.term(&ir.Return{})
	fl.finish()
}
