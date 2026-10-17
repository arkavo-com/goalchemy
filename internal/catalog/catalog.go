// Package catalog locates the contract catalog and exposes the external
// capability registry derived from it.
package catalog

import (
	"go/types"
	"io/fs"
	"os"
	"strings"

	"github.com/eugenioenko/goalchemy"
	"github.com/eugenioenko/goalchemy/internal/contracts"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
)

// FS returns the catalog root: GOALCHEMY_ROOT when set, else the embedded copy.
func FS() fs.FS {
	if root := os.Getenv("GOALCHEMY_ROOT"); root != "" {
		return os.DirFS(root)
	}
	return goalchemy.Assets
}

func Load(fsys fs.FS) (*contracts.Catalog, []diagnostics.Diagnostic) {
	return contracts.Load(fsys, contracts.LoadOptions{Resolve: Resolver(fsys)})
}

// Registry maps external Go symbols to capability contract IDs.
type Registry struct {
	Symbols  map[string]string
	Packages map[string]bool
	// Opaque lists external types ("pkg.Type") that are represented by
	// runtime handles; their methods are capability calls.
	Opaque map[string]bool
	// Suspends records contracts classified as suspension: may.
	Suspends map[string]bool
}

func NewRegistry(cat *contracts.Catalog) *Registry {
	r := &Registry{Symbols: map[string]string{}, Packages: map[string]bool{}, Opaque: map[string]bool{}, Suspends: map[string]bool{}}
	r.Opaque[TaskPackage+".FutureState"] = true
	if cat == nil {
		return r
	}
	for id, f := range cat.Functions {
		r.Suspends[id] = f.Suspension == "may"
		if f.Signature.Source != "declaration" {
			continue
		}
		sym := f.Signature.Symbol
		r.Symbols[sym] = id
		if i := strings.LastIndex(sym, "."); i > 0 {
			pkg := sym[:i]
			if j := strings.Index(pkg, ".("); j > 0 {
				r.Opaque[pkg[:j]+"."+strings.TrimSuffix(pkg[j+2:], ")")] = true
				pkg = pkg[:j]
			}
			r.Packages[pkg] = true
		}
	}
	return r
}

// SymbolKey renders the registry key for an object: pkg.Func or pkg.(Type).Method.
func SymbolKey(obj types.Object) string {
	if obj.Pkg() == nil {
		return obj.Name()
	}
	if fn, ok := obj.(*types.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil {
			t := recv.Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if n, ok := t.(*types.Named); ok {
				return obj.Pkg().Path() + ".(" + n.Obj().Name() + ")." + fn.Name()
			}
		}
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// External reports whether obj from a non-source package is usable:
// registered functions and variables, opaque handle types, constants of
// capability packages, and their named basic or function types.
func (r *Registry) External(obj types.Object) bool {
	if obj.Pkg() == nil {
		return false
	}
	if TaskIntrinsic(obj) != "" || obj.Pkg().Path() == TaskPackage && obj.Name() == "Future" {
		return true
	}
	lib := r.Packages[obj.Pkg().Path()] || strings.HasPrefix(obj.Pkg().Path(), LibModule)
	switch o := obj.(type) {
	case *types.Const:
		return lib
	case *types.TypeName:
		if o.IsAlias() {
			if n, ok := types.Unalias(o.Type()).(*types.Named); ok && r.IsOpaque(n.Obj()) {
				return true
			}
		}
		if r.Opaque[obj.Pkg().Path()+"."+obj.Name()] {
			return true
		}
		if !lib {
			return false
		}
		switch o.Type().Underlying().(type) {
		case *types.Basic, *types.Signature:
			return true
		}
		return false
	}
	_, ok := r.Symbols[SymbolKey(obj)]
	return ok
}

// IsOpaque reports whether a named type is a registered opaque handle.
func (r *Registry) IsOpaque(obj *types.TypeName) bool {
	return obj.Pkg() != nil && r.Opaque[obj.Pkg().Path()+"."+obj.Name()]
}

// Package reports whether a program may import path as a capability
// package: only Goalchemy's library packages qualify.
func (r *Registry) Package(path string) bool { return strings.HasPrefix(path, LibModule) }
