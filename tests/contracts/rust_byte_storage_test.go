package contracts

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestRustByteGrowthNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "byte_growth", Dir: "testdata/byte_growth"}, []string{"rust"})
}

func TestRustByteValuesNativeOracle(t *testing.T) {
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"rust"})
}

func TestRustByteGCNativeOracle(t *testing.T) {
	t.Setenv("GOALCHEMY_GC_THRESHOLD", "1")
	// Escaped pointers, views, fields and boxes survive safepoints in the common
	// fixture; the values fixture also retains native storage in suspended frames.
	testutil.RunFixture(t, testutil.Fixture{Name: "byte_storage", Dir: "../language/testdata/byte_storage"}, []string{"rust"})
	testutil.RunFixture(t, testutil.Fixture{Name: "java_byte_values", Dir: "testdata/java_byte_values", Gate: "cooperative"}, []string{"rust"})
}

func TestRustByteStorage(t *testing.T) {
	outDir := t.TempDir()
	if ds := testutil.CompileGateOptions("../language/testdata/byte_storage", "rust", outDir, "sequential", driver.EmitOptions{Names: "readable"}); len(ds) > 0 {
		t.Fatal(ds)
	}
	src, err := testutil.GeneratedSource(outDir, ".rs")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"byte_array(vec![0; 4])", "byte_array_clone(_x)", "byte_array_set(_d, _s)", "byte_array_eq(_a, _b)", "byte_array_key(_a)", "BYTE_NIL", "make_byte_slice(", "append_bytes(", ".i() as u8", "slice_to_array(", "make_slice("} {
		if !strings.Contains(string(src), want) {
			t.Errorf("emitted byte program lacks %q", want)
		}
	}
	// Execute the emitted helpers too: behavioral oracles alone could hide a
	// compiler path which constructs generic storage despite native primitives.
	match := regexp.MustCompile(`fn z_([A-Za-z0-9_]+)\(\) -> V \{ byte_array\(vec!\[0; 4\]\) \}`).FindSubmatch(src)
	if match == nil {
		t.Fatal("missing emitted native byte-array zero helper")
	}
	id := string(match[1])
	data, err := os.ReadFile(filepath.Join(outDir, "goalchemy.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest link.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	helperModule := ""
	for _, name := range manifest.GeneratedFiles {
		if filepath.Ext(name) != ".rs" {
			continue
		}
		file, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(file), string(match[0])) {
			helperModule = strings.TrimSuffix(filepath.Base(name), ".rs")
		}
	}
	if helperModule == "" {
		t.Fatal("missing native helper module")
	}
	check := fmt.Sprintf(`
#[test]
fn emitted_byte_helpers() {
    use %[2]s::{z_%[1]s,c_%[1]s,eq_%[1]s,k_%[1]s,set_%[1]s};
    let a = z_%[1]s();
    let alias = slice_array(a.clone(), V::Nil, V::Nil, V::Nil, false);
    assert!(matches!(alias, V::ByteSlice(..)));
    with(a.h(), |o| match o { Obj::Bytes(v) => assert_eq!(v, &[0; 4]), _ => panic!("generic emitted array") });
    set_slot(a.h(), 0, V::Int(255));
    let b = c_%[1]s(&a);
    assert_ne!(a.h(), b.h());
    assert!(eq_%[1]s(&a, &b));
    let key = k_%[1]s(&a);
    assert!(matches!(&key, Key::Bytes(v) if v.as_ref() == &[255, 0, 0, 0]));
    set_slot(b.h(), 0, V::Int(128));
    assert!(!eq_%[1]s(&a, &b));
    set_%[1]s(&a, &b);
    assert_eq!(sget(alias, V::Int(0)).i(), 128);
    assert!(eq_%[1]s(&a, &b));
    assert!(matches!(key, Key::Bytes(v) if v.as_ref() == &[255, 0, 0, 0]));
}
`, id, helperModule)
	entry := filepath.Join(outDir, "src", "main.rs")
	main, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, append(main, []byte(check)...), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("rustc", "--edition", "2021", "-Awarnings", "--test", "-C", "opt-level=1", "-o", "byte_helpers", "src/main.rs")
	build.Dir, build.Env = outDir, driver.ToolEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("emitted native helpers compile: %v\n%s", err, out)
	}
	run := exec.Command(filepath.Join(outDir, "byte_helpers"))
	run.Env = driver.ToolEnv()
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("emitted native helpers: %v\n%s", err, out)
	}
	cmd := exec.Command("sh", "targets/rust/tests/byte_storage_test.sh")
	cmd.Dir = root
	cmd.Env = driver.ToolEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native byte storage: %v\n%s", err, out)
	}
}
