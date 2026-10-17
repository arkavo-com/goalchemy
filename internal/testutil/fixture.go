// Package testutil runs language fixtures natively and through Goalchemy
// targets and compares their observations.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

// GeneratedSource reads native source artifacts listed by the build manifest.
// It is for textual assertions; target runners build the actual native files.
func GeneratedSource(out, extension string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest link.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	var source bytes.Buffer
	for _, name := range manifest.GeneratedFiles {
		if filepath.Ext(name) != extension {
			continue
		}
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			return nil, err
		}
		source.Write(data)
	}
	return source.Bytes(), nil
}

var llvmVersion = regexp.MustCompile(`(?m)^LLVM_VERSION='([^']+)'$`)

// SanitizerClang returns the clang installed from toolchains.lock.
func SanitizerClang() (string, error) {
	tc := driver.ToolchainRoot()
	if tc == "" {
		return "", fmt.Errorf(".toolchains not found; run scripts/fetch-toolchains.sh llvm")
	}
	lock, err := os.ReadFile(filepath.Join(filepath.Dir(tc), "toolchains.lock"))
	if err != nil {
		return "", err
	}
	m := llvmVersion.FindStringSubmatch(string(lock))
	if m == nil {
		return "", fmt.Errorf("toolchains.lock has no LLVM_VERSION")
	}
	clang := filepath.Join(tc, "llvm-"+m[1], "bin", "clang")
	st, err := os.Stat(clang)
	if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
		return "", fmt.Errorf("pinned clang unavailable at %s; run scripts/fetch-toolchains.sh llvm", clang)
	}
	return clang, nil
}

// Observation is what a fixture run produced.
type Observation struct {
	Stdout string
	Stderr string
	Exit   int
}

func (o Observation) String() string {
	return fmt.Sprintf("exit=%d\n--- stdout\n%s--- stderr\n%s", o.Exit, o.Stdout, o.Stderr)
}

var fatalLine = regexp.MustCompile(`(?m)^fatal error: [^\n]*\n`)

var panicTail = regexp.MustCompile(`(?s)\n(\[signal [^\n]*\]\n)?\ngoroutine \d+ .*$`)

// Normalize keeps standard error up to and including the first panic line
// and drops goroutine traces, which are not part of the observable contract.
func Normalize(o Observation) Observation {
	if loc := fatalLine.FindStringIndex(o.Stderr); loc != nil {
		o.Stderr = o.Stderr[:loc[1]]
		return o
	}
	if i := strings.Index(o.Stderr, "panic: "); i >= 0 && (i == 0 || o.Stderr[i-1] == '\n') {
		rest := o.Stderr[i:]
		if j := strings.IndexByte(rest, '\n'); j >= 0 {
			rest = rest[:j+1]
		}
		o.Stderr = o.Stderr[:i] + strings.TrimSuffix(rest, " [recovered]\n")
		if !strings.HasSuffix(o.Stderr, "\n") {
			o.Stderr += "\n"
		}
	}
	o.Stderr = panicTail.ReplaceAllString(o.Stderr, "\n")
	return o
}

// Unordered sorts output lines, for fixtures whose native order is unspecified.
func Unordered(o Observation) Observation {
	sortLines := func(s string) string {
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		sort.Strings(lines)
		return strings.Join(lines, "\n") + "\n"
	}
	o.Stdout = sortLines(o.Stdout)
	o.Stderr = sortLines(o.Stderr)
	return o
}

func run(dir string, timeout time.Duration, name string, args ...string) (Observation, error) {
	return runEnv(dir, timeout, nil, name, args...)
}

func runEnv(dir string, timeout time.Duration, env []string, name string, args ...string) (Observation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(driver.ToolEnv(), env...), "GOTOOLCHAIN="+frontend.ReferenceToolchain, "GOFLAGS=")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	o := Observation{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		o.Exit = ee.ExitCode()
		err = nil
	}
	if ctx.Err() != nil {
		return o, fmt.Errorf("%s timed out", name)
	}
	return o, err
}

var buildMu sync.Mutex

