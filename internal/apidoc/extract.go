package apidoc

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/frontend"
	"golang.org/x/tools/go/packages"
)

type extractor struct {
	program  *frontend.Program
	packages map[string]*packages.Package
	roots    map[string]bool
	comments map[types.Object]string
	symbols  map[string]*Symbol
	queue    []types.Object
}

// Extract collects root exports and reachable named types using stable source identities.
func Extract(program *frontend.Program, title, compiler string) *Catalog {
	e := &extractor{program: program, packages: map[string]*packages.Package{}, roots: map[string]bool{}, comments: map[types.Object]string{}, symbols: map[string]*Symbol{}}
	var visit func(*packages.Package)
	visit = func(p *packages.Package) {
		if p == nil || e.packages[p.PkgPath] != nil {
			return
		}
		e.packages[p.PkgPath] = p
		e.indexComments(p)
		for _, imported := range p.Imports {
			visit(imported)
		}
	}
	for _, p := range append(append([]*packages.Package{}, program.Source...), program.External...) {
		visit(p)
	}
	for _, p := range program.Roots {
		e.roots[p.PkgPath] = true
		e.packages[p.PkgPath] = p
	}
	for _, p := range program.Roots {
		for _, name := range p.Types.Scope().Names() {
			obj := p.Types.Scope().Lookup(name)
			if obj.Exported() {
				e.queue = append(e.queue, obj)
			}
		}
	}
	for len(e.queue) > 0 {
		obj := e.queue[0]
		e.queue = e.queue[1:]
		e.add(obj)
	}
	c := &Catalog{SchemaVersion: 1, Compiler: compiler, Title: title, Packages: []Package{}, Symbols: []*Symbol{}, Targets: []Target{}}
	for _, symbol := range e.symbols {
		c.Symbols = append(c.Symbols, symbol)
	}
	sort.Slice(c.Symbols, func(i, j int) bool { return c.Symbols[i].ID < c.Symbols[j].ID })
	byPackage := map[string][]string{}
	for _, symbol := range c.Symbols {
		byPackage[symbol.Package] = append(byPackage[symbol.Package], symbol.ID)
	}
	for path := range e.roots {
		if _, ok := byPackage[path]; !ok {
			byPackage[path] = []string{}
		}
	}
	for path, members := range byPackage {
		p := e.packages[path]
		if p == nil {
			continue
		}
		item := Package{ID: path, Name: p.Name, Root: e.roots[path], Symbols: members}
		if len(p.Syntax) > 0 {
			if d, err := doc.NewFromFiles(program.Fset, p.Syntax, path, doc.PreserveAST); err == nil {
				item.Description = d.Doc
			}
		}
		c.Packages = append(c.Packages, item)
	}
	sort.Slice(c.Packages, func(i, j int) bool { return c.Packages[i].ID < c.Packages[j].ID })
	return c
}

func objectID(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	name := obj.Name()
	if f, ok := obj.(*types.Func); ok {
		if recv := f.Signature().Recv(); recv != nil {
			if n := named(recv.Type()); n != nil {
				name = n.Obj().Name() + "." + name
			}
		}
	}
	return obj.Pkg().Path() + "#" + name
}
func named(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, _ := types.Unalias(t).(*types.Named)
	return n
}

func (e *extractor) indexComments(p *packages.Package) {
	if p.TypesInfo == nil {
		return
	}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			var names []*ast.Ident
			var text string
			switch d := n.(type) {
			case *ast.FuncDecl:
				names = []*ast.Ident{d.Name}
				text = d.Doc.Text()
			case *ast.TypeSpec:
				names = []*ast.Ident{d.Name}
				text = d.Doc.Text()
			case *ast.ValueSpec:
				names = d.Names
				text = d.Doc.Text()
			case *ast.Field:
				names = d.Names
				text = d.Doc.Text()
				if text == "" {
					text = d.Comment.Text()
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if s.Doc == nil {
							e.comments[p.TypesInfo.Defs[s.Name]] = d.Doc.Text()
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if s.Doc == nil {
								e.comments[p.TypesInfo.Defs[name]] = d.Doc.Text()
							}
						}
					}
				}
			}
			for _, name := range names {
				if obj := p.TypesInfo.Defs[name]; obj != nil && text != "" {
					e.comments[obj] = text
				}
			}
			return true
		})
	}
}

