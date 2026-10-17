package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestNamingFlagsAndConfigOverride(t *testing.T) {
	dir := t.TempDir()
	for file, data := range map[string]string{
		"go.mod":         "module example.com/naming\n\ngo 1.25\n",
		"main.go":        "package main\ntype PolicyBinding struct { Count int }\nfunc CountBinding(binding PolicyBinding) int { return binding.Count }\nfunc main() { println(CountBinding(PolicyBinding{Count: 1})) }\n",
		"goalchemy.yaml": "schema_version: 1\npackages: [.]\ncompact_names: true\ntargets:\n  go: {out: output}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const target = "go"
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"compile default", []string{"compile", "-C", dir, "-target", target, "-out", filepath.Join(dir, "output"), "."}, "source"},
		{"compile compact", []string{"compile", "-C", dir, "-target", target, "-out", filepath.Join(dir, "output"), "--compact-names", "."}, "compact"},
		{"compile readable", []string{"compile", "-C", dir, "-target", target, "-out", filepath.Join(dir, "output"), "--names=readable", "."}, "readable"},
		{"run default", []string{"run", "-C", dir, "-target", target, "--keep", "."}, "source"},
		{"run compact", []string{"run", "-C", dir, "-target", target, "--keep", "--names=compact", "."}, "compact"},
		{"build config", []string{"build", "-config", filepath.Join(dir, "goalchemy.yaml")}, "compact"},
		{"build default override", []string{"build", "-config", filepath.Join(dir, "goalchemy.yaml"), "--compact-names=false"}, "source"},
		{"build readable override", []string{"build", "-config", filepath.Join(dir, "goalchemy.yaml"), "--names=readable"}, "readable"},
		{"build compact override", []string{"build", "-config", filepath.Join(dir, "goalchemy.yaml"), "--compact-names"}, "compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), tc.args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, &stderr)
			}
			out := filepath.Join(dir, "output")
			if tc.args[0] == "run" {
				for _, line := range strings.Split(stderr.String(), "\n") {
					if strings.HasPrefix(line, "generated: ") {
						out = strings.TrimPrefix(line, "generated: ")
						break
					}
				}
				if out == filepath.Join(dir, "output") {
					t.Fatal("run did not report its generated directory")
				}
				defer os.RemoveAll(out)
				if !strings.Contains(stdout.String(), "1\n") && !strings.Contains(stderr.String(), "\n1\n") {
					t.Fatalf("run output: %s%s", &stdout, &stderr)
				}
			}
			source, err := testutil.GeneratedSource(out, ".go")
			if err != nil {
				t.Fatal(err)
			}
			mode := "compact"
			if strings.Contains(string(source), "fn_main_main_") {
				mode = "readable"
			} else if strings.Contains(string(source), "func countBinding(binding policyBinding) int {") {
				mode = "source"
			}
			if mode != tc.want {
				t.Fatalf("generated %s names, want %s\n%s", mode, tc.want, source)
			}
		})
	}
}

func TestNamingFlagConflicts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"compile", "-target", "go", "-out", t.TempDir(), "--compact-names", "--names=source", "."}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "conflicts") {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
}
