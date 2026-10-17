package subset_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

func TestFutureDiagnostics(t *testing.T) {
	cat, ds := catalog.Load(catalog.FS())
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	reg := catalog.NewRegistry(cat)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, source, message string }{
		{"export", `package example; import "github.com/eugenioenko/goalchemy/lib/task"; func Pending() task.Future[func() int] { return task.Start(func() int{return 1}) }`, "cannot cross an exported library boundary"},
		{"handle", `package example; import "github.com/eugenioenko/goalchemy/lib/task"; type Handle struct { pending []task.Future[func() int] }`, "cannot cross an exported library boundary"},
		{"method", `package example; import "github.com/eugenioenko/goalchemy/lib/task"; type Handle struct {}; func (Handle) Pending() task.Future[func() int] { return task.Start(func() int{return 1}) }`, "cannot cross an exported library boundary"},
		{"internal", `package example; import "github.com/eugenioenko/goalchemy/lib/task"; func Value() int { f:=task.Start(func() int{return 1}); return task.Await(f) }`, ""},
		{"nonfunction", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ task.Start(7) }`, "task.Start function"},
		{"parameters", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ task.Start(func(int) int{return 1}) }`, "task.Start function"},
		{"defer", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ f:=task.Start(func() {}); defer task.Await(f) }`, "defer task.Await"},
		{"go", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ go task.Start(func(){}) }`, "go task.Start"},
		{"result type", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ f:=task.Start(func() int{return 1}); var x string = task.Await(f); _=x }`, "cannot use"},
		{"future signature", `package main; import "github.com/eugenioenko/goalchemy/lib/task"; func main(){ var f task.Future[int]; _=f }`, "future without a zero-argument result signature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mod := "module futuretest\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => " + root + "\n"
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(tc.source), 0644); err != nil {
				t.Fatal(err)
			}
			prog, diags := frontend.Load(context.Background(), frontend.Options{Dir: dir, External: reg.Package})
			if len(diags) == 0 {
				diags = append(diags, subset.Check(prog, subset.Options{Gate: subset.Cooperative, External: reg.External, ExternalPackage: reg.Package})...)
			}
			var messages []string
			for _, d := range diags {
				messages = append(messages, d.Message)
			}
			got := strings.Join(messages, "\n")
			if tc.message == "" {
				if len(diags) > 0 {
					t.Fatal(diags)
				}
			} else if !strings.Contains(got, tc.message) {
				t.Fatalf("wanted %q, got %v", tc.message, diags)
			}
		})
	}
}
