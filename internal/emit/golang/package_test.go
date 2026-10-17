package golang_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/golang"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/naming"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestPackageOutput(t *testing.T) {
	fixture, err := filepath.Abs("../../../tests/language/testdata/package_output")
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	// The source graph itself, rather than encoded target names, establishes
	// owners and initialization dependency order.
	seen := map[string]bool{}
	for _, pkg := range res.IR.Packages {
		for _, dep := range pkg.Imports {
			if !seen[dep] {
				t.Fatalf("package %s precedes dependency %s", pkg.Path, dep)
			}
		}
		seen[pkg.Path] = true
	}
	if len(seen) != 5 || !res.IR.Packages[4].Root {
		t.Fatalf("unexpected source graph: %+v", res.IR.Packages)
	}
	promoted, closures := 0, 0
	for _, f := range res.IR.Funcs {
		if f.Wrapper && f.RecvType != nil && strings.Contains(f.Name, "worker.Envelope") {
			promoted++
			if !strings.HasSuffix(f.Pkg, "/worker") {
				t.Fatalf("promoted method owner: %+v", f)
			}
		}
		if f.Closure {
			closures++
			if !strings.HasSuffix(f.Pkg, "/worker") {
				t.Fatalf("closure owner: %s", f.Pkg)
			}
		}
	}
	if promoted == 0 || closures < 2 {
		t.Fatalf("fixture did not exercise wrappers/closures: %d/%d", promoted, closures)
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out := t.TempDir()
			p := *res.IR
			p.Names = testutil.Names(compact)
			emitted, err := golang.Emit(&p, out, nil)
			if err != nil {
				t.Fatal(err)
			}
			again, err := golang.Emit(&p, out, nil)
			if err != nil || !reflect.DeepEqual(emitted, again) {
				t.Fatalf("nonrepeatable emission: %v", err)
			}
			checkPackageDeclarations(t, &p, emitted)
			if ds := driver.EmitWithOptions("go", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			checkPackageManifest(t, out, &p, emitted)
			actual, err := testutil.Runners["go"](out)
			if err != nil || actual.Exit != 0 || actual.Stdout != "" || actual.Stderr != "package output ok\n" {
				t.Fatalf("native package files: %v\n%s", err, actual)
			}
		})
	}
	native, err := testutil.Native(fixture, t.TempDir())
	if err != nil || native.Exit != 0 || native.Stdout != "" || native.Stderr != "package output ok\n" {
		t.Fatalf("source oracle: %v\n%s", err, native)
	}
}

func checkPackageDeclarations(t *testing.T, p *ir.Program, out *golang.Output) {
	t.Helper()
	owners := map[string][]string{}
	packageFiles := map[string]string{}
	for _, file := range out.Files {
		if len(file.Path) > 64 {
			t.Fatalf("overlong source filename: %s", file.Path)
		}
		if file.Owner != "" {
			if packageFiles[file.Owner] != "" {
				t.Fatalf("duplicate owner file: %s", file.Owner)
			}
			packageFiles[file.Owner] = file.Path
			if len(file.Lines) == 0 {
				t.Fatalf("missing source map for %s", file.Path)
			}
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Source, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					owners[d.Name.Name] = append(owners[d.Name.Name], file.Owner)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						owners[s.Name.Name] = append(owners[s.Name.Name], file.Owner)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							owners[n.Name] = append(owners[n.Name], file.Owner)
						}
					}
				}
			}
		}
		physical := strings.Split(string(file.Source), "\n")
		for line, pos := range file.Lines {
			if line < 1 || line > len(physical) {
				t.Fatalf("invalid physical map %s:%d", file.Path, line)
			}
			original, err := os.ReadFile(pos.Filename)
			if err != nil || pos.Line < 1 || pos.Line > len(strings.Split(string(original), "\n")) {
				t.Fatalf("invalid source map %s:%d -> %s: %v", file.Path, line, pos, err)
			}
		}
	}
	if len(packageFiles) != len(p.Packages) {
		t.Fatalf("package files: %v", packageFiles)
	}
	assert := func(symbol, owner string) {
		t.Helper()
		if found := owners[symbol]; len(found) != 1 || found[0] != owner {
			t.Fatalf("declaration %s owners %v, want %q", symbol, found, owner)
		}
	}
	for _, f := range p.Funcs {
		if f.MethodID != "" && !f.Wrapper || f.Closure && !f.MaySuspend {
			continue
		}
		assert(naming.New(p, golang.NameRules(p)).Symbol(f.Sym), f.Pkg)
	}
	for _, g := range p.Globals {
		assert(naming.New(p, golang.NameRules(p)).Symbol(g.Sym), g.Pkg)
	}
	for _, typ := range p.Types.All {
		if typ.Kind == ir.KNamed && typ.Pkg != "" {
			assert(naming.New(p, golang.NameRules(p)).Type(typ, "type_"), typ.Pkg)
		}
	}
	var sameBase []string
	for owner, file := range packageFiles {
		if strings.HasSuffix(owner, "/value") {
			sameBase = append(sameBase, file)
		}
	}
	if len(sameBase) != 2 || sameBase[0] == sameBase[1] {
		t.Fatalf("same-basename package files: %v", sameBase)
	}
}

func checkPackageManifest(t *testing.T, dir string, p *ir.Program, out *golang.Output) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "goalchemy.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest link.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.SourcePackages, out.Packages) {
		t.Fatalf("manifest lost ownership: %+v", manifest.SourcePackages)
	}
	for i, pkg := range manifest.SourcePackages {
		if !reflect.DeepEqual(pkg.Package, p.Packages[i]) {
			t.Fatalf("manifest source graph: %+v", pkg)
		}
	}
	names := map[string]bool{"goalchemy.manifest.json": true}
	for _, name := range append(manifest.GeneratedFiles, manifest.RuntimeFiles...) {
		if names[name] {
			t.Fatalf("duplicate manifest member: %s", name)
		}
		names[name] = true
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			name, _ := filepath.Rel(dir, path)
			if !names[filepath.ToSlash(name)] {
				t.Errorf("unlisted generated file: %s", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range out.Files {
		if len(file.Lines) > 0 {
			data, err := os.ReadFile(filepath.Join(dir, file.Path+".lines"))
			if err != nil || len(data) == 0 {
				t.Fatalf("missing native-file sidecar %s: %v", file.Path, err)
			}
		}
	}
}
