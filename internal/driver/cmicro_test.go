package driver_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestMicroCProfileIsolation(t *testing.T) {
	res, ds := driver.Build(context.Background(), driver.Options{Dir: "../../tests/language/testdata/calls_results"})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	out := t.TempDir()
	for _, target := range []string{"c-micro", "c", "c-micro"} {
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
		_, micro := os.Stat(filepath.Join(out, "rt/micro/micro.c"))
		_, desktop := os.Stat(filepath.Join(out, "rt/types/sched.c"))
		_, idf := os.Stat(filepath.Join(out, "esp-idf/main/CMakeLists.txt"))
		if (micro == nil) != (target == "c-micro") || (idf == nil) != (target == "c-micro") || (desktop == nil) != (target == "c") {
			t.Fatal("stale/missing profile files")
		}
	}
	if res.Catalog.Targets["c-micro"] != nil {
		t.Fatal("profile mutated shared catalog")
	}
	copy := *res
	program := *res.IR
	copy.IR = &program
	program.Externals = map[string]*ir.Extern{"clock": {Contract: "lib.clock.unix"}}
	if ds := driver.Emit("c-micro", &copy, out); len(ds) != 1 || ds[0].Code != "GCE002" {
		t.Fatalf("host capability: %v", ds)
	}
	program.Externals = nil
	program.Cooperative = true
	if ds := driver.Emit("c-micro", &copy, out); len(ds) != 1 || ds[0].Code != "GCE007" {
		t.Fatalf("cooperative: %v", ds)
	}
	program.Cooperative = false
	types := *program.Types
	types.All = []*ir.Type{{Kind: ir.KFloat, FloatBits: 64}}
	program.Types = &types
	if ds := driver.Emit("c-micro", &copy, out); len(ds) != 1 || ds[0].Code != "GCE007" {
		t.Fatalf("floating-point: %v", ds)
	}
	program.Types = res.IR.Types
	program.Library = true
	if ds := driver.Emit("c-micro", &copy, out); len(ds) != 1 || ds[0].Code != "GCE006" {
		t.Fatalf("library: %v", ds)
	}
}

func TestMicroCExecution(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("C17 compiler required")
	}
	fixtures, err := testutil.Discover("../../tests/language/testdata")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{}
	for _, name := range []string{"int_arith", "int_convert", "calls_results", "slices_alias", "slice_growth", "maps_basic", "map_iteration", "structs_values", "strings_bytes", "interfaces", "closures", "defer_recover", "runtime_panics", "byte_storage"} {
		wanted[name] = true
	}
	for _, f := range fixtures {
		if wanted[f.Name] {
			delete(wanted, f.Name)
			t.Run(f.Name, func(t *testing.T) { testutil.RunFixture(t, f, []string{"c-micro"}) })
		}
	}
	if len(wanted) > 0 {
		t.Fatalf("missing fixtures: %v", wanted)
	}
}

func TestMicroCLifecycle(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("C17 compiler required")
	}
	source, out := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{"go.mod": "module example\n\ngo 1.25\n", "main.go": `package main
var count int
func init() { count++ }
func main() { b:=make([]byte,32); b[0]=7; println(count,b[0]) }
`} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if ds := testutil.Compile(source, "c-micro", out); len(ds) > 0 {
		t.Fatal(ds)
	}
	consumer, err := os.ReadFile("../../targets/c/tests/micro_lifecycle.c")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "rt/micro/host.c"), consumer, 0600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "run.sh")
	c.Dir = out
	c.Env = append(os.Environ(), "CFLAGS=-O1 -g -fsanitize=address,undefined -fno-sanitize-recover=all", "ASAN_OPTIONS=detect_leaks=0")
	if data, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
}
