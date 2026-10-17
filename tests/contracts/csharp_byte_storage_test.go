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

func TestCSharpByteGrowthNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "byte_growth", Dir: "testdata/byte_growth"}, []string{"csharp"})
}

func TestCSharpByteValuesNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"csharp"})
}

func TestCSharpByteStorage(t *testing.T) {
	outDir := t.TempDir()
	if ds := testutil.CompileGateOptions("../language/testdata/byte_storage", "csharp", outDir, "sequential", driver.EmitOptions{Names: "readable"}); len(ds) > 0 {
		t.Fatal(ds)
	}
	src, err := testutil.GeneratedSource(outDir, ".cs")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"new byte[4]", "byte[] _s", "byte[] _d", "Slice.BYTE_NIL", "() => 0L, true)", ".appendBytes(", ".sliceToByteArray(", ".bget(", ".bset("} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted byte program lacks %q", want)
		}
	}
	testSrc, err := os.ReadFile(filepath.Join(root, "targets/csharp/tests/ByteStorageTest.cs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "ByteStorageTest.cs"), testSrc, 0600); err != nil {
		t.Fatal(err)
	}
	run, err := os.ReadFile(filepath.Join(outDir, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	runText := strings.Replace(string(run), "-optimize+", "-main:ByteStorageTest -optimize+", 1)
	runText = strings.Replace(runText, "-out:bin/main.dll $refs", "-out:bin/main.dll $refs ByteStorageTest.cs", 1)
	if err := os.WriteFile(filepath.Join(outDir, "run.sh"), []byte(runText), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "run.sh")
	cmd.Dir = outDir
	cmd.Env = driver.ToolEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native byte storage: %v\n%s", err, out)
	}
}
