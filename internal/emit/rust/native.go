package rust

import (
	"fmt"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/structure"
)

// nativeFunction keeps the frames-convention starter for function values,
// methods, go, defer and library exports. Static calls await the async body.
func (e *emitter) nativeFunction(f *ir.Func) string {
	e.use("core.task.spawn")
	fe := &fnEmitter{e: e, f: f, order: map[*ir.Block]int{}, native: true}
	fe.allLocals()
	for i, b := range f.Blocks {
		fe.order[b] = i
		for _, in := range b.Instrs {
			if _, ok := in.(*ir.Defer); ok {
				fe.defers = true
			}
		}
	}
	fe.analyze()
	var params, args, roots []string
	ps := append(append([]*ir.Local(nil), f.Env...), f.Params...)
	for _, p := range ps {
		name := "_arg_" + fe.slot(p)
		params = append(params, name+": V")
		args = append(args, name)
		roots = append(roots, name+".clone()")
	}
	signature := append([]string{"_t: &Rc<Task>"}, params...)
	fe.w("%s%sasync fn %s(%s) -> V {", position(f.Pos), e.visibility(), e.fnName(f.Sym, "a_"), strings.Join(signature, ", "))
	fe.indent = "    "
	fe.w("let _depth = native_guard(_t);")
	fe.slotDecls()
	fe.w("let _l = Fr::task(_t, %d);", len(f.Locals))
	for _, p := range ps {
		fe.w("_l.s(%s, _arg_%s);", fe.slot(p), fe.slot(p))
	}
	fe.w("safepoint();")
	if fe.defers {
		fe.w("let _p = match catch_async(async {")
		fe.indent = "        "
		fe.body()
		fe.indent = "    "
		fe.w("}).await { Ok(()) => V::Nil, Err(_p) => _p };")
		fe.w("run_defers_async(_t, &_l, _p).await;")
		fe.w("%s", fe.results())
	} else {
		fe.body()
	}
	fe.indent = ""
	fe.w("}\n")
	fe.w("%sfn %s(%s) -> V {", e.visibility(), e.fnName(f.Sym, "f_"), strings.Join(params, ", "))
	fe.w("    native_task(vec![%s], %d, move |_t| Box::pin(async move { %s(%s).await }))", strings.Join(roots, ", "), len(f.Results), e.fnName(f.Sym, "a_"), strings.Join(append([]string{"&_t"}, args...), ", "))
	fe.w("}\n")
	return structure.Resolve(fe.b.String())
}

func (fe *fnEmitter) nativePause(p *ir.Pause) {
	e := fe.e
	const wait = "if _t.blocked.get() { wait(_t).await; }"
	switch i := p.Op.(type) {
	case *ir.Call:
		switch i.Kind {
		case ir.CallExtern:
			fe.w("%s(%s);", e.externSym(i.Extern.Contract), strings.Join(append([]string{"_t"}, fe.externArgs(i)...), ", "))
			fe.w(wait)
		case ir.CallStatic:
			expr := "Box::pin(" + e.symbol(i.Func.Sym, "a_") + "(" + strings.Join(append([]string{"_t"}, fe.args(i)...), ", ") + ")).await"
			switch len(i.Dsts) {
			case 0:
				fe.w("%s;", expr)
			case 1:
				if i.Dsts[0] == nil {
					fe.w("%s;", expr)
				} else {
					fe.w("%s;", fe.set(i.Dsts[0], expr))
				}
			default:
				fe.w("{ let _r = %s;", expr)
				for k, d := range i.Dsts {
					if d != nil {
						fe.w("    %s;", fe.set(d, fmt.Sprintf("_r.at(%d)", k)))
					}
				}
				fe.w("}")
			}
			return
		default:
			fe.w("await_frame(_t, %s).await;", fe.callExpr(i))
		}
	case *ir.Send:
		e.use("core.chan.send")
		fe.w("chan_send(_t, %s, %s);", fe.val(i.Ch), fe.val(i.V))
		fe.w(wait)
	case *ir.Recv:
		e.use("core.chan.recv")
		fe.w("chan_recv(_t, %s);", fe.val(i.Ch))
		fe.w(wait)
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
		fe.w(wait)
	default:
		panic(fmt.Sprintf("rust: native pause on %T", p.Op))
	}
	fe.resume(p.Op)
}
