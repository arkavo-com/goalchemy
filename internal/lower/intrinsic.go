package lower

import (
	"go/ast"
	"go/constant"
	"go/types"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/ir"
)

func (fl *fnLowerer) callee(e *ast.CallExpr) types.Object {
	switch f := ast.Unparen(e.Fun).(type) {
	case *ast.SelectorExpr:
		if fl.info.Selections[f] == nil {
			return fl.info.Uses[f.Sel]
		}
	case *ast.Ident:
		return fl.info.Uses[f]
	}
	return nil
}

func (fl *fnLowerer) intrinsic(e *ast.CallExpr) ([]ir.Value, bool) {
	if f, ok := ast.Unparen(e.Fun).(*ast.SelectorExpr); ok && fl.f.Pkg != catalog.JSONPackage {
		if sel := fl.info.Selections[f]; sel != nil && sel.Kind() == types.MethodVal {
			if name, ok := catalog.JSONStream(sel.Obj()); ok {
				return fl.jsonStream(e, f, sel, name), true
			}
		}
	}
	obj := fl.callee(e)
	if obj == nil {
		return nil, false
	}
	if name, ok := catalog.JSONMarshal(obj); ok {
		return fl.jsonMarshal(e, obj, name), true
	}
	if !catalog.IsErrorsAs(obj) {
		return nil, false
	}
	if obj.Pkg().Path() != catalog.ErrorsPackage {
		fl.term(&ir.PanicRuntime{At: at(e), Msg: "errors.As requires a target of static pointer type"})
		return []ir.Value{&ir.Const{Type: fl.ts().Bool(), Val: constant.MakeBool(false)}}, true
	}
	return []ir.Value{fl.errorsAs(e, obj)}, true
}

// errorsAs lowers errors.As(err, target) into a call of the source helper
// asTarget with a matcher that asserts each error to target's element type.
func (fl *fnLowerer) errorsAs(e *ast.CallExpr, as types.Object) ir.Value {
	helper, ok := as.Pkg().Scope().Lookup("asTarget").(*types.Func)
	if !ok {
		fail(e.Pos(), "errors.asTarget is missing")
	}
	f := fl.l.funcs[helper]
	if f == nil {
		fail(e.Pos(), "errors.asTarget is not lowered")
	}
	params := helper.Signature().Params()
	ptrT := fl.info.TypeOf(e.Args[1])
	if _, isPtr := ptrT.Underlying().(*types.Pointer); !isPtr {
		fail(e.Args[1].Pos(), "errors.As target must have a pointer type")
	}
	errV := fl.exprTo(e.Args[0], fl.typ(params.At(0).Type()))
	ptr := fl.exprTo(e.Args[1], fl.typ(ptrT))
	target := fl.conv(ptr, fl.typ(params.At(1).Type()), e)
	isNil := fl.temp(fl.ts().Bool())
	fl.emit(&ir.BinOp{At: at(e), Dst: isNil, Op: ir.Eq, X: ptr, Y: &ir.Const{Type: ptr.IRType(), Nil: true}})
	matchT := fl.typ(params.At(3).Type())
	match := fl.l.asMatcher(fl.f.Pkg, fl.typ(ptrT), matchT, fl.typ(params.At(0).Type()), fl.typ(params.At(1).Type()))
	d := fl.temp(fl.ts().Bool())
	fl.emit(&ir.Call{At: at(e), Kind: ir.CallStatic, Func: f, Dsts: []*ir.Local{d},
		Args: []ir.Value{errV, target, isNil, &ir.FuncRef{Func: match, Type: matchT}}})
	return d
}

// asMatcher builds func(err error, target any) bool for one target pointer
// type: it stores err into *target when err's dynamic value is assignable.
func (l *Lowerer) asMatcher(pkg string, ptrT, sig, errT, anyT *ir.Type) *ir.Func {
	key := "errors.As|" + pkg + "|" + ptrT.Go.String()
	if f, ok := l.wrappers[key]; ok {
		return f
	}
	elem := ptrT.U().Elem
	f := l.addFunc(&ir.Func{Name: "errors.As[" + ptrT.String() + "]", Sym: l.sym("errors_as_" + sanitize(elem.String())),
		Sig: sig, Pkg: pkg, Wrapper: true})
	l.wrappers[key] = f
	fl := l.newFn(nil, f)
	fl.start()
	errL := f.NewLocal("err", errT, ir.LParam)
	targetL := f.NewLocal("target", anyT, ir.LParam)
	f.Params = append(f.Params, errL, targetL)
	result := f.NewLocal("", l.ts.Bool(), ir.LResult)
	f.Results = append(f.Results, result)
	fl.emit(&ir.DeclVar{L: result})
	p := fl.temp(ptrT)
	fl.emit(&ir.TypeAssert{Dst: p, X: targetL, T: ptrT})
	v := fl.temp(elem)
	ok := fl.temp(l.ts.Bool())
	fl.emit(&ir.TypeAssert{Dst: v, Ok: ok, X: errL, T: elem})
	found := f.NewBlock("found")
	done := f.NewBlock("done")
	fl.term(&ir.If{Cond: ok, Then: found, Else: done})
	fl.b = found
	fl.emit(&ir.Store{Place: &ir.Place{Root: ir.DerefRoot{Ptr: p}, Type: elem}, V: v})
	fl.jump(done)
	fl.b = done
	fl.emit(&ir.Store{Place: localPlace(result), V: ok})
	fl.term(&ir.Return{})
	fl.finish()
	return f
}
