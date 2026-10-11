// Package lower translates type-checked Goalchemy source into IR. It decides
// evaluation order, copy boundaries, receiver adaptation, promoted members,
// closure capture, and runtime intrinsic selection so that emitters do not.
package lower

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/ir"

	"golang.org/x/tools/go/packages"
)

type Lowerer struct {
	prog *frontend.Program
	reg  *catalog.Registry
	out  *ir.Program
	ts   *ir.Types

	pkgs       map[*types.Package]*packages.Package
	funcs      map[*types.Func]*ir.Func
	decls      map[*types.Func]*ast.FuncDecl
	globals    map[*types.Var]*ir.Global
	boxed      map[*types.Var]bool
	captures   map[*ast.FuncLit][]*types.Var
	wrappers   map[string]*ir.Func
	syms       map[string]bool
	pkgAlias   map[string]string
	localTypeN int

	diags []diagnostics.Diagnostic
}

// internalError aborts lowering of a function with a GCI diagnostic.
type internalError struct {
	pos token.Pos
	msg string
}

func Lower(prog *frontend.Program, reg *catalog.Registry) (*ir.Program, []diagnostics.Diagnostic) {
	l := &Lowerer{
		prog: prog, reg: reg, ts: ir.NewTypes(),
		pkgs: map[*types.Package]*packages.Package{}, funcs: map[*types.Func]*ir.Func{},
		decls: map[*types.Func]*ast.FuncDecl{}, globals: map[*types.Var]*ir.Global{},
		boxed: map[*types.Var]bool{}, captures: map[*ast.FuncLit][]*types.Var{},
		wrappers: map[string]*ir.Func{}, syms: map[string]bool{}, pkgAlias: map[string]string{},
	}
	l.out = &ir.Program{Types: l.ts, Externals: map[string]*ir.Extern{}, Fset: prog.Fset}
	if reg != nil {
		l.ts.Opaque = reg.IsOpaque
	}
	for _, p := range prog.Source {
		l.pkgs[p.Types] = p
		l.alias(p.PkgPath, p.Name)
		pkg := ir.Package{Path: p.PkgPath, Name: p.Name, Root: l.root(p), Imports: []string{}}
		for imported := range p.Imports {
			if prog.IsSource(imported) {
				pkg.Imports = append(pkg.Imports, imported)
			}
		}
		sort.Strings(pkg.Imports)
		l.out.Packages = append(l.out.Packages, pkg)
	}
	l.recordMethods()
	for _, p := range prog.Source {
		l.declare(p)
	}
	for _, r := range prog.Roots {
		if r.Name != "main" {
			l.out.Library = true
		}
	}
	for _, p := range prog.Source {
		l.analyze(p)
	}
	for _, p := range prog.Source {
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					obj := p.TypesInfo.Defs[fd.Name].(*types.Func)
					l.guard(fd.Pos(), func() { l.lowerDecl(p, fd, l.funcs[obj]) })
				}
			}
		}
	}
	l.guard(token.NoPos, l.lowerInit)
	l.guard(token.NoPos, l.jsonNilPointers)
	if l.out.Library {
		l.boxStreamHandles()
	}
	l.completeMethodSets()
	l.entry()
	for _, f := range l.out.Funcs {
		prune(f)
	}
	ir.ComputeEffects(l.out)
	ir.SplitPauses(l.out)
	for _, f := range l.out.Funcs {
		if err := ir.Verify(f); err != nil {
			l.diags = append(l.diags, diagnostics.Diagnostic{Code: "GCI002", Severity: diagnostics.Error,
				Symbol: f.Name, Feature: "IR verification", Message: err.Error(),
				Remedy: "This is a compiler defect; please report it with the source program."})
		}
	}
	diagnostics.Sort(l.diags)
	return l.out, l.diags
}

func (l *Lowerer) guard(pos token.Pos, f func()) {
	defer func() {
		if r := recover(); r != nil {
			ie, ok := r.(internalError)
			if !ok {
				ie = internalError{pos: pos, msg: fmt.Sprint(r)}
				if ie.pos == token.NoPos {
					ie.pos = pos
				}
			}
			d := diagnostics.Span(l.prog.Fset, ie.pos, token.NoPos)
			d.Code, d.Severity, d.Feature = "GCI001", diagnostics.Error, "lowering"
			d.Message = "cannot lower construct: " + ie.msg
			d.Remedy = "Rewrite the construct using supported features; report a compiler defect if it passed check."
			l.diags = append(l.diags, d)
		}
	}()
	f()
}

