package lower

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/ir"
)

func (fl *fnLowerer) taskIntrinsic(e *ast.CallExpr) ([]ir.Value, bool) {
	if inner, ok := ast.Unparen(e.Fun).(*ast.CallExpr); ok && catalog.TaskIntrinsic(fl.callee(inner)) == "Await" {
		future := fl.expr(inner.Args[0])
		stateT := future.IRType().U().Fields[0].Type
		state := fl.load(&ir.Place{Root: ir.ValueRoot{Value: future}, Path: []ir.Proj{{Field: 0, Type: stateT}}, Type: stateT}, e)
		helper := fl.callee(inner).Pkg().Scope().Lookup("await")
		ext := fl.l.extern(helper)
		values := fl.temp(ext.Sig.Results[0])
		fl.emit(&ir.Call{At: at(e), Kind: ir.CallExtern, Extern: ext, Args: []ir.Value{state}, Dsts: []*ir.Local{values}})
		sig := fl.info.TypeOf(inner).Underlying().(*types.Signature)
		var out []ir.Value
		for i := 0; i < sig.Results().Len(); i++ {
			t := fl.typ(sig.Results().At(i).Type())
			v := fl.load(&ir.Place{Root: ir.SliceRoot{Slice: values, Index: &ir.Const{Type: fl.ts().IntT(), Val: constant.MakeInt64(int64(i))}}, Type: values.Type.U().Elem}, e)
			dst := fl.temp(t)
			fl.emit(&ir.TypeAssert{At: at(e), Dst: dst, Ok: fl.temp(fl.ts().Bool()), X: v, T: t})
			out = append(out, dst)
		}
		return out, true
	}
	obj := fl.callee(e)
	if catalog.TaskIntrinsic(obj) != "Start" {
		return nil, false
	}
	fn := fl.expr(e.Args[0])
	helper := obj.Pkg().Scope().Lookup("start")
	ext := fl.l.extern(helper)
	vector := types.NewSlice(types.Universe.Lookup("any").Type())
	callback := fl.typ(types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", vector)), false))
	adapter := fl.l.taskAdapter(fl.f.Pkg, fn.IRType(), callback)
	bound := fl.temp(callback)
	fl.emit(&ir.MakeBound{At: at(e), Dst: bound, Func: adapter, Recv: fn})
	boxed := fl.conv(bound, ext.Sig.Params[0], e)
	state := fl.temp(ext.Sig.Results[0])
	fl.emit(&ir.Call{At: at(e), Kind: ir.CallExtern, Extern: ext, Args: []ir.Value{boxed}, Dsts: []*ir.Local{state}})
	future := fl.temp(fl.typeOf(e))
	fl.emit(&ir.Zero{At: at(e), Dst: future})
	fl.emit(&ir.Store{At: at(e), Place: &ir.Place{Root: ir.LocalRoot{Local: future}, Path: []ir.Proj{{Field: 0, Type: state.Type}}, Type: state.Type}, V: state})
	return []ir.Value{future}, true
}

func (l *Lowerer) taskAdapter(pkg string, fnT, callback *ir.Type) *ir.Func {
	key := "task.Start|" + pkg + "|" + itoa(fnT.ID)
	if f := l.wrappers[key]; f != nil {
		return f
	}
	params := types.NewTuple(types.NewVar(token.NoPos, nil, "fn", fnT.Go))
	results := types.NewTuple(types.NewVar(token.NoPos, nil, "", callback.U().Results[0].Go))
	sig := l.ts.Of(types.NewSignatureType(nil, nil, nil, params, results, false))
	f := l.addFunc(&ir.Func{Name: "task.Start", Sym: l.sym("task_start"), Pkg: pkg, Wrapper: true, Sig: sig})
	l.wrappers[key] = f
	fl := l.newFn(nil, f)
	fl.start()
	fn := f.NewLocal("fn", fnT, ir.LParam)
	f.Params = []*ir.Local{fn}
	result := f.NewLocal("", callback.U().Results[0], ir.LResult)
	f.Results = []*ir.Local{result}
	fl.emit(&ir.DeclVar{L: result})
	call := &ir.Call{Kind: ir.CallValue, Fn: fn}
	for _, t := range fnT.U().Results {
		call.Dsts = append(call.Dsts, fl.temp(t))
	}
	fl.emit(call)
	n := &ir.Const{Type: l.ts.IntT(), Val: constant.MakeInt64(int64(len(call.Dsts)))}
	fl.emit(&ir.MakeSlice{Dst: result, Len: n, Cap: n})
	for i, value := range call.Dsts {
		boxed := fl.conv(value, result.Type.U().Elem, nil)
		fl.emit(&ir.Store{Place: &ir.Place{Root: ir.SliceRoot{Slice: result, Index: &ir.Const{Type: l.ts.IntT(), Val: constant.MakeInt64(int64(i))}}, Type: result.Type.U().Elem}, V: boxed})
	}
	fl.term(&ir.Return{})
	fl.finish()
	return f
}
