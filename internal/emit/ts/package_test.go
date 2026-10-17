package ts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/ts"
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
			output, err := ts.Emit(&p, nil)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ts.Emit(&p, nil)
			if err != nil || !reflect.DeepEqual(output, again) {
				t.Fatalf("nonrepeatable emission: %v", err)
			}
			packageFiles := map[string]string{}
			combined := generatedSource(output)
			for _, file := range output.Files {
				if len(file.Path) > 64 {
					t.Fatalf("overlong path: %s", file.Path)
				}
				source := string(file.Source)
				if strings.Contains(source, "eval(") || strings.Contains(source, "new Function(") {
					t.Fatal("source modules must execute natively")
				}
				if file.Owner != "" {
					packageFiles[file.Owner] = file.Path
					if !strings.Contains(source, "export const $globals = {") {
						t.Fatalf("missing owned storage: %s", file.Path)
					}
					mapDir, _ := filepath.Abs(".")
					data, err := file.SourceMap.JSON(mapDir)
					if err != nil {
						t.Fatal(err)
					}
					var sm struct {
						Mappings string   `json:"mappings"`
						Sources  []string `json:"sources"`
					}
					if err = json.Unmarshal(data, &sm); err != nil {
						t.Fatal(err)
					}
					if sm.Mappings == "" || len(strings.Split(sm.Mappings, ";")) > len(strings.Split(source, "\n")) {
						t.Fatalf("invalid native mappings: %s", file.Path)
					}
					for _, original := range sm.Sources {
						if _, err := os.Stat(filepath.Join(mapDir, original)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if file.Path == "shared.ts" && strings.Contains(source, "./pkg_") {
					t.Fatal("canonical support must be a leaf")
				}
			}
			if len(packageFiles) != len(p.Packages) {
				t.Fatalf("missing source owners: %v", packageFiles)
			}
			for _, f := range p.Funcs {
				symbol := naming.New(&p, ts.NameRules(&p)).Symbol(f.Sym)
				declarations := 0
				for _, file := range output.Files {
					if strings.Contains(string(file.Source), "function "+symbol+"(") {
						declarations++
						if file.Owner != f.Pkg {
							t.Fatalf("function %s owner %s, want %s", f.Name, file.Owner, f.Pkg)
						}
					}
				}
				if declarations != 1 {
					t.Fatalf("function %s declarations %d", f.Name, declarations)
				}
			}
			for _, g := range p.Globals {
				for _, file := range output.Files {
					declaration := naming.New(&p, ts.NameRules(&p)).Symbol(g.Sym) + ": "
					if strings.Contains(string(file.Source), declaration) && file.Owner != g.Pkg {
						t.Fatalf("global %s wrong owner", g.Name)
					}
				}
			}
			for _, typ := range p.Types.All {
				if !typ.Boxed {
					continue
				}
				if count := strings.Count(combined, "$types."+naming.New(&p, ts.NameRules(&p)).Type(typ, "TD$")+" = "); count != 1 {
					t.Fatalf("descriptor %d assignments %d", typ.ID, count)
				}
			}
			var workerSource string
			for _, file := range output.Files {
				if strings.HasSuffix(file.Owner, "/worker") {
					workerSource = string(file.Source)
				}
			}
			if !strings.Contains(workerSource, "import * as $module_pkg_") || !strings.Contains(workerSource, ".$globals.") {
				t.Fatal("worker lacks imported live package references")
			}
			out := t.TempDir()
			if ds := driver.EmitWithOptions("typescript", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
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
				t.Fatalf("ownership manifest: %+v", manifest.SourcePackages)
			}
			inventory := map[string]bool{}
			for _, name := range manifest.GeneratedFiles {
				inventory[name] = true
			}
			for _, file := range output.Files {
				if !inventory[file.Path] || !inventory[file.Path+".map"] {
					t.Fatalf("missing source/map inventory: %s", file.Path)
				}
				var sm struct {
					File string `json:"file"`
				}
				data, err := os.ReadFile(filepath.Join(out, file.Path+".map"))
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(data, &sm); err != nil || sm.File != file.Path {
					t.Fatalf("native map path %s: %s (%v)", file.Path, sm.File, err)
				}
				if file.Owner != "" {
					checkOwnedDeclarationMap(t, &p, file.Source, file.Owner, data, out)
				}
			}
			actual, err := testutil.Runners["typescript"](out)
			if err != nil || actual.Exit != 0 || actual.Stdout != "" || actual.Stderr != "package output ok\n" {
				t.Fatalf("native package modules: %v\n%s", err, actual)
			}
		})
	}
	abs, _ := filepath.Abs(fixture)
	native, err := testutil.Native(abs, t.TempDir())
	if err != nil || native.Exit != 0 || native.Stderr != "package output ok\n" {
		t.Fatalf("source oracle: %v\n%s", err, native)
	}
}