func fail(pos token.Pos, format string, args ...any) {
	panic(internalError{pos: pos, msg: fmt.Sprintf(format, args...)})
}

func (l *Lowerer) alias(path, name string) string {
	if a, ok := l.pkgAlias[path]; ok {
		return a
	}
	base := sanitize(name)
	a := base
	for i := 2; ; i++ {
		used := false
		for _, v := range l.pkgAlias {
			if v == a {
				used = true
			}
		}
		if !used {
			break
		}
		a = fmt.Sprintf("%s%d", base, i)
	}
	l.pkgAlias[path] = a
	return a
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// sym reserves a unique stable symbol.
func (l *Lowerer) sym(base string) string {
	s := sanitize(base)
	if !l.syms[s] {
		l.syms[s] = true
		return s
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s_%d", s, i)
		if !l.syms[c] {
			l.syms[c] = true
			return c
		}
	}
}

func (l *Lowerer) addFunc(f *ir.Func) *ir.Func {
	f.ID = len(l.out.Funcs)
	l.out.Funcs = append(l.out.Funcs, f)
	return f
}

// declare creates IR functions and globals for package-level declarations.
func (l *Lowerer) declare(p *packages.Package) {
	alias := l.alias(p.PkgPath, p.Name)
	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				obj := p.TypesInfo.Defs[d.Name].(*types.Func)
				sig := obj.Signature()
				name := p.Name + "." + obj.Name()
				symBase := alias + "_" + obj.Name()
				var recvT *ir.Type
				if sig.Recv() != nil {
					recvT = l.ts.Of(sig.Recv().Type())
					rn := recvTypeName(sig.Recv().Type())
					name = p.Name + "." + rn + "." + obj.Name()
					symBase = alias + "_" + rn + "_" + obj.Name()
				}
				irf := &ir.Func{Name: name, Sym: l.sym(symBase), Pkg: p.PkgPath, Sig: l.funcSig(sig), Pos: d.Pos()}
				if recvT != nil {
					irf.MethodID = l.ts.MethodID(obj)
					irf.RecvType = recvT
				}
				l.addFunc(irf)
				l.funcs[obj] = irf
				l.decls[obj] = d
				if obj.Name() == "main" && p.Name == "main" && sig.Recv() == nil {
					l.out.Main = irf
				}
				if l.root(p) && p.Name != "main" && obj.Exported() && sig.Recv() == nil {
					l.out.Exports = append(l.out.Exports, irf)
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, s := range d.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						v, ok := p.TypesInfo.Defs[n].(*types.Var)
						if !ok || n.Name == "_" {
							continue
						}
						g := &ir.Global{ID: len(l.out.Globals), Name: n.Name, Pkg: p.PkgPath,
							Sym: l.sym(alias + "_" + n.Name), Type: l.ts.Of(v.Type()), Pos: n.Pos()}
						l.out.Globals = append(l.out.Globals, g)
						l.globals[v] = g
					}
				}
			}
		}
	}
}

// root reports whether p is one of the packages the program was built from.
func (l *Lowerer) root(p *packages.Package) bool {
	for _, r := range l.prog.Roots {
		if r.PkgPath == p.PkgPath {
			return true
		}
	}
	return false
}

func recvTypeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		return "(*" + recvTypeName(p.Elem()) + ")"
	}
	if n, ok := types.Unalias(t).(*types.Named); ok {
		return n.Obj().Name()
	}
	return types.TypeString(t, nil)
}

// funcSig returns the IR function type with the receiver as first parameter.
func (l *Lowerer) funcSig(sig *types.Signature) *ir.Type {
	if sig.Recv() == nil {
		return l.ts.Of(sig)
	}
	var params []*types.Var
	params = append(params, sig.Recv())
	for i := 0; i < sig.Params().Len(); i++ {
		params = append(params, sig.Params().At(i))
	}
	return l.ts.Of(types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), sig.Results(), sig.Variadic()))
}

