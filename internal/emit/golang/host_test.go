package golang_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

// Host files compiled into the generated package declare names Goalchemy
// cannot see; a project lists them so lowercased source names avoid them.
func TestHostFilesReserveNames(t *testing.T) {
	sources := map[string]string{
		"fixture.go": `package boundary
 type AttributesServiceClient struct { n int }
 func Log(s string) string { return s + "!" }
 func Call(s string) (string, int) { c := AttributesServiceClient{n: 2}; return Log(s), c.n }
 `,
	}
	hostDir := t.TempDir()
	host := "package generated\n\nimport \"log\"\n\ntype attributesServiceClient struct{}\n\nfunc init() { log.SetFlags(0) }\n"
	if err := os.WriteFile(filepath.Join(hostDir, "services.go"), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	res := buildNamingSources(t, sources)
	ds := driver.EmitWithOptions("go", res, t.TempDir(), driver.EmitOptions{NameCollisions: "fail"})
	if diagnostics.HasErrors(ds) {
		t.Fatalf("lowercasing alone is not reported: %v", ds)
	}
	ds = driver.EmitWithOptions("go", res, t.TempDir(), driver.EmitOptions{NameCollisions: "fail", Host: []string{hostDir}})
	if len(ds) != 2 {
		t.Fatalf("host collisions under fail = %v", ds)
	}
	for i, want := range []string{"AttributesServiceClient collides with attributesServiceClient declared at " + filepath.Join(hostDir, "services.go") + ":5:6 (host declaration); it would be emitted as attributesServiceClient_2",
		"Log collides with log declared at " + filepath.Join(hostDir, "services.go") + ":3:8 (host declaration); it would be emitted as log_2"} {
		if ds[i].Code != "GCE009" || ds[i].Message != want {
			t.Fatalf("diagnostic %d = %+v, want %s", i, ds[i], want)
		}
	}
	if ds[1].Line == 0 {
		t.Fatalf("function collision has no source position: %+v", ds[1])
	}
	dir := t.TempDir()
	if ds := driver.EmitWithOptions("go", res, dir, driver.EmitOptions{Host: []string{filepath.Join(hostDir, "services.go")}}); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	declared := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					declared[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if s, ok := spec.(*ast.TypeSpec); ok {
						declared[s.Name.Name] = true
					}
				}
			}
		}
	}
	for _, name := range []string{"attributesServiceClient_2", "log_2"} {
		if !declared[name] {
			t.Errorf("generated package does not declare %s", name)
		}
	}
	for _, name := range []string{"attributesServiceClient", "log"} {
		if declared[name] {
			t.Errorf("generated package declares host name %s", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "services.go"), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	consumer := `package main
 import ("context"; generated "goalchemyout")
 func main() {
  if s, n, err := generated.Call(context.Background(), "a"); s != "a!" || n != 2 || err != nil { panic("call") }
 }
 `
	cmdDir := filepath.Join(dir, "cmd", "check")
	if err := os.MkdirAll(cmdDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(consumer), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/check")
	cmd.Dir = dir
	cmd.Env = append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14")
	if result, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("consumer with host file: %v\n%s", err, result)
	}
}
