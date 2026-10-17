package contracts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestPythonByteGrowthNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "byte_growth", Dir: "testdata/byte_growth"}, []string{"python"})
}

func TestPythonByteValuesNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"python"})
}

func TestPythonByteStorage(t *testing.T) {
	outDir := t.TempDir()
	if ds := testutil.CompileGateOptions("../language/testdata/byte_storage", "python", outDir, "sequential", driver.EmitOptions{Names: "readable"}); len(ds) > 0 {
		t.Fatal(ds)
	}
	src, err := testutil.GeneratedSource(outDir, ".py")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rt.alloc_bytes(4)", "return bytearray(_s)", "memoryview(_d)[:] = _s", "return bytes(_x)", "rt.BYTE_NIL", "lambda: 0, True)", "bytearray((", "rt.slice_to_array("} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted byte program lacks %q", want)
		}
	}
	testSrc, err := os.ReadFile(filepath.Join(root, "targets/python/tests/byte_storage_test.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "byte_storage_test.py"), testSrc, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "byte_storage_test.py")
	cmd.Dir = outDir
	cmd.Env = driver.ToolEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native byte storage: %v\n%s", err, out)
	}
}
