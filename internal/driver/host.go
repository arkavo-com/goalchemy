package driver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// HostNames parses the Go files a project compiles into the generated Go
// package and returns the names they declare: every package-level type,
// function, variable and constant, and every import name in file scope.
// Each path is a Go file or a directory whose Go files are read.
func HostNames(paths []string) ([]ir.HostName, error) {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
	}
	fset := token.NewFileSet()
	var names []ir.HostName
	seen := map[string]bool{}
	add := func(name string, pos token.Pos) {
		if name == "" || name == "_" || name == "." || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, ir.HostName{Name: name, Pos: fset.Position(pos).String()})
	}
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, imp := range f.Imports {
			if imp.Name != nil {
				add(imp.Name.Name, imp.Name.Pos())
				continue
			}
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				add(importName(p), imp.Path.Pos())
			}
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					add(d.Name.Name, d.Name.Pos())
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						add(s.Name.Name, s.Name.Pos())
					case *ast.ValueSpec:
						for _, n := range s.Names {
							add(n.Name, n.Pos())
						}
					}
				}
			}
		}
	}
	return names, nil
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// importName guesses the package name an import without an alias binds: the
// last path element, skipping a major version element and any suffix after
// a dot. A guess that is not an identifier reserves nothing.
func importName(importPath string) string {
	base := path.Base(importPath)
	if majorVersion.MatchString(base) && path.Dir(importPath) != "." {
		base = path.Base(path.Dir(importPath))
	}
	base, _, _ = strings.Cut(base, ".")
	if !token.IsIdentifier(base) {
		return ""
	}
	return base
}
