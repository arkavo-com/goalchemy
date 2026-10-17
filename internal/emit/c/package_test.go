package c_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/emit/c"
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
			output, err := c.Emit(&p, nil)
			if err != nil {
				t.Fatal(err)
			}
			again, err := c.Emit(&p, nil)
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
				if file.Path == "goalchemy_internal.h" && strings.Contains(source, "{") {
					t.Fatal("private header contains source bodies")
				}
				if regexp.MustCompile(`#include\s*[<"][^>"]*\.c[>"]`).MatchString(source) {
					t.Fatal("package holders must compile as native sources")
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
				declaration := regexp.MustCompile(`(?m)^gx_V ` + regexp.QuoteMeta(cFunc(&p, f.Sym)) + `\([^;\n]*\) \{`)
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
				declaration := "gx_V " + cGlobal(&p, global.Sym) + ";"
				for _, file := range output.Files {
					for _, line := range strings.Split(string(file.Source), "\n") {
						if strings.TrimSpace(line) == declaration {
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
				if typ.Boxed && typ.Kind != ir.KString {
					if count := strings.Count(combined, "const gx_TypeDesc "+naming.New(&p, c.NameRules(&p)).Type(typ, "td_")+" = "); count != 1 {
						t.Fatalf("descriptor %d bindings %d", typ.ID, count)
					}
				}
			}
			layout, err := artifact.NewLayout(&p)
			if err != nil {
				t.Fatal(err)
			}
			for _, typ := range p.Types.All {
				if typ.U().Kind != ir.KStruct && typ.U().Kind != ir.KArray {
					continue
				}
				declaration := "gx_V " + naming.New(&p, c.NameRules(&p)).Type(typ.U(), "z_") + "(void) {"
				count := 0
				for _, file := range output.Files {
					if strings.Contains(string(file.Source), declaration) {
						count++
						if file.Owner != layout.RepresentationOwner(typ) {
							t.Fatalf("helper %s wrong owner %s", declaration, file.Owner)
						}
					}
				}
				if count > 1 {
					t.Fatalf("duplicate helper %s", declaration)
				}
			}
			out := t.TempDir()
			if ds := driver.EmitWithOptions("c", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
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
			// callers keep unrelated C sources in the same directory.
			if err := os.WriteFile(filepath.Join(out, "Caller.c"), []byte("caller-owned source intentionally invalid for this build\n"), 0600); err != nil {
				t.Fatal(err)
			}
			actual, err := testutil.Runners["c"](out)
			if err != nil || actual.Exit != 0 || actual.Stdout != "" || actual.Stderr != "package output ok\n" {
				t.Fatalf("native package files: %v\n%s", err, actual)
			}
			// Compile each owned file independently. nm must expose its own
			// function definitions and leave foreign mutable storage unresolved
			// until the final native linker joins the translation units.
			for _, file := range output.Files {
				if file.Owner == "" {
					continue
				}
				object := filepath.Join(t.TempDir(), "package.o")
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				cmd := exec.CommandContext(ctx, "cc", "-std=c17", "-Irt/types", "-I"+filepath.Join(driver.ToolchainRoot(), "bdwgc", "include"), "-c", file.Path, "-o", object)
				cmd.Dir, cmd.Env = out, driver.ToolEnv()
				if data, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("independent source unit %s: %v\n%s", file.Path, err, data)
				}
				nm := exec.CommandContext(ctx, "nm", "-g", object)
				data, err := nm.CombinedOutput()
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range p.Funcs {
					if f.Pkg == file.Owner && !strings.Contains(string(data), " T "+cFunc(&p, f.Sym)+"\n") {
						t.Fatalf("%s lacks its native function %s", file.Path, f.Name)
					}
				}
				if strings.HasSuffix(file.Owner, "/worker") {
					for _, global := range p.Globals {
						if strings.HasSuffix(global.Pkg, "/first/value") && global.Name == "Counter" && !strings.Contains(string(data), " U "+cGlobal(&p, global.Sym)+"\n") {
							t.Fatal("foreign mutable storage was duplicated instead of linked")
						}
					}
				}
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
			declaration := "gx_V " + cGlobal(p, global.Sym) + ";"
			for index, line := range strings.Split(string(source), "\n") {
				if strings.TrimSpace(line) == declaration {
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

func cFunc(p *ir.Program, sym string) string {
	names := naming.New(p, c.NameRules(p))
	return names.Derived("f", names.Symbol(sym))
}

func cGlobal(p *ir.Program, sym string) string {
	names := naming.New(p, c.NameRules(p))
	return names.Derived("g", names.Symbol(sym))
}
