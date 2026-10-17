package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

func buildNames(t *testing.T, source string) *Result {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"go.mod":  "module example.com/names\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => " + root + "\n",
		"main.go": source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, ds := Build(context.Background(), Options{Dir: dir})
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	return res
}

func TestNameCollisionsFail(t *testing.T) {
	res := buildNames(t, "package main\n\nfunc count(n int) int {\n\ttotal := n\n\tif n > 1 {\n\t\ttotal := 2\n\t\tn += total\n\t}\n\treturn total + n\n}\n\nfunc main() { println(count(3)) }\n")
	if ds := EmitWithOptions("go", res, t.TempDir(), EmitOptions{}); len(ds) != 0 {
		t.Fatalf("rename policy: %v", ds)
	}
	ds := EmitWithOptions("go", res, t.TempDir(), EmitOptions{NameCollisions: "fail"})
	if len(ds) != 1 {
		t.Fatalf("fail policy: %v", ds)
	}
	d := ds[0]
	if d.Code != "GCE009" || filepath.Base(d.File) != "main.go" || d.Line != 6 || !strings.Contains(d.Message, "total collides with total declared at ") ||
		!strings.Contains(d.Message, "main.go:4:2") || !strings.Contains(d.Message, "total_2") {
		t.Fatalf("diagnostic: %s", d)
	}
}

// Every target declares a function's locals in one scope, so a shadowed
// local takes a suffix everywhere; keywords and reserved names rename only
// on the targets that reserve them.
func TestSourceNamesPerTarget(t *testing.T) {
	res := buildNames(t, `package main

var class = 1

func work(self, out, fn int) int {
	total := self
	{
		total := out
		fn += total
	}
	return total + fn
}

func main() { println(work(1, 2, 3), class) }
`)
	var work *ir.Func
	for _, f := range res.IR.Funcs {
		if f.Name == "main.work" {
			work = f
		}
	}
	for _, tc := range []struct {
		target string
		want   map[string]string
	}{
		{"go", map[string]string{"class": "class", "self": "self", "out": "out", "fn": "fn"}},
		{"typescript", map[string]string{"class": "class_2", "self": "self", "out": "out", "fn": "fn"}},
		{"python", map[string]string{"class": "class_2", "self": "self_2", "out": "out", "fn": "fn"}},
		{"java", map[string]string{"class": "class_2", "self": "self", "out": "out", "fn": "fn"}},
		{"csharp", map[string]string{"class": "class_2", "self": "self", "out": "out_2", "fn": "fn"}},
		{"rust", map[string]string{"class": "class", "self": "self_2", "out": "out", "fn": "fn_2"}},
		{"swift", map[string]string{"class": "class_2", "self": "self_2", "out": "out", "fn": "fn"}},
		{"c", map[string]string{"class": "class", "self": "self", "out": "out", "fn": "fn"}},
	} {
		t.Run(tc.target, func(t *testing.T) {
			program := *res.IR
			if scan, ok := unqualifiedRuntime[tc.target]; ok {
				declared, called, err := runtimeNames(res.Catalog.FS, tc.target, scan)
				if err != nil {
					t.Fatal(err)
				}
				program.ReservedNames, program.ReservedGlobals = declared, called
			}
			names := naming.New(&program, nameRules[tc.target](&program))
			got := map[string]string{"class": names.Symbol(res.IR.Globals[0].Sym)}
			var totals []string
			for _, l := range work.Locals {
				switch l.Name {
				case "self", "out", "fn":
					got[l.Name] = names.Local(l, "_")
				case "total":
					totals = append(totals, names.Local(l, "_"))
				}
			}
			for name, want := range tc.want {
				if got[name] != want {
					t.Errorf("%s = %q, want %q", name, got[name], want)
				}
			}
			if strings.Join(totals, " ") != "total total_2" {
				t.Errorf("shadowed locals = %v", totals)
			}
		})
	}
}
