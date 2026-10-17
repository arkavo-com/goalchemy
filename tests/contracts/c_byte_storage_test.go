package contracts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestCByteGrowthNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "byte_growth", Dir: "testdata/byte_growth"}, []string{"c"})
}
func TestCByteValuesNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"c"})
}
func TestCByteStorage(t *testing.T) {
	outDir := t.TempDir()
	if ds := testutil.CompileGateOptions("../language/testdata/byte_storage", "c", outDir, "sequential", driver.EmitOptions{Names: "readable"}); len(ds) > 0 {
		t.Fatal(ds)
	}
	src, err := testutil.GeneratedSource(outDir, ".c")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gx_byte_array(4)", "gx_byte_array_clone(_x, 4)", "gx_byte_array_set(_d, _s, 4)", "gx_byte_array_eq(_a, _b, 4)", "gx_byte_array_key(_a, 4, _out)", "gx_nil_byte_slice()", "gx_make_byte_slice(", "gx_append_bytes(", "(const uint8_t[]){", "(uint8_t)gx_i(", "gx_make_slice("} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted byte program lacks %q", want)
		}
	}
	match := regexp.MustCompile(`gx_V z_([A-Za-z0-9_]+)\(void\) \{ return gx_byte_array\(4\); \}`).FindSubmatch(src)
	if match == nil {
		t.Fatal("missing emitted byte zero helper")
	}
	id := string(match[1])
	check := fmt.Sprintf(`
#include <assert.h>
int main(void) {
    GC_INIT();
    gx_V a = z_%[1]s();
    assert(a.pad == 1 && sizeof(*gx_bytes(a)) == 1 && GC_base(a.u.p));
    assert(GC_size(GC_base(a.u.p)) >= 4 && GC_size(GC_base(a.u.p)) < 4*sizeof(gx_V));
    gx_V alias = gx_slice_array(a, 4, gx_nil(), gx_nil(), gx_nil(), false);
    assert(alias.pad == 1 && alias.u.p == a.u.p && alias.l == 4);
    for (size_t i = 0; i < 4; i++) assert(gx_bytes(a)[i] == 0);
    gx_aset(a, gx_int(0), 4, gx_int(255));
    gx_V b = c_%[1]s(a);
    assert(b.u.p != a.u.p && b.pad == 1 && eq_%[1]s(a,b));
    assert(GC_size(GC_base(b.u.p)) >= 4 && GC_size(GC_base(b.u.p)) < 4*sizeof(gx_V));
    gx_Buf key = {0}, expected = {0};
    k_%[1]s(a,&key);
    gx_key_open(&expected);
    gx_vkey(gx_int(255),&expected);
    for (int i = 0; i < 3; i++) gx_vkey(gx_int(0),&expected);
    gx_key_close(&expected);
    assert(key.n == expected.n && !memcmp(key.b,expected.b,key.n));
    gx_aset(b,gx_int(0),4,gx_int(128));
    assert(!eq_%[1]s(a,b));
    set_%[1]s(a,b);
    assert(eq_%[1]s(a,b) && gx_i(gx_sget(alias,gx_int(0))) == 128);
    assert(!memcmp(key.b,expected.b,key.n));
    return 0;
}
`, id)
	main, err := os.ReadFile(filepath.Join(outDir, "main.c"))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(main), "int main(void)", "static int original_main(void)", 1) + check
	if err := os.WriteFile(filepath.Join(outDir, "main.c"), []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	// The generated build script uses the same collector discovery as ordinary
	// programs, including GOALCHEMY_BDWGC and system pkg-config installations.
	build := exec.Command("sh", "run.sh")
	build.Dir, build.Env = outDir, driver.ToolEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("emitted native helpers: %v\n%s", err, out)
	}
	cmd := exec.Command("sh", "targets/c/tests/byte_storage_test.sh")
	cmd.Dir = root
	cmd.Env = driver.ToolEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native runtime/collector: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

func TestCByteStorageSanitized(t *testing.T) {
	clang, err := testutil.SanitizerClang()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "targets/c/tests/byte_storage_test.sh")
	cmd.Dir = root
	cmd.Env = append(driver.ToolEnv(), "CC="+clang, "CFLAGS=-O1 -g -fsanitize=address,undefined -fno-sanitize-recover=all", "ASAN_OPTIONS=detect_stack_use_after_return=0:detect_leaks=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sanitized native runtime/collector: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

// The shared values fixture also guards native byte arrays nested inside struct
// and interface map keys; replay those additions on every accepted target.
func TestByteValuesAcrossTargets(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"go", "typescript", "python", "java", "csharp", "rust", "c", "swift"})
}
