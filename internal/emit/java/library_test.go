package java_test

import (
	"fmt"
	"github.com/eugenioenko/goalchemy/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

// Unsupported boundaries produce compiler diagnostics before source/JAR emission.
func TestLibraryBoundaryDiagnostics(t *testing.T) {
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	for name, source := range map[string]string{
		"map":             "func Export()(map[string]string,error){return nil,nil}",
		"pointer":         "func Export(v *int)(int,error){return 0,nil}",
		"function":        "func Export(v func())(int,error){return 0,nil}",
		"private-field":   "type Value struct{hidden []byte};func Export(v Value)(int,error){return 0,nil}",
		"variadic":        "func Export(v ...int)(int,error){return 0,nil}",
		"descriptor-name": "type TypeDesc struct{Data []byte};func Export(v TypeDesc)(TypeDesc,error){return v,nil}",
		"runtime-name":    "type Library struct{Data []byte};func Export(v Library)(Library,error){return v,nil}",
		"outer-name":      "type Generated struct{Data []byte};func Export(v Generated)(Generated,error){return v,nil}",
		"native-handle":   "import \"github.com/eugenioenko/goalchemy/lib/crypto\";func Export(k *crypto.Key)(int,error){return 0,nil}",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for n, data := range map[string]string{"go.mod": fmt.Sprintf("module boundary\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root), "library.go": "package boundary\n" + source + "\n"} {
				if e := os.WriteFile(filepath.Join(dir, n), []byte(data), 0600); e != nil {
					t.Fatal(e)
				}
			}
			ds := testutil.CompileGate(dir, "java", t.TempDir(), "cooperative")
			found := false
			for _, d := range ds {
				if d.Code == "GCE007" {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected honest boundary diagnostic: %v", ds)
			}
		})
	}
}
