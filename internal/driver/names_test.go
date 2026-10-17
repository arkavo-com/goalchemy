package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNameCollisionsFail(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"go.mod":  "module example.com/names\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => " + root + "\n",
		"main.go": "package main\n\nfunc count(n int) int {\n\ttotal := n\n\tif n > 1 {\n\t\ttotal := 2\n\t\tn += total\n\t}\n\treturn total + n\n}\n\nfunc main() { println(count(3)) }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, ds := Build(context.Background(), Options{Dir: dir})
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	if ds := EmitWithOptions("go", res, t.TempDir(), EmitOptions{}); len(ds) != 0 {
		t.Fatalf("rename policy: %v", ds)
	}
	ds = EmitWithOptions("go", res, t.TempDir(), EmitOptions{NameCollisions: "fail"})
	if len(ds) != 1 {
		t.Fatalf("fail policy: %v", ds)
	}
	d := ds[0]
	if d.Code != "GCE009" || filepath.Base(d.File) != "main.go" || d.Line != 6 || !strings.Contains(d.Message, "total collides with total declared at ") ||
		!strings.Contains(d.Message, "main.go:4:2") || !strings.Contains(d.Message, "total_2") {
		t.Fatalf("diagnostic: %s", d)
	}
}
