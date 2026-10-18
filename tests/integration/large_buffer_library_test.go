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

func TestGeneratedLargeBufferLibraries(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := filepath.Join(root, "tests/integration/testdata/large_buffer_library")
	for _, target := range []string{"typescript", "python", "java", "csharp", "c"} {
		for _, pause := range driver.PauseModels {
			if target == "c" && pause == "native" {
				continue
			}
			t.Run(target+"/"+pause, func(t *testing.T) {
				source, parent, consumer := t.TempDir(), t.TempDir(), t.TempDir()
				out := filepath.Join(parent, "probe")
				write := func(dir, name, text string) {
					t.Helper()
					if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				copyFixture := func(name, dir string) {
					t.Helper()
					data, err := os.ReadFile(filepath.Join(fixtures, name))
					if err != nil {
						t.Fatal(err)
					}
					write(dir, name, string(data))
				}
				write(source, "go.mod", fmt.Sprintf("module largebufferprobe\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %q\n", root))
				write(source, "library.go", `package probe
func Echo(value []byte) []byte {
 if len(value) > 0 { value[0]++; value[len(value)-1]++ }
 return value
}
func StringSize(value string) int { return len(value) }
`)
				if ds := testutil.CompileGateOptions(source, target, out, "cooperative", driver.EmitOptions{PauseModel: pause}); len(ds) > 0 {
					t.Fatal(ds)
				}
				env := driver.ToolEnv()
				run := func(dir, name string, args ...string) string {
					t.Helper()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
				case "typescript":
					copyFixture("consumer.ts", out)
					write(out, "tsconfig.json", `{"compilerOptions":{"target":"ES2022","module":"NodeNext","moduleResolution":"NodeNext","rewriteRelativeImportExtensions":true,"outDir":"dist","strict":true,"skipLibCheck":true,"lib":["ES2022","DOM","DOM.Iterable"]},"include":["main.ts","rt/**/*.ts","consumer.ts"]}`)
					run(out, "tsc", "-p", ".")
					result = run(out, "node", "dist/consumer.js")
				case "python":
					result = run(consumer, "python3", filepath.Join(fixtures, "consumer.py"), parent)
				case "java":
					run(out, "sh", "build.sh")
					copyFixture("LargeBufferConsumer.java", consumer)
					bin := ""
					for _, entry := range env {
						if strings.HasPrefix(entry, "JAVA_HOME=") {
							bin = filepath.Join(strings.TrimPrefix(entry, "JAVA_HOME="), "bin")
						}
					}
					jar := filepath.Join(out, "goalchemy-generated.jar")
					run(consumer, filepath.Join(bin, "javac"), "-cp", jar, "-d", consumer, "LargeBufferConsumer.java")
					result = run(consumer, filepath.Join(bin, "java"), "-cp", consumer+string(os.PathListSeparator)+jar, "consumer.LargeBufferConsumer")
				case "csharp":
					dotnet := filepath.Join(root, ".toolchains/dotnet/dotnet")
					run(out, dotnet, "build", "main.csproj", "-c", "Release", "-o", "lib", "--nologo")
					copyFixture("LargeBufferConsumer.cs", consumer)
					write(consumer, "consumer.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework></PropertyGroup><ItemGroup><Reference Include="main"><HintPath>`+filepath.Join(out, "lib/main.dll")+`</HintPath></Reference></ItemGroup></Project>`)
					run(consumer, dotnet, "build", "consumer.csproj", "-o", "bin", "--nologo")
					result = run(consumer, dotnet, filepath.Join(consumer, "bin/consumer.dll"))
				case "c":
					run(out, "sh", "build.sh")
					copyFixture("consumer.c", consumer)
					gc := "-lgc"
					for _, entry := range env {
						if prefix := strings.TrimPrefix(entry, "GOALCHEMY_BDWGC="); prefix != entry && prefix != "" {
							gc = filepath.Join(prefix, "lib", "libgc.a")
						}
					}
					run(consumer, "cc", "-std=c17", "-O2", "-Wall", "-Wextra", "-Werror", "-I"+out, "consumer.c", filepath.Join(out, "libgoalchemy.a"), gc, "-lpthread", "-lm", "-o", "consumer")
					result = run(consumer, filepath.Join(consumer, "consumer"))
				}
				if !strings.Contains(result, "PASS large buffer library") {
					t.Fatal("missing native consumer completion", result)
				}
				t.Log(result)
			})
		}
	}
}
