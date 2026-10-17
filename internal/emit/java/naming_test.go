package java_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/java"
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
			out, err := java.Emit(res.IR, nil)
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
			source := generatedSource(out)
			for _, f := range res.IR.Funcs {
				if !strings.Contains(source, naming.New(res.IR, java.NameRules(res.IR)).Symbol(f.Sym)+"(") {
					t.Errorf("missing emitted function %s", f.Name)
				}
			}
			for _, g := range res.IR.Globals {
				if !strings.Contains(source, naming.New(res.IR, java.NameRules(res.IR)).Symbol(g.Sym)) {
					t.Errorf("missing global %s", g.Name)
				}
			}
			if !compact {
				for _, name := range []string{"State", "array_2_int", "calculate", "counter", "v_input", "f$value", "f$clone", "f$_clone", "f$caf_u"} {
					if !strings.Contains(source, name) {
						t.Errorf("readable output missing %q", name)
					}
				}
			} else {
				for _, name := range []string{"fn_", "global_", "v_input", "f$value"} {
					if strings.Contains(source, name) {
						t.Errorf("compact output retains %q", name)
					}
				}
			}
			dir := t.TempDir()
			if ds := driver.EmitWithOptions("java", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatalf("emit: %v", ds)
			}
			got, err := testutil.Runners["java"](dir)
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
		out, err := java.Emit(res.IR, nil)
		if err != nil {
			t.Fatal(err)
		}
		source := generatedSource(out)
		for _, name := range []string{"public static final class State", "public long Clone", "public String Label", "Export("} {
			if !strings.Contains(source, name) {
				t.Errorf("compact=%v public API missing %q", compact, name)
			}
		}
		var declarations []string
		for _, line := range strings.Split(source, "\n") {
			if strings.Contains(line, "public static Library.Operation<") {
				declarations = append(declarations, strings.TrimSpace(line))
			}
		}
		if len(declarations) == 0 {
			t.Fatal("no public export declarations")
		}
		facade = append(facade, strings.Join(declarations, "\n"))
	}
	if facade[0] != facade[1] {
		t.Fatalf("public API depends on private names:\n%s\n%s", facade[0], facade[1])
	}
}

// A public source type can match the fallback name of an anonymous boundary
// struct. Construct that collision from the lowered anonymous type's ID.
func TestLibraryAnonymousPublicNameCollision(t *testing.T) {
	res := buildNamingFixture(t, `package boundary
    type Named struct { Label string }
    func Export(value struct { Clone int }) (Named,error) { return Named{Label:"result"},nil }
    `)
	publicName := fmt.Sprintf("Value%d", res.IR.Exports[0].Sig.Params[0].U().ID)
	for _, typ := range res.IR.Types.All {
		if typ.Obj == "Named" {
			typ.Obj = publicName
			typ.Name = "boundary." + publicName
		}
	}
	for _, compact := range []bool{false, true} {
		res.IR.Names = testutil.Names(compact)
		out, err := java.Emit(res.IR, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{publicName, publicName + "_"} {
			if !strings.Contains(generatedSource(out), "class "+name+" {") {
				t.Errorf("compact=%v missing distinct public class %s", compact, name)
			}
		}
	}
}

func generatedSource(output *java.Output) string {
	var source strings.Builder
	for _, file := range output.Files {
		source.Write(file.Source)
	}
	return source.String()
}
