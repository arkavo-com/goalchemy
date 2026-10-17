package frontend

import (
	"bytes"
	"go/format"
	"go/token"
	"strings"
	"testing"
)

func TestParseFutureFile(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		normalized   bool
	}{
		{"import", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ x := task.Await(f); _ = x }`, true},
		{"alias", `package main; import work "github.com/eugenioenko/goalchemy/lib/task"; func main(){ work.Await(f) }`, true},
		{"dot", `package main; import . "github.com/eugenioenko/goalchemy/lib/task"; func main(){ Await(f) }`, true},
		{"shadow", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ task := local; task.Await(f) }`, false},
		{"dot shadow", `package main; import . "github.com/eugenioenko/goalchemy/lib/task"; func main(){ Await := local; Await(f) }`, false},
		{"other package", `package main; import "other/task"; func main(){ task.Await(f) }`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := ParseFutureFile(fset, "main.go", []byte(tc.source), false)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			if err := format.Node(&b, fset, f); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(b.String(), "Await(f)()"); got != tc.normalized {
				t.Fatalf("normalized=%v: %s", got, b.String())
			}
		})
	}
}