// analyze finds variables whose address is taken or that closures capture.
func (l *Lowerer) analyze(p *packages.Package) {
	info := p.TypesInfo
	var markAddr func(e ast.Expr)
	markAddr = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.ParenExpr:
			markAddr(x.X)
		case *ast.Ident:
			if v, ok := info.Uses[x].(*types.Var); ok {
				l.boxed[v] = true
				if g, ok := l.globals[v]; ok {
					g.AddrTaken = true
				}
			}
		case *ast.SelectorExpr:
			if sel := info.Selections[x]; sel != nil && sel.Kind() == types.FieldVal && !sel.Indirect() {
				markAddr(x.X)
			} else if sel == nil {
				// Package-qualified source globals have no field selection.
				// Their address needs the same stable storage as a local
				// package identifier in targets without native lvalues.
				if v, ok := info.Uses[x.Sel].(*types.Var); ok {
					if g, ok := l.globals[v]; ok {
						l.boxed[v] = true
						g.AddrTaken = true
					}
				}
			}
		case *ast.IndexExpr:
			if t := info.TypeOf(x.X); t != nil {
				if _, ok := t.Underlying().(*types.Array); ok {
					markAddr(x.X)
				}
			}
		}
	}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.UnaryExpr:
				if n.Op == token.AND {
					markAddr(n.X)
				}
			case *ast.SliceExpr:
				if t := info.TypeOf(n.X); t != nil {
					if _, ok := t.Underlying().(*types.Array); ok {
						markAddr(n.X)
					}
				}
			case *ast.SelectorExpr:
				sel := info.Selections[n]
				if sel == nil || sel.Kind() != types.MethodVal {
					break
				}
				fn := sel.Obj().(*types.Func)
				if r := fn.Signature().Recv(); r != nil {
					if _, ptr := r.Type().(*types.Pointer); ptr && !sel.Indirect() {
						if _, xptr := info.TypeOf(n.X).Underlying().(*types.Pointer); !xptr {
							markAddr(n.X)
						}
					}
				}
			case *ast.FuncLit:
				l.captures[n] = l.freeVars(info, n)
				for _, v := range l.captures[n] {
					l.boxed[v] = true
				}
			}
			return true
		})
	}
}

// freeVars lists local variables used in lit but declared outside it, in
// order of first use.
func (l *Lowerer) freeVars(info *types.Info, lit *ast.FuncLit) []*types.Var {
	var out []*types.Var
	seen := map[*types.Var]bool{}
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		v, ok := info.Uses[id].(*types.Var)
		if !ok || v.IsField() || seen[v] {
			return true
		}
		if _, global := l.globals[v]; global || v.Parent() == nil || v.Parent() == v.Pkg().Scope() {
			return true
		}
		if v.Pos() >= lit.Pos() && v.Pos() < lit.End() {
			return true
		}
		seen[v] = true
		out = append(out, v)
		return true
	})
	return out
}

func (l *Lowerer) lowerDecl(p *packages.Package, fd *ast.FuncDecl, f *ir.Func) {
	fl := l.newFn(p, f)
	sig := p.TypesInfo.Defs[fd.Name].(*types.Func).Signature()
	if fd.Recv != nil {
		fl.params(fd.Recv, []*types.Var{sig.Recv()}, ir.LParam)
		f.Recv = f.Params[0]
	}
	fl.params(fd.Type.Params, tupleVars(sig.Params()), ir.LParam)
	fl.results(fd.Type.Results, sig.Results())
	fl.body(fd.Body)
}

func tupleVars(t *types.Tuple) []*types.Var {
	out := make([]*types.Var, t.Len())
	for i := range out {
		out[i] = t.At(i)
	}
	return out
}

// lowerInit builds the program initialization function.
func (l *Lowerer) lowerInit() {
	f := l.addFunc(&ir.Func{Name: "$init", Sym: l.sym("goalchemy_init"), Sig: l.ts.Of(types.NewSignatureType(nil, nil, nil, nil, nil, false))})
	l.out.Init = f
	fl := l.newFn(nil, f)
	fl.start()
	for _, p := range l.prog.Source {
		fl.setPkg(p)
		for _, in := range p.TypesInfo.InitOrder {
			fl.initializer(in)
		}
		for _, file := range p.Syntax {
			for _, d := range file.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Name.Name != "init" || fd.Recv != nil {
					continue
				}
				obj := p.TypesInfo.Defs[fd.Name].(*types.Func)
				fl.emit(&ir.Call{At: ir.At{Pos: fd.Pos()}, Kind: ir.CallStatic, Func: l.funcs[obj]})
			}
		}
	}
	fl.finish()
}