// Native builds and runs the fixture with the reference Go toolchain.
func Native(fixtureDir, work string) (Observation, error) {
	bin := filepath.Join(work, "native")
	abs, _ := filepath.Abs(fixtureDir)
	args := []string{"build", "-o", bin}
	replace := map[string]string{}
	err := filepath.WalkDir(abs, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte(frontend.TaskPackage)) {
			return nil
		}
		fset := token.NewFileSet()
		file, err := frontend.ParseFutureFile(fset, path, src, true)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := format.Node(&buf, fset, file); err != nil {
			return err
		}
		copy := filepath.Join(work, fmt.Sprintf("reference-%d.go", len(replace)))
		if err := os.WriteFile(copy, buf.Bytes(), 0600); err != nil {
			return err
		}
		replace[path] = copy
		return nil
	})
	if err != nil {
		return Observation{}, err
	}
	if len(replace) > 0 {
		data, err := json.Marshal(struct{ Replace map[string]string }{replace})
		if err != nil {
			return Observation{}, err
		}
		overlay := filepath.Join(work, "reference-overlay.json")
		if err := os.WriteFile(overlay, data, 0600); err != nil {
			return Observation{}, err
		}
		args = append(args, "-overlay", overlay)
	}
	args = append(args, ".")
	if o, err := run(abs, 2*time.Minute, "go", args...); err != nil || o.Exit != 0 {
		return o, fmt.Errorf("native build failed: %v\n%s", err, o.Stderr)
	}
	return run(work, time.Minute, bin)
}

// Compile compiles the fixture for target into out.
func Compile(fixtureDir, target, out string) []diagnostics.Diagnostic {
	return CompileGate(fixtureDir, target, out, "")
}

// CompileGate compiles with an explicit language gate.
func CompileGate(fixtureDir, target, out, gate string) []diagnostics.Diagnostic {
	return CompileGateOptions(fixtureDir, target, out, gate, driver.EmitOptions{})
}

// CompileGateOptions compiles with explicit emission options.
func CompileGateOptions(fixtureDir, target, out, gate string, opts driver.EmitOptions) []diagnostics.Diagnostic {
	abs, _ := filepath.Abs(fixtureDir)
	res, ds := driver.Build(context.Background(), driver.Options{Dir: abs, Gate: subset.Gate(gate)})
	if diagnostics.HasErrors(ds) {
		return ds
	}
	return driver.EmitWithOptions(target, res, out, opts)
}

// Runner knows how to build and run a compiled target directory.
type Runner func(out string) (Observation, error)

// Fixture subtests run in parallel. Limit Swift frontend builds so a full
// language matrix does not exhaust memory on a two-core CI runner.
var swiftBuilds = make(chan struct{}, 2)

// BuildSwift builds a compiled Swift program to .build/program without
// running it, sharing the fixture runner's build limit.
func BuildSwift(out string) error {
	swiftBuilds <- struct{}{}
	defer func() { <-swiftBuilds }()
	o, err := run(out, 5*time.Minute, "sh", "-c", "sed '/^exec /d' run.sh | sh")
	if err == nil && o.Exit != 0 {
		err = fmt.Errorf("swift build failed:\n%s", o.Stderr)
	}
	return err
}

// BuildCSharp compiles a generated C# program to bin/main.dll without
// running it.
func BuildCSharp(out string) error {
	o, err := run(out, 3*time.Minute, "sh", "-c", "sed '/^exec /d' run.sh | sh")
	if err == nil && o.Exit != 0 {
		err = fmt.Errorf("csharp build failed:\n%s", o.Stderr)
	}
	return err
}

// BuildJava compiles a generated Java program to classes without running
// it, and returns the command that runs it.
func BuildJava(out string) ([]string, error) {
	o, err := run(out, 3*time.Minute, "sh", "-c", "sed '/^exec /d' run.sh | sh")
	if err == nil && o.Exit != 0 {
		err = fmt.Errorf("java build failed:\n%s", o.Stderr)
	}
	if err != nil {
		return nil, err
	}
	script, err := os.ReadFile(filepath.Join(out, "run.sh"))
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(script), "\n") {
		if cmd, ok := strings.CutPrefix(line, "exec "); ok {
			return strings.Fields(cmd), nil
		}
	}
	return nil, fmt.Errorf("java run.sh has no exec line")
}

