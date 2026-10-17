package integration

import (
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

// TestGeneratedFloatLibraries uses actual importing consumers, separately from
// the source language fixtures. No target is skipped by the short test suite.
func TestGeneratedFloatLibraries(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := filepath.Join(root, "tests/integration/testdata/float_library")
	targets := []string{"go", "typescript", "python", "java", "csharp", "rust", "c", "swift"}
	for _, compact := range []bool{false, true} {
		mode := "readable"
		if compact {
			mode = "compact"
		}
		for _, target := range targets {
			t.Run(mode+"/"+target, func(t *testing.T) {
				source, parent, consumer := t.TempDir(), t.TempDir(), t.TempDir()
				out := filepath.Join(parent, "probe")
				write := func(dir, name, text string) {
					t.Helper()
					if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				copyFixture := func(name, dir, dest string) {
					t.Helper()
					data, err := os.ReadFile(filepath.Join(fixtures, name))
					if err != nil {
						t.Fatal(err)
					}
					write(dir, dest, string(data))
				}
				write(source, "go.mod", fmt.Sprintf("module floatlibraryprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %q\n", root))
				copyFixture("library.go.in", source, "library.go")
				if ds := testutil.CompileGateOptions(source, target, out, "cooperative", driver.EmitOptions{Names: testutil.Names(compact)}); len(ds) > 0 {
					t.Fatal(ds)
				}
				env := driver.ToolEnv()
				run := func(dir, name string, args ...string) string {
					t.Helper()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					cmd := exec.CommandContext(ctx, name, args...)
					cmd.Dir, cmd.Env = dir, env
					data, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("%s %v: %v\n%s", name, args, err, data)
					}
					return string(data)
				}
				var result string
				switch target {
				case "go":
					write(consumer, "go.mod", fmt.Sprintf("module floatconsumer\n\ngo 1.25\nrequire goalchemyout v0.0.0\nreplace goalchemyout => %q\n", out))
					copyFixture("consumer.go.in", consumer, "consumer_test.go")
					result = run(consumer, "go", "test", "-race", "-count=1", "./...")
				case "typescript":
					copyFixture("consumer.ts", out, "consumer.ts")
					write(out, "tsconfig.json", `{"compilerOptions":{"target":"ES2022","module":"NodeNext","moduleResolution":"NodeNext","rewriteRelativeImportExtensions":true,"declaration":true,"outDir":"dist","strict":true,"skipLibCheck":true,"lib":["ES2022","DOM","DOM.Iterable"]},"include":["main.ts","rt/**/*.ts","consumer.ts"]}`)
					run(out, "tsc", "-p", ".")
					result = run(out, "node", "dist/consumer.js")
				case "python":
					result = run(consumer, "python3", filepath.Join(fixtures, "consumer.py"), parent)
				case "java":
					run(out, "sh", "build.sh")
					copyFixture("FloatConsumer.java", consumer, "FloatConsumer.java")
					javaBin := ""
					for _, entry := range env {
						if strings.HasPrefix(entry, "JAVA_HOME=") {
							javaBin = filepath.Join(strings.TrimPrefix(entry, "JAVA_HOME="), "bin")
						}
					}
					jar := filepath.Join(out, "goalchemy-generated.jar")
					run(consumer, filepath.Join(javaBin, "javac"), "-cp", jar, "-d", consumer, "FloatConsumer.java")
					result = run(consumer, filepath.Join(javaBin, "java"), "-cp", consumer+string(os.PathListSeparator)+jar, "consumer.FloatConsumer")
				case "csharp":
					dotnet := filepath.Join(root, ".toolchains/dotnet/dotnet")
					run(out, dotnet, "build", "main.csproj", "-c", "Release", "-o", "lib", "--nologo")
					copyFixture("FloatConsumer.cs", consumer, "FloatConsumer.cs")
					write(consumer, "consumer.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><Reference Include="main"><HintPath>`+filepath.Join(out, "lib/main.dll")+`</HintPath></Reference></ItemGroup></Project>`)
					run(consumer, dotnet, "build", "consumer.csproj", "-o", "bin", "--nologo")
					result = run(consumer, dotnet, filepath.Join(consumer, "bin/consumer.dll"))
				case "rust":
					copyFixture("consumer.rs", consumer, "main.rs")
					write(consumer, "Cargo.toml", fmt.Sprintf("[package]\nname=\"float-consumer\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[dependencies]\ngoalchemy-generated={path=%q}\n[[bin]]\nname=\"float-consumer\"\npath=\"main.rs\"\n", out))
					env = append(env, "CARGO_TARGET_DIR="+filepath.Join(root, "out/float-library-cargo"))
					result = run(consumer, "cargo", "run", "--release", "--offline", "--quiet")
				case "swift":
					copyFixture("FloatConsumer.swift", consumer, "FloatConsumer.swift")
					write(consumer, "Package.swift", fmt.Sprintf(`// swift-tools-version: 6.0
import PackageDescription
let package = Package(
    name: "FloatConsumer",
    dependencies: [.package(name: "GoalchemyGenerated", path: %q)],
    targets: [.executableTarget(name: "FloatConsumer",
        dependencies: [.product(name: "GoalchemyGenerated", package: "GoalchemyGenerated")],
        path: ".", sources: ["FloatConsumer.swift"])],
    swiftLanguageModes: [.v5]
)
`, out))
					swift := "swift"
					for _, entry := range env {
						if strings.HasPrefix(entry, "PATH=") {
							for _, dir := range filepath.SplitList(strings.TrimPrefix(entry, "PATH=")) {
								candidate := filepath.Join(dir, "swift")
								if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
									swift = candidate
									break
								}
							}
						}
					}
					result = run(consumer, swift, "run", "-c", "release", "-Xswiftc", "-Onone")
				case "c":
					run(out, "sh", "build.sh")
					copyFixture("consumer.c", consumer, "consumer.c")
					gc := "-lgc"
					for _, entry := range env {
						if prefix := strings.TrimPrefix(entry, "GOALCHEMY_BDWGC="); prefix != entry && prefix != "" {
							gc = filepath.Join(prefix, "lib", "libgc.a")
						}
					}
					run(consumer, "cc", "-std=c17", "-O2", "-I"+out, "consumer.c", filepath.Join(out, "libgoalchemy.a"), gc, "-lpthread", "-lm", "-o", "consumer")
					result = run(consumer, filepath.Join(consumer, "consumer"))
				}
				if target != "go" && !strings.Contains(result, "PASS float library") {
					t.Fatal("missing native consumer completion", result)
				}
				t.Log(result)
			})
		}
	}
}
