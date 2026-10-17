package c_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/c"
	"github.com/eugenioenko/goalchemy/internal/naming"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func buildNamingFixture(t *testing.T, source string) *driver.Result {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"go.mod":     fmt.Sprintf("module namingfixture\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root),
		"fixture.go": source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: dir, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatalf("build: %v", ds)
	}
	return res
}

// Naming changes must cover frame locals, shadowing, globals, fields, closures,
// method values and interface dispatch without changing the program's output.
func TestNamingModes(t *testing.T) {
	res := buildNamingFixture(t, `package main
    type State struct { value, clone, _clone, café int; samples [2]int; _ int; _ int }
    var counter = 2
    func (s State) Amount() int { return s.value+s.clone+s._clone+s.café }
    func calculate(input int) int {
        class := State{value:input, clone:3, _clone:4, café:5}
        address := &class.value
        *address += counter
        fn := func() int { class := 1; return class }
        var reader interface{ Amount() int } = class
        bound := class.Amount
        queue := make(chan int, 1)
        queue <- reader.Amount()+bound()+fn()
        return <-queue
    }
    func main() { println(calculate(1)) }
    `)
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			res.IR.Names = testutil.Names(compact)
			boxed := make([]bool, len(res.IR.Types.All))
			symbols := make([]string, len(res.IR.Funcs))
			for i, typ := range res.IR.Types.All {
				boxed[i] = typ.Boxed
			}
			for i, fn := range res.IR.Funcs {
				symbols[i] = fn.Sym
			}
			out, err := c.Emit(res.IR, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i, typ := range res.IR.Types.All {
				if typ.Boxed != boxed[i] {
					t.Errorf("emission changed type %d boxing", i)
				}
			}
			for i, fn := range res.IR.Funcs {
				if fn.Sym != symbols[i] {
					t.Errorf("emission changed function %s identity", fn.Name)
				}
			}
			again, err := c.Emit(res.IR, nil)
			if err != nil {
				t.Fatal(err)
			}
			if generatedSource(again) != generatedSource(out) {
				t.Fatal("repeated emission changed generated names")
			}
			source := generatedSource(out)
			for _, f := range res.IR.Funcs {
				if !strings.Contains(source, naming.New(res.IR, c.NameRules(res.IR)).Symbol(f.Sym)+"(") {
					t.Errorf("missing emitted function %s", f.Name)
				}
			}
			for _, g := range res.IR.Globals {
				if !strings.Contains(source, naming.New(res.IR, c.NameRules(res.IR)).Symbol(g.Sym)) {
					t.Errorf("missing global %s", g.Name)
				}
			}
			if !compact {
				for _, name := range []string{"State", "calculate", "counter", "v_input", "array_2_int"} {
					if !strings.Contains(source, name) {
						t.Errorf("readable output missing %q", name)
					}
				}
			} else {
				for _, name := range []string{"fn_", "global_", "v_input"} {
					if strings.Contains(source, name) {
						t.Errorf("compact output retains %q", name)
					}
				}
			}
			dir := t.TempDir()
			if ds := driver.EmitWithOptions("c", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatalf("emit: %v", ds)
			}
			got, err := testutil.Runners["c"](dir)
			if err != nil {
				t.Fatal(err)
			}
			if got.Exit != 0 || got.Stdout != "" || got.Stderr != "31\n" {
				t.Fatalf("program output: %s", got)
			}
		})
	}
}

func TestLibraryPublicNamesStableAcrossNamingModes(t *testing.T) {
	res := buildNamingFixture(t, `package boundary
    type State struct { Clone int; Label string }
    var counter int
    func Export(value State) (State,error) { counter++; value.Clone+=counter; return value,nil }
    `)
	var facade []string
	for _, compact := range []bool{false, true} {
		res.IR.Names = testutil.Names(compact)
		out, err := c.Emit(res.IR, nil)
		if err != nil {
			t.Fatal(err)
		}
		source := generatedSource(out)
		for _, name := range []string{"\"Export\"", "\"Clone\"", "\"Label\""} {
			if !strings.Contains(source, name) {
				t.Errorf("compact=%v public API missing %q", compact, name)
			}
		}
		var declarations []string
		for _, line := range strings.Split(source, "\n") {
			if strings.Contains(line, "int goalchemy_invoke(") {
				declarations = append(declarations, strings.TrimSpace(line))
			}
		}
		if len(declarations) == 0 {
			t.Fatal("no public export declarations")
		}
		facade = append(facade, string(out.Header))
	}
	if facade[0] != facade[1] {
		t.Fatalf("public API depends on private names:\n%s\n%s", facade[0], facade[1])
	}
}

func generatedSource(output *c.Output) string {
	var source strings.Builder
	for _, file := range output.Files {
		source.Write(file.Source)
	}
	return source.String()
}
