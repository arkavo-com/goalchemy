package subset

import (
	"go/ast"
	"go/types"

	"github.com/eugenioenko/goalchemy/internal/catalog"
)

func containsFuture(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if catalog.IsFuture(t) {
		return true
	}
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		if containsFuture(t.Underlying(), seen) {
			return true
		}
		for i := 0; i < t.NumMethods(); i++ {
			if method := t.Method(i); method.Exported() && containsFuture(method.Type(), seen) {
				return true
			}
		}
		return false
	case *types.Pointer:
		return containsFuture(t.Elem(), seen)
	case *types.Slice:
		return containsFuture(t.Elem(), seen)
	case *types.Array:
		return containsFuture(t.Elem(), seen)
	case *types.Chan:
		return containsFuture(t.Elem(), seen)
	case *types.Map:
		return containsFuture(t.Key(), seen) || containsFuture(t.Elem(), seen)
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if containsFuture(t.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Signature:
		return containsFuture(t.Params(), seen) || containsFuture(t.Results(), seen)
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if containsFuture(t.At(i).Type(), seen) {
				return true
			}
		}
	case *types.Interface:
		for i := 0; i < t.NumMethods(); i++ {
			if containsFuture(t.Method(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

func (c *checker) futureBoundaries() {
	for _, p := range c.prog.Roots {
		if p.Name == "main" {
			continue
		}
		for _, name := range p.Types.Scope().Names() {
			obj := p.Types.Scope().Lookup(name)
			if obj.Exported() && containsFuture(obj.Type(), map[types.Type]bool{}) {
				c.symbol = name
				c.report(&ast.Ident{Name: name, NamePos: obj.Pos()}, "GCS006", "future at library boundary", "task.Future cannot cross an exported library boundary or handle", "Await the future inside the library and export its completed results instead.")
			}
		}
	}
}
