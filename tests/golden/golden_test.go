// Package golden checks the generated source of a small program on every
// target against reviewed copies, so changes to the shape of generated code
// show up in review.
package golden

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var variants = []struct {
	name, target, ext, pause string
}{
	{"go-native", "go", ".go", "native"},
	{"go-frames", "go", ".go", "frames"},
	{"typescript", "typescript", ".ts", "frames"},
	{"typescript-native", "typescript", ".ts", "native"},
	{"python", "python", ".py", "frames"},
	{"python-native", "python", ".py", "native"},
	{"java", "java", ".java", ""},
	{"java-native", "java", ".java", "native"},
	{"csharp", "csharp", ".cs", "frames"},
	{"csharp-native", "csharp", ".cs", "native"},
	{"rust", "rust", ".rs", ""},
	{"rust-native", "rust", ".rs", "native"},
	{"c", "c", ".c", ""},
	{"swift", "swift", ".swift", "frames"},
	{"swift-native", "swift", ".swift", "native"},
}

func TestGenerated(t *testing.T) {
	const fixture = "../language/testdata/co_fetch_tasks"
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			out := t.TempDir()
			opts := driver.EmitOptions{PauseModel: v.pause, NoLineDirectives: true}
			if ds := testutil.CompileGateOptions(fixture, v.target, out, "cooperative", opts); len(ds) > 0 {
				t.Fatalf("compile: %v", ds)
			}
			got := source(t, out, v.ext)
			for _, marker := range []string{"/*line ", "sourceMappingURL"} {
				if strings.Contains(got, marker) {
					t.Errorf("output contains %q with line directives off", marker)
				}
			}
			path := filepath.Join("testdata", v.name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./tests/golden -update)", err)
			}
			if got != string(want) {
				t.Errorf("generated %s differs from %s; review and run go test ./tests/golden -update\n%s", v.name, path, diff(string(want), got))
			}
		})
	}
}

// source concatenates the generated files of the program's own package.
func source(t *testing.T, out, ext string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".lines") || strings.HasSuffix(path, ".map") {
			t.Errorf("line table %s written with line directives off", filepath.Base(path))
		}
		if info.IsDir() || !strings.HasPrefix(info.Name(), "pkg_") || filepath.Ext(path) != ext {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.WriteString("// " + info.Name() + "\n")
		b.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "first difference at line " + strconv.Itoa(i+1) + ":\n- " + a + "\n+ " + b
		}
	}
	return ""
}
