package java_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/java"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/naming"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func packageFixture(t *testing.T, relative string) *driver.Result {
	t.Helper()
	fixture, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	return res
}

func TestPackageOutput(t *testing.T) {
	fixture := "../../../tests/language/testdata/package_output"
	res := packageFixture(t, fixture)
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			p := *res.IR
			p.Names = testutil.Names(compact)
			output, err := java.Emit(&p, nil)
			if err != nil {
				t.Fatal(err)
			}
			again, err := java.Emit(&p, nil)
			if err != nil || !reflect.DeepEqual(output, again) {
				t.Fatalf("nonrepeatable emission: %v", err)
			}
			owned := map[string]string{}
			combined := generatedSource(output)
			for _, file := range output.Files {
				if len(file.Path) > 64 {
					t.Fatalf("overlong source path: %s", file.Path)
				}
				source := string(file.Source)
				if strings.Contains(source, "exec(") || strings.Contains(source, "eval(") {
					t.Fatal("package holders must compile as native sources")
				}
				if file.Path == "_GoalchemySupport.java" && strings.Contains(source, "pkg_") {
					t.Fatal("canonical support must remain a leaf")
				}
				if file.Owner != "" {
					if owned[file.Owner] != "" {
						t.Fatalf("duplicate source owner %s", file.Owner)
					}
					owned[file.Owner] = file.Path
					if len(file.Lines) == 0 {
						t.Fatalf("missing native positions: %s", file.Path)
					}
					for line, pos := range file.Lines {
						if line < 1 || line > len(strings.Split(source, "\n")) {
							t.Fatalf("invalid generated line %s:%d", file.Path, line)
						}
						if _, err := os.Stat(pos.Filename); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if len(owned) != len(p.Packages) {
				t.Fatalf("missing source owners: %v", owned)
			}
			for _, f := range p.Funcs {
				count := 0
				declaration := regexp.MustCompile(`(?m)^\s+static [^\n(]+ ` + regexp.QuoteMeta(naming.New(&p, java.NameRules(&p)).Symbol(f.Sym)) + `\(`)
				for _, file := range output.Files {
					if declaration.Match(file.Source) {
						count++
						if file.Owner != f.Pkg {
							t.Fatalf("function %s owner %s, want %s", f.Name, file.Owner, f.Pkg)
						}
					}
				}
				if count != 1 {
					t.Fatalf("function %s declarations %d", f.Name, count)
				}
			}
			for _, global := range p.Globals {
				count := 0
				declaration := naming.New(&p, java.NameRules(&p)).Symbol(global.Sym) + " = "
				for _, file := range output.Files {
					for _, line := range strings.Split(string(file.Source), "\n") {
						if strings.Contains(line, declaration) && strings.HasPrefix(strings.TrimSpace(line), "static ") {
							count++
							if file.Owner != global.Pkg {
								t.Fatalf("global %s wrong owner", global.Name)
							}
						}
					}
				}
				if count != 1 {
					t.Fatalf("global %s declarations %d", global.Name, count)
				}
			}
			for _, typ := range p.Types.All {
				if typ.Boxed {
					if count := strings.Count(combined, naming.New(&p, java.NameRules(&p)).Type(typ, "TD$")+" = "); count != 1 {
						t.Fatalf("descriptor %d bindings %d", typ.ID, count)
					}
				}
			}
			var worker string
			for _, file := range output.Files {
				if strings.HasSuffix(file.Owner, "/worker") {
					worker = string(file.Source)
				}
			}
			for _, global := range p.Globals {
				if strings.HasSuffix(global.Pkg, "/first/value") && global.Name == "Counter" {
					reference := strings.TrimSuffix(owned[global.Pkg], ".java") + "." + naming.New(&p, java.NameRules(&p)).Symbol(global.Sym)
					if !strings.Contains(worker, reference) {
						t.Fatal("worker lacks qualified live package storage")
					}
				}
			}
			out := t.TempDir()
			if ds := driver.EmitWithOptions("java", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			data, err := os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest link.Manifest
			if err = json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(manifest.SourcePackages, output.Packages) {
				t.Fatalf("source ownership manifest: %+v", manifest.SourcePackages)
			}
			inventory := map[string]bool{}
			for _, name := range manifest.GeneratedFiles {
				inventory[name] = true
			}
			for _, file := range output.Files {
				if !inventory[file.Path] {
					t.Fatalf("missing source inventory: %s", file.Path)
				}
				if len(file.Lines) > 0 && !inventory[file.Path+".lines"] {
					t.Fatalf("missing map inventory: %s", file.Path)
				}
				if file.Owner != "" {
					checkDeclarationMap(t, &p, file.Source, file.Owner, out, file.Path)
				}
			}
			// Native build scripts must use the emitted inventory even when
			// callers keep unrelated Java sources in the same directory.
			if err := os.WriteFile(filepath.Join(out, "Caller.java"), []byte("caller-owned source intentionally invalid for this build\n"), 0600); err != nil {
				t.Fatal(err)
			}
			actual, err := testutil.Runners["java"](out)
			if err != nil || actual.Exit != 0 || actual.Stdout != "" || actual.Stderr != "package output ok\n" {
				t.Fatalf("native package files: %v\n%s", err, actual)
			}
		})
	}
	abs, _ := filepath.Abs(fixture)
	native, err := testutil.Native(abs, t.TempDir())
	if err != nil || native.Exit != 0 || native.Stderr != "package output ok\n" {
		t.Fatalf("native source oracle: %v\n%s", err, native)
	}
}

// Parse the written sidecar independently and compare the actual physical
// declaration line, including all native module import lines, to source.
func checkDeclarationMap(t *testing.T, p *ir.Program, source []byte, owner, out, file string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, file+".lines"))
	if err != nil {
		t.Fatal(err)
	}
	positions := map[int]token.Position{}
	for _, entry := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.SplitN(entry, "\t", 2)
		if len(fields) != 2 {
			t.Fatalf("invalid sidecar entry %q", entry)
		}
		generated, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatal(err)
		}
		last := strings.LastIndex(fields[1], ":")
		prior := strings.LastIndex(fields[1][:last], ":")
		line, err := strconv.Atoi(fields[1][prior+1 : last])
		if err != nil {
			t.Fatal(err)
		}
		column, err := strconv.Atoi(fields[1][last+1:])
		if err != nil {
			t.Fatal(err)
		}
		positions[generated] = token.Position{Filename: filepath.Clean(filepath.Join(out, fields[1][:prior])), Line: line, Column: column}
	}
	for _, global := range p.Globals {
		if global.Pkg == owner {
			declaration := naming.New(p, java.NameRules(p)).Symbol(global.Sym) + " = "
			for index, line := range strings.Split(string(source), "\n") {
				if strings.Contains(line, declaration) && strings.HasPrefix(strings.TrimSpace(line), "static ") {
					expected := p.Fset.Position(global.Pos)
					actual, ok := positions[index+1]
					if !ok || actual.Filename != filepath.Clean(expected.Filename) || actual.Line != expected.Line || actual.Column != expected.Column {
						t.Fatalf("declaration %s:%d maps to %s, want %s", file, index+1, actual, expected)
					}
					return
				}
			}
			t.Fatalf("missing physical global %s", global.Name)
		}
	}
	t.Fatalf("owner %s lacks mapped declaration", owner)
}