func (e *extractor) typeRef(t types.Type) Type {
	ref := Type{Text: types.TypeString(t, func(p *types.Package) string { return p.Name() })}
	seen := map[types.Type]bool{}
	ids := map[string]bool{}
	var walk func(types.Type)
	walk = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Named:
			if id := objectID(t.Obj()); id != "" {
				ids[id] = true
				e.queue = append(e.queue, t.Obj())
			}
		case *types.Alias:
			if id := objectID(t.Obj()); id != "" {
				ids[id] = true
				e.queue = append(e.queue, t.Obj())
			}
		case *types.Pointer:
			walk(t.Elem())
		case *types.Slice:
			walk(t.Elem())
		case *types.Array:
			walk(t.Elem())
		case *types.Map:
			walk(t.Key())
			walk(t.Elem())
		case *types.Chan:
			walk(t.Elem())
		case *types.Signature:
			for i := 0; i < t.Params().Len(); i++ {
				walk(t.Params().At(i).Type())
			}
			for i := 0; i < t.Results().Len(); i++ {
				walk(t.Results().At(i).Type())
			}
		case *types.Struct:
			for i := 0; i < t.NumFields(); i++ {
				if t.Field(i).Exported() {
					walk(t.Field(i).Type())
				}
			}
		case *types.Interface:
			for i := 0; i < t.NumEmbeddeds(); i++ {
				walk(t.EmbeddedType(i))
			}
			for i := 0; i < t.NumExplicitMethods(); i++ {
				if t.ExplicitMethod(i).Exported() {
					walk(t.ExplicitMethod(i).Type())
				}
			}
		}
	}
	walk(t)
	for id := range ids {
		ref.References = append(ref.References, id)
	}
	sort.Strings(ref.References)
	return ref
}
func (e *extractor) tuple(t *types.Tuple) []Field {
	var fields []Field
	for i := 0; i < t.Len(); i++ {
		v := t.At(i)
		fields = append(fields, Field{Name: v.Name(), Type: e.typeRef(v.Type())})
	}
	return fields
}
func (e *extractor) add(obj types.Object) {
	id := objectID(obj)
	if id == "" || e.symbols[id] != nil {
		return
	}
	s := &Symbol{ID: id, Package: obj.Pkg().Path(), Name: obj.Name(), Root: e.roots[obj.Pkg().Path()], Description: e.comments[obj]}
	e.symbols[id] = s
	pos := e.program.Fset.Position(obj.Pos())
	s.Location = Location{File: filepath.Base(pos.Filename), Line: pos.Line}
	if i := strings.Index(s.Description, "Deprecated:"); i >= 0 {
		s.Deprecated = strings.TrimSpace(strings.SplitN(s.Description[i+len("Deprecated:"):], "\n\n", 2)[0])
	}
	qualify := func(p *types.Package) string {
		if p == obj.Pkg() {
			return ""
		}
		return p.Name()
	}
	s.Signature = types.ObjectString(obj, qualify)
	switch obj := obj.(type) {
	case *types.TypeName:
		s.Kind = "type"
		if obj.IsAlias() {
			s.Kind = "alias"
			rhs := obj.Type()
			if a, ok := rhs.(*types.Alias); ok {
				rhs = a.Rhs()
			}
			r := e.typeRef(rhs)
			s.References = append(s.References, r.References...)
			s.Signature = "type " + s.Name + " = " + r.Text
			return
		}
		n := named(obj.Type())
		if n == nil {
			return
		}
		underlying := n.Underlying()
		ref := e.typeRef(underlying)
		s.References = append(s.References, ref.References...)
		if fields, ok := underlying.(*types.Struct); ok {
			var lines []string
			for i := 0; i < fields.NumFields(); i++ {
				f := fields.Field(i)
				if !f.Exported() {
					continue
				}
				field := Field{Name: f.Name(), Type: e.typeRef(f.Type()), Description: e.comments[f], Tag: fields.Tag(i), Embedded: f.Embedded()}
				s.Fields = append(s.Fields, field)
				line := f.Name() + " " + field.Type.Text
				if f.Embedded() {
					line = field.Type.Text
				}
				if field.Tag != "" {
					line += " `" + field.Tag + "`"
				}
				lines = append(lines, "    "+line)
			}
			s.Signature = "type " + s.Name + " struct {\n" + strings.Join(lines, "\n") + "\n}"
		} else {
			s.Signature = "type " + s.Name + " " + ref.Text
		}
		for i := 0; s.Root && i < n.NumMethods(); i++ {
			m := n.Method(i)
			if m.Exported() {
				s.Members = append(s.Members, objectID(m))
				e.queue = append(e.queue, m)
			}
		}
		if iface, ok := underlying.(*types.Interface); ok && s.Root {
			for i := 0; i < iface.NumExplicitMethods(); i++ {
				m := iface.ExplicitMethod(i)
				if m.Exported() {
					s.Members = append(s.Members, objectID(m))
					e.queue = append(e.queue, m)
				}
			}
		}
	case *types.Func:
		s.Kind = "function"
		sig := obj.Signature()
		s.Parameters = e.tuple(sig.Params())
		s.Results = e.tuple(sig.Results())
		if recv := sig.Recv(); recv != nil {
			s.Kind = "method"
			name := recv.Name()
			if name == "" {
				name = "receiver"
			}
			s.Signature = "func (" + name + " " + types.TypeString(recv.Type(), qualify) + ") " + obj.Name() + strings.TrimPrefix(types.TypeString(sig, qualify), "func")
			if n := named(recv.Type()); n != nil {
				s.Parent = objectID(n.Obj())
				e.queue = append(e.queue, n.Obj())
			}
		}
		ref := e.typeRef(sig)
		s.References = append(s.References, ref.References...)
	case *types.Const:
		s.Kind = "constant"
		s.Signature += " = " + obj.Val().ExactString()
		ref := e.typeRef(obj.Type())
		s.References = ref.References
	case *types.Var:
		s.Kind = "variable"
		ref := e.typeRef(obj.Type())
		s.References = ref.References
	default:
		delete(e.symbols, id)
	}
}

