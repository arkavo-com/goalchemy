package language

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

// Cooperative fixtures must schedule identically under both pause models,
// including under other scheduler choice seeds.
func TestGoPauseModelsAgree(t *testing.T) {
	pauseModelsAgree(t, "go", func(t *testing.T, out string) []string {
		build := exec.Command("go", "build", "-o", "prog", ".")
		build.Dir = out
		if b, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\n%s", err, b)
		}
		return []string{filepath.Join(out, "prog")}
	})
}

func TestTypeScriptPauseModelsAgree(t *testing.T) {
	pauseModelsAgree(t, "typescript", func(t *testing.T, out string) []string {
		return []string{"node", "--stack-size=4000", "main.ts"}
	})
}

func TestSwiftPauseModelsAgree(t *testing.T) {
	pauseModelsAgree(t, "swift", func(t *testing.T, out string) []string {
		if err := testutil.BuildSwift(out); err != nil {
			t.Fatal(err)
		}
		return []string{filepath.Join(out, ".build", "program")}
	})
}

func TestCSharpPauseModelsAgree(t *testing.T) {
	pauseModelsAgree(t, "csharp", func(t *testing.T, out string) []string {
		if err := testutil.BuildCSharp(out); err != nil {
			t.Fatal(err)
		}
		return []string{"sh", "-c", "exec dotnet bin/main.dll"}
	})
}

func TestJavaPauseModelsAgree(t *testing.T) {
	pauseModelsAgree(t, "java", func(t *testing.T, out string) []string {
		cmd, err := testutil.BuildJava(out)
		if err != nil {
			t.Fatal(err)
		}
		return cmd
	})
}

// pauseModelsAgree compiles every cooperative fixture for target under both
// pause models; prepare returns the command that runs a compiled program.
func pauseModelsAgree(t *testing.T, target string, prepare func(t *testing.T, out string) []string) {
	fixtures, err := testutil.Discover("testdata")
	if err != nil {
		t.Fatal(err)
	}
	only := os.Getenv("FIXTURE")
	for _, f := range fixtures {
		if f.Gate != "cooperative" || len(f.Reject) > 0 || (only != "" && !strings.Contains(f.Name, only)) {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			cmds := map[string][]string{}
			for _, model := range []string{"frames", "native"} {
				out := filepath.Join(work, model)
				if ds := testutil.CompileGateOptions(f.Dir, target, out, f.Gate, driver.EmitOptions{PauseModel: model}); len(ds) > 0 {
					t.Fatalf("%s: %v", model, ds)
				}
				cmds[model] = prepare(t, out)
			}
			for _, seed := range []string{"1", "7", "4242"} {
				frames, native := runSeed(t, filepath.Join(work, "frames"), cmds["frames"], seed), runSeed(t, filepath.Join(work, "native"), cmds["native"], seed)
				if frames != native {
					t.Errorf("seed %s: native differs from frames\n=== frames\n%s=== native\n%s", seed, frames, native)
				}
			}
		})
	}
}

func runSeed(t *testing.T, dir string, argv []string, seed string) testutil.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(driver.ToolEnv(), "GOALCHEMY_SEED="+seed)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	o := testutil.Observation{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		o.Exit = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o
}