func TestReEmissionChangesPackageGraphAndEntry(t *testing.T) {
	executable := packageFixture(t, "../../../tests/language/testdata/package_output")
	library := packageFixture(t, "../../../tests/integration/testdata/package_library/api")
	out := t.TempDir()
	if ds := driver.Emit("typescript", executable, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	data, err := os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var old link.Manifest
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"caller.ts", "rt/caller.ts"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte("export {};\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Failed public-boundary preflight must retain the complete old inventory.
	rejected := buildNamingFixture(t, "package probe\nfunc Bad() map[string]int { return nil }")
	if ds := driver.Emit("typescript", rejected, out); !diagnostics.HasErrors(ds) {
		t.Fatal("unbounded boundary accepted")
	}
	for _, name := range append(old.GeneratedFiles, old.RuntimeFiles...) {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("failed emission removed %s", name)
		}
	}
	if ds := driver.Emit("typescript", library, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	data, err = os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var current link.Manifest
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, name := range append(current.GeneratedFiles, current.RuntimeFiles...) {
		kept[name] = true
	}
	for _, name := range append(old.GeneratedFiles, old.RuntimeFiles...) {
		if !kept[name] {
			if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
				t.Fatalf("obsolete file retained: %s (%v)", name, err)
			}
		}
	}
	for _, name := range []string{"caller.ts", "rt/caller.ts"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("caller file removed: %s", name)
		}
	}
	// The global initializer's closure is a state-package function, not central.
	found := false
	for _, f := range library.IR.Funcs {
		if f.Closure && strings.HasPrefix(f.Name, "$init$") {
			found = true
			if !strings.HasSuffix(f.Pkg, "/state") {
				t.Fatalf("initializer closure owner: %s (%s)", f.Pkg, f.Name)
			}
		}
	}
	if !found {
		t.Fatal("fixture lacks initializer closure")
	}
}

// Decode the written v3 map independently of the emitter's marker machinery.
// The physical declaration line includes the module's actual import preamble.
func checkOwnedDeclarationMap(t *testing.T, p *ir.Program, source []byte, owner string, data []byte, dir string) {
	t.Helper()
	var sm struct {
		Sources  []string `json:"sources"`
		Mappings string   `json:"mappings"`
	}
	if err := json.Unmarshal(data, &sm); err != nil {
		t.Fatal(err)
	}
	mapped := map[int]token.Position{}
	sourceIndex, sourceLine, sourceColumn := 0, 0, 0
	for generatedLine, line := range strings.Split(sm.Mappings, ";") {
		if line == "" {
			continue
		}
		for _, segment := range strings.Split(line, ",") {
			values := decodeVLQ(t, segment)
			if len(values) == 1 {
				continue
			}
			if len(values) < 4 {
				t.Fatalf("invalid source mapping %q", segment)
			}
			sourceIndex += values[1]
			sourceLine += values[2]
			sourceColumn += values[3]
			if sourceIndex < 0 || sourceIndex >= len(sm.Sources) {
				t.Fatal("invalid source index")
			}
			mapped[generatedLine+1] = token.Position{Filename: filepath.Clean(filepath.Join(dir, sm.Sources[sourceIndex])), Line: sourceLine + 1, Column: sourceColumn + 1}
		}
	}
	for _, global := range p.Globals {
		if global.Pkg != owner {
			continue
		}
		declaration := naming.New(p, ts.NameRules(p)).Symbol(global.Sym) + ": "
		for index, line := range strings.Split(string(source), "\n") {
			if !strings.Contains(line, declaration) {
				continue
			}
			expected := p.Fset.Position(global.Pos)
			actual, ok := mapped[index+1]
			if !ok || actual.Filename != filepath.Clean(expected.Filename) || actual.Line != expected.Line || actual.Column != expected.Column {
				t.Fatalf("owned declaration %s:%d maps to %s, want %s", owner, index+1, actual, expected)
			}
			return
		}
		t.Fatalf("missing physical declaration %s", global.Name)
	}
	t.Fatalf("owner %s has no mapped declaration", owner)
}

func decodeVLQ(t *testing.T, segment string) []int {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var values []int
	value, shift := 0, 0
	for _, r := range segment {
		digit := strings.IndexRune(alphabet, r)
		if digit < 0 || shift > 60 {
			t.Fatalf("invalid VLQ %q", segment)
		}
		value |= (digit & 31) << shift
		if digit&32 != 0 {
			shift += 5
			continue
		}
		signed := value >> 1
		if value&1 != 0 {
			signed = -signed
		}
		values = append(values, signed)
		value, shift = 0, 0
	}
	if shift != 0 {
		t.Fatalf("unfinished VLQ %q", segment)
	}
	return values
}
