package driver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/testutil"
	"strings"
)

func TestTinyGoProfileIsolation(t *testing.T) {
	res, ds := driver.Build(context.Background(), driver.Options{Dir: "../../tests/language/testdata/calls_results"})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	out := t.TempDir()
	for _, target := range []string{"tinygo", "go"} {
		if ds := driver.Emit(target, res, out); len(ds) > 0 {
			t.Fatal(ds)
		}
		data, err := os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m link.Manifest
		if err := json.Unmarshal(data, &m); err != nil || m.Target != target {
			t.Fatalf("manifest: %s: %v", data, err)
		}
		_, guard := os.Stat(filepath.Join(out, "rt/tinygo_profile.go"))
		_, runner := os.Stat(filepath.Join(out, "run.sh"))
		if (guard == nil) != (target == "tinygo") || (runner == nil) != (target == "tinygo") {
			t.Fatalf("stale/missing TinyGo files for %s: %v, %v", target, guard, runner)
		}
	}
	if res.Catalog.Targets["tinygo"] != nil {
		t.Fatal("TinyGo emission mutated the shared catalog")
	}
	withHost := *res
	program := *res.IR
	program.Externals = map[string]*ir.Extern{"clock": {Contract: "lib.clock.unix"}}
	withHost.IR = &program
	if ds := driver.Emit("tinygo", &withHost, out); len(ds) != 1 || ds[0].Code != "GCE002" {
		t.Fatalf("host capability accepted: %v", ds)
	}
}

func TestTinyGoUnsupportedProfiles(t *testing.T) {
	for _, tc := range []struct{ name, source, code string }{
		{"function_box", "package main; func main() { var x any = func() {}; _ = x }", "GCE007"},
		{"recover", "package main; func main() { recover() }", "GCE007"},
		{"float", "package main; func main() { x := 1.5; println(x) }", "GCE007"},
		{"library", "package example; func Add(a, b int) int { return a+b }", "GCE006"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			for name, contents := range map[string]string{"go.mod": "module example\n\ngo 1.25\n", "main.go": tc.source} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ds := testutil.Compile(src, "tinygo", t.TempDir())
			if len(ds) != 1 || ds[0].Code != tc.code {
				t.Fatalf("wanted %s: %v", tc.code, ds)
			}
		})
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: "../../tests/language/testdata/calls_results"})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	if ds := driver.EmitWithOptions("tinygo", res, t.TempDir(), driver.EmitOptions{PauseModel: "native"}); len(ds) != 1 || ds[0].Code != "GCE008" {
		t.Fatal(ds)
	}
}

// Opt in: TinyGo's LLVM compilation is intentionally kept out of routine tests.
func TestTinyGoExecution(t *testing.T) {
	if os.Getenv("GOALCHEMY_TEST_TINYGO") != "1" {
		t.Skip("set GOALCHEMY_TEST_TINYGO=1 with TinyGo 0.43.0 installed")
	}
	fixtures, err := testutil.Discover("../../tests/language/testdata")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{}
	for _, name := range []string{"int_arith", "int_convert", "calls_results", "slices_alias", "slice_growth", "maps_basic", "map_iteration", "structs_values", "co_buffered", "co_pipeline", "co_select_default"} {
		wanted[name] = true
	}
	for _, f := range fixtures {
		if !wanted[f.Name] {
			continue
		}
		delete(wanted, f.Name)
		t.Run(f.Name, func(t *testing.T) { testutil.RunFixture(t, f, []string{"tinygo"}) })
	}
	if len(wanted) > 0 {
		t.Fatalf("missing fixtures: %v", wanted)
	}
	for _, tc := range []struct{ name, source string }{
		{"integers64", `package main
func kind(x any) string { switch x.(type) { case int: return "int"; case int64: return "int64"; case uint: return "uint"; case uint64: return "uint64" }; return "other" }
func main() {
 x := int(9223372036854775807); x++; println(x, x >> 63)
 u := uint(18446744073709551615); u++; println(u, ^u)
 println(kind(int(1)), kind(int64(1)), kind(uint(1)), kind(uint64(1)))
 var a any = int(1); var b any = int64(1); println(a == b)
 m := map[any]int{int(1): 3, int64(1): 5}; println(len(m), m[int(1)], m[int64(1)])
 s := []int{1, 2}; var length int = len(s); var n int = copy(s, []int{7}); println(length, n, s[0])
 for i, r := range "aé" { println(kind(i), i, r) }
}`},
		{"values", `package main
type Count int
type Flag bool
type Text string
type I interface { Value() int }
type V struct{}
func (V) Value() int { return 7 }
func main() {
 defer println("deferred", Count(-3))
 println(Count(-9), Flag(true), Text("hello"), uint64(18446744073709551615))
 print(Count(1), Text(":"), Count(2)); println(); println()
 var x any = V{}
 println(x.(I).Value())
}`},
		{"panic_nil", "package main; func fail() int { panic(nil) }; func main() { defer println(\"deferred\"); println(fail()) }"},
		{"assert_method", "package main; type I interface{ M() }; func main() { var x any = 1; _ = x.(I) }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			for name, data := range map[string]string{"go.mod": "module example\n\ngo 1.25\n", "main.go": tc.source} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			testutil.RunFixture(t, testutil.Fixture{Dir: src}, []string{"tinygo"})
		})
	}
}

func TestTinyGoWASISizeOverflow(t *testing.T) {
	if os.Getenv("GOALCHEMY_TEST_TINYGO") != "1" {
		t.Skip("set GOALCHEMY_TEST_TINYGO=1")
	}
	t.Setenv("GOALCHEMY_TINYGO_TARGET", "wasip1")
	for _, tc := range []struct{ name, body, message string }{
		{"slice", "n := int(1<<32 + 1); s := make([]byte, n); println(len(s))", "makeslice: len out of range"},
		{"channel", "n := uint(1<<32 + 1); c := make(chan byte, n); println(cap(c))", "makechan: size out of range"},
		{"index", "s := []byte{1, 2}; n := uint(1<<32 + 1); println(s[n])", "index out of range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, out := t.TempDir(), t.TempDir()
			for name, data := range map[string]string{"go.mod": "module example\n\ngo 1.25\n", "main.go": "package main\nfunc main() {" + tc.body + "}"} {
				if err := os.WriteFile(filepath.Join(src, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if ds := testutil.CompileGate(src, "tinygo", out, "cooperative"); len(ds) > 0 {
				t.Fatal(ds)
			}
			got, err := testutil.Runners["tinygo"](out)
			if err != nil || got.Exit == 0 || !strings.Contains(got.Stderr, tc.message) {
				t.Fatalf("size truncated or failure missing: %v\n%s", err, got)
			}
		})
	}
}
