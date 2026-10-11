// Package frontend loads Go packages under the locked Goalchemy source profile.
package frontend

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"go/version"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"

	"golang.org/x/tools/go/packages"
)

const (
	SourceLanguage     = "go1.25"
	ReferenceToolchain = "go1.27.1"
	SourceGOOS         = "linux"
	SourceGOARCH       = "amd64"
)

type Options struct {
	Dir      string
	Patterns []string
	Tags     []string
	// External reports whether an import path is a declared external capability
	// package rather than included source.
	External func(path string) bool
	// SourceLib reports whether an import path outside the main module is
	// library source compiled with the program.
	SourceLib func(path string) bool
}

type Program struct {
	Fset *token.FileSet
	// Roots are the packages matched by the patterns.
	Roots []*packages.Package
	// Source holds every included source package in dependency order.
	Source []*packages.Package
	// External holds imported packages that are not included source.
	External []*packages.Package
	Tags     []string
}

func (p *Program) IsSource(path string) bool {
	for _, s := range p.Source {
		if s.PkgPath == path {
			return true
		}
	}
	return false
}

func Load(ctx context.Context, opts Options) (*Program, []diagnostics.Diagnostic) {
	patterns := opts.Patterns
	if len(patterns) == 0 {
		patterns = []string{"."}
	}
	fset := token.NewFileSet()
	env := append(os.Environ(),
		"GOOS="+SourceGOOS, "GOARCH="+SourceGOARCH, "CGO_ENABLED=0",
		"GOTOOLCHAIN="+ReferenceToolchain, "GOFLAGS=-mod=mod")
	cfg := &packages.Config{
		Context: ctx,
		Dir:     opts.Dir,
		Fset:    fset,
		Env:     env,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule |
			packages.NeedImports | packages.NeedDeps,
	}
	tags := append([]string(nil), opts.Tags...)
	sort.Strings(tags)
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	prog := &Program{Fset: fset, Tags: tags}
	var ds []diagnostics.Diagnostic
	roots, err := packages.Load(cfg, patterns...)
	if err != nil {
		ds = append(ds, diagnostics.Diagnostic{Code: "GCL001", Severity: diagnostics.Error,
			Feature: "package loading", Message: err.Error(),
			Remedy: "Check the package patterns and the Go module configuration."})
		return prog, ds
	}
	if len(roots) == 0 {
		ds = append(ds, diagnostics.Diagnostic{Code: "GCL002", Severity: diagnostics.Error,
			Feature: "source selection", Message: "no packages matched " + strings.Join(patterns, " "),
			Remedy: "Provide a package pattern that matches Go source files."})
		return prog, ds
	}
	// Library exports may take std/io streams from the host; their adapter is
	// source, loaded with the roots so it shares their std/io types.
	var hostIO *packages.Package
	if needsHostIO(roots) {
		all, err := packages.Load(cfg, append(append([]string{}, patterns...), HostIOPackage)...)
		if err == nil {
			var kept []*packages.Package
			var extra []*packages.Package
			for _, r := range all {
				if r.PkgPath == HostIOPackage {
					extra = append(extra, r)
				} else {
					kept = append(kept, r)
				}
			}
			if len(kept) == len(roots) && len(extra) == 1 {
				roots, hostIO = kept, extra[0]
			}
		}
	}
	prog.Roots = roots
	external := opts.External
	if external == nil {
		external = func(string) bool { return false }
	}
	sourceLib := opts.SourceLib
	if sourceLib == nil {
		sourceLib = func(string) bool { return false }
	}
	seen := map[string]bool{}
	var visit func(p *packages.Package)
	visit = func(p *packages.Package) {
		if seen[p.PkgPath] {
			return
		}
		seen[p.PkgPath] = true
		if external(p.PkgPath) || !(isSource(p) || sourceLib(p.PkgPath)) {
			prog.External = append(prog.External, p)
			return
		}
		paths := make([]string, 0, len(p.Imports))
		for path := range p.Imports {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			visit(p.Imports[path])
		}
		prog.Source = append(prog.Source, p)
	}
	for _, r := range roots {
		visit(r)
	}
	if hostIO != nil {
		visit(hostIO)
	}
	for _, p := range prog.Source {
		ds = append(ds, packageErrors(p)...)
		ds = append(ds, languageVersion(fset, p)...)
	}
	for _, r := range roots {
		if !prog.IsSource(r.PkgPath) {
			ds = append(ds, diagnostics.Diagnostic{Code: "GCL002", Severity: diagnostics.Error,
				Symbol: r.PkgPath, Feature: "source selection",
				Message: fmt.Sprintf("root package %s is not part of the main module", r.PkgPath),
				Remedy:  "Select packages from the module being transpiled."})
		}
	}
	diagnostics.Sort(ds)
	return prog, diagnostics.Dedup(ds)
}

// HostIOPackage adapts host streams passed as std/io export parameters.
const HostIOPackage = "github.com/eugenioenko/goalchemy/std/io/hostio"

