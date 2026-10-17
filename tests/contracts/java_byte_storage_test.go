package contracts

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestJavaByteGrowthNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "byte_growth", Dir: "testdata/byte_growth"}, []string{"java"})
}

func TestJavaByteValuesNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"java"})
}

func TestJavaByteStorage(t *testing.T) {
	outDir := t.TempDir()
	if ds := testutil.CompileGateOptions("../language/testdata/byte_storage", "java", outDir, "sequential", driver.EmitOptions{Names: "readable"}); len(ds) > 0 {
		t.Fatal(ds)
	}
	src, err := testutil.GeneratedSource(outDir, ".java")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"new byte[4]", "byte[] _s", "byte[] _d", "Slice.BYTE_NIL", "() -> 0L, true)", ".appendBytes(", ".sliceToByteArray(", ".bget(", ".bset("} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted byte program lacks %q", want)
		}
	}
	testSrc, err := os.ReadFile(filepath.Join(root, "targets/java/tests/ByteStorageTest.java"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "ByteStorageTest.java"), testSrc, 0600); err != nil {
		t.Fatal(err)
	}
	var sources []string
	if err := filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".java") {
			sources = append(sources, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	env := driver.ToolEnv()
	javaBin := ""
	for _, item := range env {
		if strings.HasPrefix(item, "JAVA_HOME=") {
			javaBin = filepath.Join(strings.TrimPrefix(item, "JAVA_HOME="), "bin")
		}
	}
	javac, java := "javac", "java"
	if javaBin != "" {
		javac, java = filepath.Join(javaBin, javac), filepath.Join(javaBin, java)
	}
	cmd := exec.Command(javac, append([]string{"-d", outDir}, sources...)...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("javac: %v\n%s", err, out)
	}
	cmd = exec.Command(java, "-cp", outDir, "ByteStorageTest")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native byte storage: %v\n%s", err, out)
	}
}
