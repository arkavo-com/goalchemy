package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstalledCompilerOutsideRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs an external module")
	}
	for _, tool := range []string{"node", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable", tool)
		}
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	dir := t.TempDir()
	module := fmt.Sprintf("module example.com/goalchemy-external-test\n\ngo 1.25\n\nrequire github.com/eugenioenko/goalchemy v0.6.5\n\nreplace github.com/eugenioenko/goalchemy => %s\n", root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `package main

import "github.com/eugenioenko/goalchemy/lib/sync"

func main() {
	var mu sync.Mutex
	_ = mu
	println("external-ok")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := filepath.Join(dir, "goalchemy")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/goalchemy")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build compiler: %v\n%s", err, out)
	}
	for _, target := range []string{"typescript", "python"} {
		cmd := exec.CommandContext(ctx, binary, "run", "-target", target, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOALCHEMY_ROOT=")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "external-ok") {
			t.Fatalf("%s from external module: %v\n%s", target, err, out)
		}
	}
}