// Check verifies catalog references after extraction or deserialization.
func (c *Catalog) Check() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported API schema version %d", c.SchemaVersion)
	}
	ids := map[string]*Symbol{}
	pkgs := map[string]bool{}
	targets := map[string]string{}
	for _, p := range c.Packages {
		if p.ID == "" || pkgs[p.ID] {
			return fmt.Errorf("invalid or duplicate API package %q", p.ID)
		}
		pkgs[p.ID] = true
	}
	for _, target := range c.Targets {
		if target.Name == "" || targets[target.Name] != "" || (target.Status != "available" && target.Status != "unavailable") {
			return fmt.Errorf("invalid or duplicate API target %q", target.Name)
		}
		targets[target.Name] = target.Status
	}
	for _, s := range c.Symbols {
		if s == nil || s.ID == "" || ids[s.ID] != nil {
			return fmt.Errorf("invalid or duplicate API symbol")
		}
		if !pkgs[s.Package] {
			return fmt.Errorf("%s belongs to missing package %s", s.ID, s.Package)
		}
		ids[s.ID] = s
	}
	for _, p := range c.Packages {
		for _, id := range p.Symbols {
			if ids[id] == nil || ids[id].Package != p.ID {
				return fmt.Errorf("%s contains invalid symbol %s", p.ID, id)
			}
		}
	}
	for _, s := range c.Symbols {
		refs := append(append(append([]string{}, s.References...), s.Members...), s.Parent)
		for _, fields := range [][]Field{s.Parameters, s.Results, s.Fields} {
			for _, f := range fields {
				refs = append(refs, f.Type.References...)
			}
		}
		for _, id := range refs {
			if id != "" && ids[id] == nil {
				return fmt.Errorf("%s references missing symbol %s", s.ID, id)
			}
		}
		for _, b := range s.Bindings {
			if targets[b.Target] != "available" || b.Name == "" || b.Signature == "" {
				return fmt.Errorf("%s has invalid binding for %s", s.ID, b.Target)
			}
		}
	}
	return nil
}

// SymbolID returns a package-qualified symbol identity for native emission metadata.
func SymbolID(pkg, name string) string { return pkg + "#" + name }