var Runners = map[string]Runner{
	"swift": func(out string) (Observation, error) {
		swiftBuilds <- struct{}{}
		defer func() { <-swiftBuilds }()
		return run(out, 5*time.Minute, "sh", "run.sh")
	},
	"go": func(out string) (Observation, error) {
		if o, err := run(out, 2*time.Minute, "go", "build", "-o", "prog", "."); err != nil || o.Exit != 0 {
			return o, fmt.Errorf("go build failed: %v\n%s", err, o.Stderr)
		}
		return run(out, time.Minute, filepath.Join(out, "prog"))
	},
	"typescript": func(out string) (Observation, error) {
		return run(out, time.Minute, "node", "--stack-size=4000", "main.ts")
	},
	"python": func(out string) (Observation, error) {
		return run(out, 2*time.Minute, "python3", "main.py")
	},
	"java": func(out string) (Observation, error) {
		return run(out, 3*time.Minute, "sh", "run.sh")
	},
	"csharp": func(out string) (Observation, error) {
		return run(out, 3*time.Minute, "sh", "run.sh")
	},
	"c": func(out string) (Observation, error) {
		return run(out, 3*time.Minute, "sh", "run.sh")
	},
	// c-sanitize uses the pinned clang and its address and undefined-behavior
	// sanitizers with normal address-space randomization.
	"c-sanitize": func(out string) (Observation, error) {
		clang, err := SanitizerClang()
		if err != nil {
			return Observation{}, err
		}
		return runEnv(out, 5*time.Minute, []string{
			"CC=" + clang,
			"CFLAGS=-O1 -g -fsanitize=address,undefined -fno-sanitize-recover=all -fno-omit-frame-pointer",
			"ASAN_OPTIONS=detect_leaks=0:detect_stack_use_after_return=0",
		}, "sh", "run.sh")
	},
	"rust": func(out string) (Observation, error) {
		bin, err := BuildRust(out)
		if err != nil {
			return Observation{}, err
		}
		return run(out, 3*time.Minute, bin)
	},
}

// Programs that use native library contracts build with Cargo and depend on
// vendored OpenSSL, reqwest and tokio. Every such build shares one target
// directory so those dependencies compile once per machine instead of once
// per fixture. Every generated crate is named goalchemy-generated and uplifts
// its binary to release/main, so a build and the copy of its binary happen
// under a process and a file lock.
var cargoBuilds sync.Mutex

func cargoTargetDir() string {
	if dir := os.Getenv("GOALCHEMY_CARGO_TARGET_DIR"); dir != "" {
		return dir
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "out", "fixture-cargo", "target")
}

// BuildRust compiles a generated Rust program to out/main without running it
// and returns the binary's path.
func BuildRust(out string) (string, error) {
	script, err := os.ReadFile(filepath.Join(out, "run.sh"))
	if err != nil {
		return "", err
	}
	bin := filepath.Join(out, "main")
	if !strings.Contains(string(script), "cargo run") {
		o, err := run(out, 15*time.Minute, "rustc", "--edition", "2021", "-C", "opt-level=2", "-C", "debuginfo=0", "-o", "main", "src/main.rs")
		if err == nil && o.Exit != 0 {
			err = fmt.Errorf("rustc failed:\n%s", o.Stderr)
		}
		if err != nil {
			return "", err
		}
		return bin, nil
	}
	target := cargoTargetDir()
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	cargoBuilds.Lock()
	defer cargoBuilds.Unlock()
	unlock, err := lockFile(filepath.Join(target, ".goalchemy-lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	o, err := runEnv(out, 15*time.Minute, []string{"CARGO_TARGET_DIR=" + target}, "cargo", "build", "--release", "--quiet")
	if err == nil && o.Exit != 0 {
		err = fmt.Errorf("cargo build failed:\n%s", o.Stderr)
	}
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(target, "release", "main"))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		return "", err
	}
	return bin, nil
}

// EmitTarget maps a runner name to the target it compiles: runner
// variants such as c-sanitize share their base target's output.
func EmitTarget(runner string) string {
	if base, _, ok := strings.Cut(runner, "-"); ok {
		return base
	}
	return runner
}

