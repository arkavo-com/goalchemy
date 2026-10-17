package golang_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

// The generated package is one Go package for every source package, so only
// the library's public API may be exported: host adapter files added to it
// must not collide with the source packages' exported names.
func TestSourceNamesExportOnlyLibraryAPI(t *testing.T) {
	res := buildNamingSources(t, map[string]string{
		"inner/inner.go": `package inner
 import "github.com/eugenioenko/goalchemy/std/errors"
 var ErrMissing = errors.New("missing")
 var errMissing = 1
 const Limit = 3
 var UTC = "utc"
 type Type struct{ Name string }
 func (t Type) Error() string { return t.Name }
 func Check(s string) error {
  if s == "" { return ErrMissing }
  if len(s) > Limit+errMissing { return Type{UTC} }
  return nil
 }
 `,
		"fixture.go": `package boundary
 import "namingfixture/inner"
 type Point struct { X, Y int }
 type Store struct { n int }
 func NewStore() *Store { return &Store{} }
 func (s *Store) Add(p Point) int { s.n += p.X; return s.n }
 func Valid(s string) (bool, error) { if err := inner.Check(s); err != nil { return false, err }; return true, nil }
 `,
	})
	dir := t.TempDir()
	if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{}); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var exported []string
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					exported = append(exported, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							exported = append(exported, s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								exported = append(exported, n.Name)
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(exported)
	if got, want := strings.Join(exported, " "), "Callback Callbacks LibraryError NewStore Point Store Valid"; got != want {
		t.Fatalf("exported identifiers = %s, want %s", got, want)
	}
	adapter := "package generated\n\nvar ErrMissing, Limit, UTC = 1, 2, 3\n\ntype Type struct{}\n\nfunc Check() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "adapter.go"), []byte(adapter), 0600); err != nil {
		t.Fatal(err)
	}
	consumer := `package main
 import ("context"; generated "goalchemyout")
 func main() {
  ctx := context.Background()
  if ok, err := generated.Valid(ctx, "ab"); !ok || err != nil { panic("valid") }
  if _, err := generated.Valid(ctx, ""); err == nil || err.Error() != "missing" { panic("sentinel") }
  if _, err := generated.Valid(ctx, "abcdef"); err == nil || err.Error() != "utc" { panic("error method") }
  s, err := generated.NewStore(ctx)
  if err != nil { panic(err) }
  if n, err := s.Add(ctx, generated.Point{X: 2}); n != 2 || err != nil { panic("handle") }
  generated.Check()
  _ = generated.ErrMissing
 }
 `
	cmdDir := filepath.Join(dir, "cmd", "check")
	if err := os.MkdirAll(cmdDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(consumer), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/check")
	cmd.Dir = dir
	cmd.Env = append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14")
	if result, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("consumer with host adapter: %v\n%s", err, result)
	}
}