const ioPackage = "github.com/eugenioenko/goalchemy/std/io"

// needsHostIO reports whether an exported function or method of a library
// root takes or returns a std/io interface, or whether an exported type has
// Read or Write methods that hosts can use as a stream.
func needsHostIO(roots []*packages.Package) bool {
	isIO := func(vs *types.Tuple) bool {
		for i := 0; i < vs.Len(); i++ {
			if n, ok := vs.At(i).Type().(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == ioPackage {
				return true
			}
		}
		return false
	}
	usesIO := func(sig *types.Signature) bool { return isIO(sig.Params()) || isIO(sig.Results()) }
	streamMethod := func(m *types.Func) bool {
		if m.Name() != "Read" && m.Name() != "Write" {
			return false
		}
		sig := m.Signature()
		if sig.Params().Len() != 1 {
			return false
		}
		s, ok := sig.Params().At(0).Type().(*types.Slice)
		return ok && types.Identical(s.Elem(), types.Typ[types.Byte])
	}
	for _, r := range roots {
		if r.Name == "main" || r.Types == nil {
			continue
		}
		scope := r.Types.Scope()
		for _, name := range scope.Names() {
			switch obj := scope.Lookup(name).(type) {
			case *types.Func:
				if obj.Exported() && usesIO(obj.Signature()) {
					return true
				}
			case *types.TypeName:
				if !obj.Exported() {
					continue
				}
				if n, ok := obj.Type().(*types.Named); ok {
					for i := 0; i < n.NumMethods(); i++ {
						if m := n.Method(i); m.Exported() && (usesIO(m.Signature()) || streamMethod(m)) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func isSource(p *packages.Package) bool {
	return p.Module != nil && p.Module.Main
}

func packageErrors(p *packages.Package) []diagnostics.Diagnostic {
	var ds []diagnostics.Diagnostic
	for _, e := range p.Errors {
		code, feature := "GCL003", "Go parsing or typing"
		remedy := "Fix the Go error; Goalchemy analyzes only valid Go."
		if e.Kind == packages.ListError {
			code, feature = "GCL001", "package loading"
			remedy = "Fix the package configuration or import path."
		}
		file, line, col := parsePosition(e.Pos)
		ds = append(ds, diagnostics.Diagnostic{Code: code, Severity: diagnostics.Error,
			File: file, Line: line, Column: col, Symbol: p.PkgPath, Feature: feature,
			Message: e.Msg, Remedy: remedy})
	}
	if len(p.Errors) > 0 {
		ds = append(ds, diagnostics.Diagnostic{Code: "GCL005", Severity: diagnostics.Error,
			Symbol: p.PkgPath, Feature: "analysis limit",
			Message: "package " + p.PkgPath + " has Go errors; subset findings for it may be incomplete",
			Remedy:  "Fix the Go errors and run the check again."})
	}
	return ds
}

func languageVersion(fset *token.FileSet, p *packages.Package) []diagnostics.Diagnostic {
	var ds []diagnostics.Diagnostic
	tooNew := func(v string) bool {
		v = version.Lang(v)
		return v != "" && version.Compare(v, SourceLanguage) > 0
	}
	if p.Module != nil && p.Module.GoVersion != "" && tooNew("go"+p.Module.GoVersion) {
		ds = append(ds, diagnostics.Diagnostic{Code: "GCL004", Severity: diagnostics.Error,
			File: p.Module.GoMod, Symbol: p.PkgPath, Feature: "source language version",
			Message: fmt.Sprintf("module requires go %s, newer than the Goalchemy baseline %s", p.Module.GoVersion, SourceLanguage),
			Remedy:  "Set the module go directive to 1.25 or earlier."})
	}
	if p.TypesInfo != nil {
		for _, f := range p.Syntax {
			if v := p.TypesInfo.FileVersions[f]; v != "" && tooNew(v) {
				d := diagnostics.Span(fset, f.Package, token.NoPos)
				d.Code, d.Severity, d.Symbol = "GCL004", diagnostics.Error, p.PkgPath
				d.Feature = "source language version"
				d.Message = fmt.Sprintf("file selects language version %s, newer than %s", v, SourceLanguage)
				d.Remedy = "Remove the go version build constraint or lower it to go1.25."
				ds = append(ds, d)
			}
		}
	}
	return ds
}

func parsePosition(s string) (string, int, int) {
	parts := strings.Split(s, ":")
	if len(parts) >= 3 {
		line, err1 := strconv.Atoi(parts[len(parts)-2])
		col, err2 := strconv.Atoi(parts[len(parts)-1])
		if err1 == nil && err2 == nil {
			return strings.Join(parts[:len(parts)-2], ":"), line, col
		}
	}
	if len(parts) >= 2 {
		if line, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return strings.Join(parts[:len(parts)-1], ":"), line, 0
		}
	}
	return s, 0, 0
}