// Fixture describes one language fixture directory.
type Fixture struct {
	Name string
	Dir  string
	// Unordered marks fixtures whose output order differs from native Go
	// only by unspecified map iteration order.
	Unordered bool
	// Reject lists diagnostic codes the fixture must produce.
	Reject []string
	// Golden holds the expected normalized observation for fixtures that
	// assert Goalchemy-selected behavior native Go does not share.
	Golden string
	// Gate is the language gate the fixture needs.
	Gate string
}

var directive = regexp.MustCompile(`(?m)^// goalchemy:(\w+)(?: (.*))?$`)

// ParseDirectives reads the fixture directives understood by Discover.
func ParseDirectives(src string) Fixture {
	var f Fixture
	for _, m := range directive.FindAllStringSubmatch(src, -1) {
		switch m[1] {
		case "unordered":
			f.Unordered = true
		case "reject":
			f.Reject = strings.Fields(m[2])
		case "gate":
			f.Gate = strings.TrimSpace(m[2])
		}
	}
	return f
}

func Discover(root string) ([]Fixture, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Fixture
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		f := Fixture{Name: e.Name(), Dir: filepath.Join(root, e.Name())}
		src, _ := os.ReadFile(filepath.Join(f.Dir, "main.go"))
		parsed := ParseDirectives(string(src))
		f.Unordered, f.Reject, f.Gate = parsed.Unordered, parsed.Reject, parsed.Gate
		if want, err := os.ReadFile(filepath.Join(f.Dir, "want.txt")); err == nil {
			f.Golden = string(want)
		}
		out = append(out, f)
	}
	return out, nil
}

// ParseObservation parses the String form of an Observation.
func ParseObservation(s string) Observation {
	var o Observation
	head, rest, _ := strings.Cut(s, "\n--- stdout\n")
	fmt.Sscanf(head, "exit=%d", &o.Exit)
	o.Stdout, o.Stderr, _ = strings.Cut(rest, "--- stderr\n")
	return o
}

// RunFixture runs one fixture through targets and reports mismatches.
func RunFixture(t *testing.T, f Fixture, targets []string) {
	RunFixtureOptions(t, f, targets, driver.EmitOptions{})
}

// RunFixtureOptions compares a selected emission mode with the native oracle.
func RunFixtureOptions(t *testing.T, f Fixture, targets []string, opts driver.EmitOptions) {
	t.Helper()
	work := t.TempDir()
	if len(f.Reject) > 0 {
		ds := CompileGateOptions(f.Dir, targets[0], filepath.Join(work, targets[0]), f.Gate, opts)
		got := map[string]bool{}
		for _, d := range ds {
			got[d.Code] = true
		}
		for _, c := range f.Reject {
			if !got[c] {
				t.Errorf("expected diagnostic %s, got %v", c, ds)
			}
		}
		return
	}
	var want Observation
	if f.Golden != "" {
		want = ParseObservation(f.Golden)
	} else {
		native, err := Native(f.Dir, work)
		if err != nil {
			t.Fatal(err)
		}
		want = Normalize(native)
	}
	var first *Observation
	for _, target := range targets {
		out := filepath.Join(work, target)
		if ds := CompileGateOptions(f.Dir, EmitTarget(target), out, f.Gate, opts); len(ds) > 0 {
			for _, d := range ds {
				t.Errorf("%s: %s", target, d)
			}
			continue
		}
		obs, err := Runners[target](out)
		if err != nil {
			t.Errorf("%s: %v", target, err)
			continue
		}
		got := Normalize(obs)
		cmpWant, cmpGot := want, got
		if f.Unordered {
			cmpWant, cmpGot = Unordered(want), Unordered(got)
		}
		if cmpGot != cmpWant {
			t.Errorf("%s differs from expected\n=== expected\n%s=== %s\n%s", target, want, target, got)
		}
		if first == nil {
			first = &got
		} else if got != *first {
			t.Errorf("%s differs from %s\n=== %s\n%s=== %s\n%s", target, targets[0], targets[0], *first, target, got)
		}
	}
}

// HasDirective reports whether src carries "// goalchemy:<name>".
func HasDirective(src, name string) bool {
	for _, m := range directive.FindAllStringSubmatch(src, -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

// Names selects compact or readable private names, the two decorated modes
// that tests compare against each other.
func Names(compact bool) string {
	if compact {
		return "compact"
	}
	return "readable"
}
