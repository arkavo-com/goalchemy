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
		name := "arg_" + fe.slot(p)
		params = append(params, name+": V")
		args = append(args, name)
		roots = append(roots, name+".clone()")
	}
	signature := append([]string{"t: &Rc<Task>"}, params...)
	fe.w("%s%sasync fn a_%s(%s) -> V {", position(f.Pos), e.visibility(), e.names.Symbol(f.Sym), strings.Join(signature, ", "))
	fe.indent = "    "
	fe.w("let _depth = native_guard(t);")
	fe.slotDecls()
	fe.w("let l = Fr::task(t, %d);", len(f.Locals))
	for _, p := range ps {
		fe.w("l.s(%s, arg_%s);", fe.slot(p), fe.slot(p))
	}
	fe.w("safepoint();")
	if fe.defers {
		fe.w("let p = match catch_async(async {")
		fe.indent = "        "
		fe.body()
		fe.indent = "    "
		fe.w("}).await { Ok(()) => V::Nil, Err(p) => p };")
		fe.w("run_defers_async(t, &l, p).await;")
		fe.w("%s", fe.results())
	} else {
		fe.body()
	}
	fe.indent = ""
	fe.w("}\n")
	fe.w("%sfn f_%s(%s) -> V {", e.visibility(), e.names.Symbol(f.Sym), strings.Join(params, ", "))
	fe.w("    native_task(vec![%s], %d, move |t| Box::pin(async move { a_%s(%s).await }))", strings.Join(roots, ", "), len(f.Results), e.names.Symbol(f.Sym), strings.Join(append([]string{"&t"}, args...), ", "))
	fe.w("}\n")
	return structure.Resolve(fe.b.String())
}

func (fe *fnEmitter) nativePause(p *ir.Pause) {
	e := fe.e
	const wait = "if t.blocked.get() { wait(t).await; }"
	switch i := p.Op.(type) {
	case *ir.Call:
		switch i.Kind {
		case ir.CallExtern:
			fe.w("%s(%s);", e.externSym(i.Extern.Contract), strings.Join(append([]string{"t"}, fe.externArgs(i)...), ", "))
			fe.w(wait)
		case ir.CallStatic:
			expr := "Box::pin(" + e.symbol(i.Func.Sym, "a_") + "(" + strings.Join(append([]string{"t"}, fe.args(i)...), ", ") + ")).await"
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
				fe.w("{ let r = %s;", expr)
				for k, d := range i.Dsts {
					if d != nil {
						fe.w("    %s;", fe.set(d, fmt.Sprintf("r.at(%d)", k)))
					}
				}
				fe.w("}")
			}
			return
		default:
			fe.w("await_frame(t, %s).await;", fe.callExpr(i))
		}
	case *ir.Send:
		e.use("core.chan.send")
		fe.w("chan_send(t, %s, %s);", fe.val(i.Ch), fe.val(i.V))
		fe.w(wait)
	case *ir.Recv:
		e.use("core.chan.recv")
		fe.w("chan_recv(t, %s);", fe.val(i.Ch))
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
		fe.w("select(t, %v, vec![%s]);", i.Default, strings.Join(cs, ", "))
		fe.w(wait)
	default:
		panic(fmt.Sprintf("rust: native pause on %T", p.Op))
	}
	fe.resume(p.Op)
}
