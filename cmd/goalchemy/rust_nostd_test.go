package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustNoStdCommands(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":         "module nostdtest\n\ngo 1.25\n",
		"main.go":        "package main\nfunc main(){println(42)}\n",
		"goalchemy.yaml": "schema_version: 1\npackages: [.]\ntargets:\n  rust-nostd: {out: output}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"compile", "-target", "rust-nostd", "-C", dir, "-out", filepath.Join(dir, "output"), "."},
		{"build", "-config", filepath.Join(dir, "goalchemy.yaml")},
		{"run", "-target", "rust-nostd", "-C", dir, "."},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, &stderr)
		}
		if args[0] == "run" && stderr.String() != "42\n" {
			t.Fatalf("run: %s", &stderr)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "output/src/lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "#![no_std]") {
		t.Fatal("missing no_std attribute")
	}
}
