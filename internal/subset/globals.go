package subset

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/frontend"
)

const ioPackage = catalog.StdModule + "io"

type globalWrite struct {
	v    *types.Var
	node ast.Node
}

type scannedFunc struct {
	callees []*types.Func
	writes  []globalWrite
}

// libraryGlobals warns about package variables that calls of library exports
// without handles write after initialization. Such calls start from fresh
// package variables and discard them when they return, so the write is lost.
func libraryGlobals(prog *frontend.Program) []diagnostics.Diagnostic {
	roots := map[string]bool{}
	for _, p := range prog.Roots {
		if p.Name == "main" {
			return nil
		}
		roots[p.PkgPath] = true
	}
	decls := map[*types.Func]*ast.FuncDecl{}
	infos := map[*types.Func]*types.Info{}
	byName := map[string][]*types.Func{}
	var exports []*types.Func
	for _, p := range prog.Source {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn, ok := p.TypesInfo.Defs[fd.Name].(*types.Func)
				if !ok {
					continue
				}
				decls[fn], infos[fn] = fd, p.TypesInfo
				if fd.Recv != nil {
					byName[fn.Name()] = append(byName[fn.Name()], fn)
				} else if roots[p.PkgPath] && fn.Exported() && freshExport(fn, roots) {
					exports = append(exports, fn)
				}
			}
		}
	}
	sort.Slice(exports, func(i, j int) bool { return exports[i].Pos() < exports[j].Pos() })
	global := func(obj types.Object) *types.Var {
		v, ok := obj.(*types.Var)
		if !ok || v.IsField() || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() || catalog.IsStd(v.Pkg().Path()) || !prog.IsSource(v.Pkg().Path()) {
			return nil
		}
		return v
	}
	scanned := map[*types.Func]*scannedFunc{}
	scan := func(fn *types.Func) *scannedFunc {
		if s := scanned[fn]; s != nil {
			return s
		}
		s := &scannedFunc{}
		scanned[fn] = s
		info := infos[fn]
		root := func(e ast.Expr) *types.Var {
			for {
				switch x := ast.Unparen(e).(type) {
				case *ast.Ident:
					return global(info.Uses[x])
				case *ast.SelectorExpr:
					sel := info.Selections[x]
					if sel == nil {
						return global(info.Uses[x.Sel])
					}
					if sel.Kind() != types.FieldVal {
						return nil
					}
					e = x.X
				case *ast.IndexExpr:
					e = x.X
				case *ast.StarExpr:
					e = x.X
				default:
					return nil
				}
			}
		}
		write := func(e ast.Expr) {
			if v := root(e); v != nil {
				s.writes = append(s.writes, globalWrite{v, e})
			}
		}
		ast.Inspect(decls[fn].Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				if f, ok := info.Uses[n].(*types.Func); ok {
					s.callees = append(s.callees, f.Origin())
				}
			case *ast.SelectorExpr:
				sel := info.Selections[n]
				if sel == nil || sel.Kind() == types.FieldVal {
					break
				}
				m := sel.Obj().(*types.Func)
				if types.IsInterface(sel.Recv()) {
					s.callees = append(s.callees, byName[m.Name()]...)
					break
				}
				recv := m.Signature().Recv()
				if recv == nil || m.Pkg() == nil || !prog.IsSource(m.Pkg().Path()) {
					break
				}
				if _, ptr := recv.Type().(*types.Pointer); ptr {
					if _, xptr := info.TypeOf(n.X).Underlying().(*types.Pointer); !xptr {
						write(n.X)
					}
				}
			case *ast.AssignStmt:
				if n.Tok != token.DEFINE {
					for _, l := range n.Lhs {
						write(l)
					}
				}
			case *ast.IncDecStmt:
				write(n.X)
			case *ast.RangeStmt:
				if n.Tok == token.ASSIGN {
					if n.Key != nil {
						write(n.Key)
					}
					if n.Value != nil {
						write(n.Value)
					}
				}
			case *ast.CallExpr:
				if id, ok := ast.Unparen(n.Fun).(*ast.Ident); ok && len(n.Args) > 0 {
					if b, ok := info.Uses[id].(*types.Builtin); ok && (b.Name() == "delete" || b.Name() == "clear" || b.Name() == "copy") {
						write(n.Args[0])
					}
				}
			}
			return true
		})
		return s
	}
	type finding struct {
		w      globalWrite
		export *types.Func
	}
	found := map[*types.Var]finding{}
	for _, ex := range exports {
		seen := map[*types.Func]bool{ex: true}
		queue := []*types.Func{ex}
		for len(queue) > 0 {
			fn := queue[0]
			queue = queue[1:]
			s := scan(fn)
			for _, w := range s.writes {
				if f, ok := found[w.v]; !ok || f.export == ex && w.node.Pos() < f.w.node.Pos() {
					found[w.v] = finding{w, ex}
				}
			}
			for _, c := range s.callees {
				if !seen[c] && decls[c] != nil {
					seen[c] = true
					queue = append(queue, c)
				}
			}
		}
	}
	var out []diagnostics.Diagnostic
	for v, f := range found {
		d := diagnostics.Span(prog.Fset, f.w.node.Pos(), f.w.node.End())
		d.Code, d.Severity, d.Feature = "GCW001", diagnostics.Warning, "mutable package variable"
		d.Symbol = f.export.Pkg().Name() + "." + f.export.Name()
		d.Message = "package variable " + v.Name() + " is written during calls to " + f.export.Name() +
			", which start from fresh package variables and discard them on return"
		d.Remedy = "Keep state that must outlive a call in a handle: an exported struct returned by pointer, whose method calls share their instance's package variables."
		out = append(out, d)
	}
	return out
}

// freshExport reports whether calls of an exported function run without an
// instance: it neither receives nor returns a handle or a source stream.
func freshExport(fn *types.Func, roots map[string]bool) bool {
	sig := fn.Signature()
	for i := 0; i < sig.Params().Len(); i++ {
		if handleType(sig.Params().At(i).Type(), roots) {
			return false
		}
	}
	for i := 0; i < sig.Results().Len(); i++ {
		t := sig.Results().At(i).Type()
		if handleType(t, roots) {
			return false
		}
		if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == ioPackage && types.IsInterface(n) {
			return false
		}
	}
	return true
}

func handleType(t types.Type, roots map[string]bool) bool {
	p, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	n, ok := p.Elem().(*types.Named)
	if !ok || n.Obj().Pkg() == nil || !roots[n.Obj().Pkg().Path()] || !n.Obj().Exported() {
		return false
	}
	_, ok = n.Underlying().(*types.Struct)
	return ok
}
