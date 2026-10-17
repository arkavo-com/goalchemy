package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

// A value-only library has no linked operation files. Its SwiftPM package must
// build independently, including when a consumer keeps it in a path with spaces.
func TestSwiftPureLibraryOutput(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source, consumer := t.TempDir(), t.TempDir()
	out := filepath.Join(t.TempDir(), "library with spaces")
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "go.mod", fmt.Sprintf("module pureswiftprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %q\n", root))
	write(source, "library.go", "package pure\nfunc Identity(value string) string { return value }\n")
	if ds := testutil.CompileGate(source, "swift", out, "sequential"); len(ds) != 0 {
		t.Fatal(ds)
	}
	if _, err := os.Stat(filepath.Join(out, "rt/runtime")); !os.IsNotExist(err) {
		t.Fatalf("pure library unexpectedly linked operation files: %v", err)
	}
	license, err := os.ReadFile(filepath.Join(out, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil || !bytes.Equal(license, original) {
		t.Fatal("generated license differs from compiler runtime license", err)
	}
	write(consumer, "Package.swift", fmt.Sprintf(`// swift-tools-version: 6.0
import PackageDescription
let package = Package(name: "PureConsumer",
    dependencies: [.package(name: "GoalchemyGenerated", path: %q)],
    targets: [.executableTarget(name: "PureConsumer",
        dependencies: [.product(name: "GoalchemyGenerated", package: "GoalchemyGenerated")],
        path: ".", sources: ["main.swift"])], swiftLanguageModes: [.v5])
`, out))
	write(consumer, "main.swift", `import GoalchemyGenerated
let value = GoString(bytes: [255, 0, 128])
let result = try Identity(value).wait()
precondition(result == value, "pure library changed byte strings")
print("PASS pure Swift library")
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "swift run -c release -Xswiftc -Onone")
	cmd.Dir, cmd.Env = consumer, driver.ToolEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pure SwiftPM consumer: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "PASS pure Swift library") {
		t.Fatal("native consumer did not complete", string(output))
	}
}