// entry builds the program entry: package initialization, then main.
func (l *Lowerer) entry() {
	if l.out.Init == nil {
		return
	}
	f := l.addFunc(&ir.Func{Name: "$entry", Sym: l.sym("goalchemy_entry"), Sig: l.out.Init.Sig})
	b := f.NewBlock("entry")
	b.Instrs = append(b.Instrs, &ir.Call{Kind: ir.CallStatic, Func: l.out.Init})
	if l.out.Main != nil {
		b.Instrs = append(b.Instrs, &ir.Call{Kind: ir.CallStatic, Func: l.out.Main})
	}
	b.Term = &ir.Return{}
	l.out.Entry = f
}

// boxStreamHandles gives exported root structs whose pointers have Read or
// Write methods a method set, so library bindings can pass them to std/io.
func (l *Lowerer) boxStreamHandles() {
	for _, r := range l.prog.Roots {
		if r.Types == nil {
			continue
		}
		scope := r.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || !tn.Exported() || tn.IsAlias() {
				continue
			}
			if _, ok := tn.Type().Underlying().(*types.Struct); !ok {
				continue
			}
			ptr := types.NewPointer(tn.Type())
			ms := types.NewMethodSet(ptr)
			if ms.Lookup(tn.Pkg(), "Read") != nil || ms.Lookup(tn.Pkg(), "Write") != nil {
				l.ts.Of(ptr).Boxed = true
			}
		}
	}
}

// completeMethodSets fills the method set of every boxed type, creating
// wrappers for promoted methods and pointer receivers as needed.
func (l *Lowerer) completeMethodSets() {
	for i := 0; i < len(l.ts.All); i++ {
		t := l.ts.All[i]
		if t.Boxed && !t.MethodSetDone {
			l.guard(token.NoPos, func() { l.methodSet(t) })
		}
	}
}

func (l *Lowerer) methodSet(t *ir.Type) []*ir.MethodEntry {
	if t.MethodSetDone {
		return t.MethodSet
	}
	t.MethodSetDone = true
	if t.Kind == ir.KMapIter || t.Kind == ir.KTuple || t.IsInterface() {
		return nil
	}
	ms := types.NewMethodSet(t.Go)
	for i := 0; i < ms.Len(); i++ {
		sel := ms.At(i)
		fn := sel.Obj().(*types.Func)
		t.MethodSet = append(t.MethodSet, &ir.MethodEntry{ID: l.ts.MethodID(fn), Name: fn.Name(), Func: l.methodFunc(t, sel)})
	}
	sort.Slice(t.MethodSet, func(i, j int) bool { return t.MethodSet[i].ID < t.MethodSet[j].ID })
	return t.MethodSet
}

// declared returns the IR function for a source method.
func (l *Lowerer) declared(fn *types.Func) *ir.Func {
	fn = fn.Origin()
	f, ok := l.funcs[fn]
	if !ok {
		fail(fn.Pos(), "method %s is not included source", fn.FullName())
	}
	return f
}

// recordMethods registers every method name and signature the program can
// dispatch, so method identities only grow a fingerprint where one name is
// used with several signatures.
func (l *Lowerer) recordMethods() {
	record := func(fn *types.Func) {
		l.ts.RecordMethod(fn, fn.Pkg() == nil || !l.prog.IsSource(fn.Pkg().Path()))
	}
	for _, p := range l.prog.Source {
		for _, objs := range []map[*ast.Ident]types.Object{p.TypesInfo.Defs, p.TypesInfo.Uses} {
			for _, obj := range objs {
				switch o := obj.(type) {
				case *types.Func:
					if o.Signature().Recv() != nil {
						record(o)
					}
				case *types.TypeName:
					t := o.Type()
					sets := []*types.MethodSet{types.NewMethodSet(t)}
					if !types.IsInterface(t) {
						sets = append(sets, types.NewMethodSet(types.NewPointer(t)))
					}
					for _, ms := range sets {
						for i := 0; i < ms.Len(); i++ {
							record(ms.At(i).Obj().(*types.Func))
						}
					}
				}
			}
		}
	}
}
