package golang_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/golang"
	"github.com/eugenioenko/goalchemy/internal/naming"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

// Join source only for assertions; native builds use the individual files.
func emittedSource(out *golang.Output) string {
	var source strings.Builder
	for _, file := range out.Files {
		source.Write(file.Source)
	}
	return source.String()
}

func buildNamingFixture(t *testing.T, source string) *driver.Result {
	return buildNamingSources(t, map[string]string{"fixture.go": source})
}

func buildNamingSources(t *testing.T, sources map[string]string) *driver.Result {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	sources["go.mod"] = fmt.Sprintf("module namingfixture\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root)
	for name, data := range sources {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}

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
			out, err := golang.Emit(res.IR, t.TempDir(), nil)
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
			again, err := golang.Emit(res.IR, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if emittedSource(again) != emittedSource(out) {
				t.Fatal("repeated emission changed generated names")
			}
			source := emittedSource(out)
			for _, f := range res.IR.Funcs {
				if f.MethodID != "" && !f.Wrapper || f.Closure && !f.MaySuspend {
					continue
				}
				if !strings.Contains(source, naming.New(res.IR, golang.NameRules(res.IR)).Symbol(f.Sym)+"(") {
					t.Errorf("missing emitted function %s", f.Name)
				}
			}
			for _, g := range res.IR.Globals {
				if !strings.Contains(source, naming.New(res.IR, golang.NameRules(res.IR)).Symbol(g.Sym)) {
					t.Errorf("missing global %s", g.Name)
				}
			}
			if !compact {
				for _, name := range []string{"State", "calculate", "counter", "v_input"} {
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
			if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatalf("emit: %v", ds)
			}
			got, err := testutil.Runners["go"](dir)
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
		out, err := golang.Emit(res.IR, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		source := emittedSource(out)
		for _, name := range []string{"type State =", "func Export("} {
			if !strings.Contains(source, name) {
				t.Errorf("compact=%v public API missing %q", compact, name)
			}
		}
		var declarations []string
		for _, line := range strings.Split(source, "\n") {
			if strings.Contains(line, "func Export(") {
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

func TestLibraryDependencyAliasesStable(t *testing.T) {
	res := buildNamingSources(t, map[string]string{
		"dep/value.go": `package dep
 type Value struct { Count int }
 type Values []Value
 func (v Value) Amount() int { return v.Count }
 `,
		"fixture.go": `package boundary
 import "namingfixture/dep"
 type Envelope struct { Item dep.Value; Items []dep.Value }
 func Export(v dep.Value) (dep.Value,error) { v.Count=v.Amount()+1; return v,nil }
 func ExportList(v dep.Values) (dep.Values,error) { return v,nil }
 func Wrap(v Envelope) (Envelope,error) { return v,nil }
 `,
	})
	var valueAlias, listAlias string
	for _, typ := range res.IR.Types.All {
		if typ.Obj == "Value" {
			valueAlias = fmt.Sprintf("Source_%s_%d", naming.Identifier(typ.Name), typ.ID)
		}
		if typ.Obj == "Values" {
			listAlias = fmt.Sprintf("Source_%s_%d", naming.Identifier(typ.Name), typ.ID)
		}
	}
	if valueAlias == "" || listAlias == "" {
		t.Fatal("missing dependency named types")
	}
	var signatures []string
	for _, compact := range []bool{false, true} {
		res.IR.Names = testutil.Names(compact)
		out, err := golang.Emit(res.IR, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var declarations []string
		for _, line := range strings.Split(emittedSource(out), "\n") {
			if strings.HasPrefix(line, "func Export(") || strings.HasPrefix(line, "func ExportList(") || strings.HasPrefix(line, "func Wrap(") {
				declarations = append(declarations, line)
			}
		}
		signatures = append(signatures, strings.Join(declarations, "\n"))
		if !strings.Contains(signatures[len(signatures)-1], valueAlias) || !strings.Contains(signatures[len(signatures)-1], listAlias) {
			t.Fatalf("missing stable dependency aliases: %s", signatures[len(signatures)-1])
		}
		dir := t.TempDir()
		if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
			t.Fatal(ds)
		}
		consumer := fmt.Sprintf(`package main
 import ("context"; generated "goalchemyout")
 func main() {
  v:=generated.%s{Count:4}
  got,err:=generated.Export(context.Background(),v);if err!=nil || got.Count!=5 || got.Amount()!=5 { panic("dependency value") }
  list,err:=generated.ExportList(context.Background(),generated.%s{v});if err!=nil || list[0].Count!=4 { panic("dependency collection") }
  nested,err:=generated.Wrap(context.Background(),generated.Envelope{Item:v,Items:[]generated.%s{v}});if err!=nil || nested.Item.Count!=4 || nested.Items[0].Count!=4 { panic("nested dependency") }
 }
 `, valueAlias, listAlias, valueAlias)
		cmdDir := filepath.Join(dir, "cmd", "check")
		if err := os.MkdirAll(cmdDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(consumer), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/check")
		cmd.Dir = dir
		cmd.Env = append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14")
		result, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("compact=%v consumer: %v\n%s", compact, err, result)
		}
	}
	if signatures[0] != signatures[1] {
		t.Fatalf("dependency API depends on private names:\n%s\n%s", signatures[0], signatures[1])
	}
}

func TestPrivateMethodPackageIdentityCollision(t *testing.T) {
	packageSource := `package value
 type Value struct { Count int }
 func (v Value) amount() int {return v.Count}
 func Check(v any) bool { _,ok:=v.(interface{amount() int});return ok }
 `
	res := buildNamingSources(t, map[string]string{
		"a-b/value.go": packageSource, "a_b/value.go": packageSource,
		"fixture.go": `package main
 import (a "namingfixture/a-b"; b "namingfixture/a_b")
 func main() {println(a.Check(a.Value{}),a.Check(b.Value{}),b.Check(a.Value{}),b.Check(b.Value{}))}
 `})
	for _, compact := range []bool{false, true} {
		dir := t.TempDir()
		if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
			t.Fatal(ds)
		}
		got, err := testutil.Runners["go"](dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Exit != 0 || got.Stderr != "true false false true\n" {
			t.Fatalf("compact=%v: %s", compact, got)
		}
	}
}

func TestNativeEmbeddedFieldsAndMethods(t *testing.T) {
	res := buildNamingFixture(t, `package main
 type Base struct { Count int }
 func (b Base) Amount() int { return b.Count }
 type Outer struct { *Base }
 func main() {
  outer:=Outer{Base:&Base{Count:4}}
  outer.Base.Count++
  var reader interface{Amount() int}=outer
  println(outer.Base.Count,reader.Amount(),outer.Amount())
 }
 `)
	for _, compact := range []bool{false, true} {
		dir := t.TempDir()
		if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
			t.Fatal(ds)
		}
		got, err := testutil.Runners["go"](dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Exit != 0 || got.Stderr != "5 5 5\n" {
			t.Fatalf("compact=%v: %s", compact, got)
		}
	}
}

func TestLibraryInternalEmbeddingUsesPublicAlias(t *testing.T) {
	res := buildNamingFixture(t, `package boundary
 type Point struct { Count int }
 func(p Point) Amount() int {return p.Count}
 type Rect struct {Point}
 func Export(input int)(int,error) {
  holder:=Rect{Point:Point{Count:input}}
  holder.Point.Count++
  return holder.Point.Count+holder.Amount(),nil
 }
 `)
	for _, compact := range []bool{false, true} {
		dir := t.TempDir()
		if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
			t.Fatal(ds)
		}
		cmdDir := filepath.Join(dir, "cmd", "check")
		if err := os.MkdirAll(cmdDir, 0700); err != nil {
			t.Fatal(err)
		}
		consumer := `package main
 import("context"; generated "goalchemyout")
 func main(){got,err:=generated.Export(context.Background(),4);if err!=nil || got!=10 {panic("internal embedding")};if (generated.Point{Count:8}).Amount()!=8 {panic("public method")}}
 `
		if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(consumer), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/check")
		cmd.Dir = dir
		cmd.Env = append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14")
		result, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("compact=%v consumer: %v\n%s", compact, err, result)
		}
	}
}

// Native Go fields keep their source names. A private method alias must avoid
// those names on every receiver while interface dispatch uses the same alias.
func TestPrivateMethodAliasesAvoidNativeFields(t *testing.T) {
	seed := buildNamingFixture(t, `package main
 type State struct{}
 func(s State)hidden()int{return 1}
 func main(){println(State{}.hidden())}
 `)
	var privateID string
	for _, fn := range seed.IR.Funcs {
		if fn.MethodID != "" {
			privateID = fn.MethodID
			break
		}
	}
	if privateID == "" {
		t.Fatal("missing private source method")
	}
	readable := fmt.Sprintf("method_%s_0", naming.Identifier(privateID))
	source := fmt.Sprintf(`package main
 type State struct {m_0, m_0_, %s, %s_ int}
 func(s State)hidden()int{return s.m_0+s.m_0_+s.%s+s.%s_}
 func(s State)Amount()int{return s.hidden()}
 func main(){
  value:=State{m_0:1,m_0_:2,%s:3,%s_:4}
  var private interface{hidden()int}=value
  var public interface{Amount()int}=value
  println(value.hidden(),private.hidden(),public.Amount())
 }
 `, readable, readable, readable, readable, readable, readable)
	res := buildNamingFixture(t, source)
	fixtureDir := filepath.Dir(res.Program.Roots[0].GoFiles[0])
	native, err := testutil.Native(fixtureDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if native.Exit != 0 || native.Stdout != "" || native.Stderr != "10 10 10\n" {
		t.Fatalf("source Go proof: %s", native)
	}
	for _, compact := range []bool{false, true} {
		dir := t.TempDir()
		if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
			t.Fatal(ds)
		}
		got, err := testutil.Runners["go"](dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Exit != native.Exit || got.Stdout != native.Stdout || got.Stderr != native.Stderr {
			t.Fatalf("compact=%v differs from source Go: %s", compact, got)
		}
	}
}
