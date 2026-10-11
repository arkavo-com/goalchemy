// Command hardening runs bounded differential campaigns, performance baselines,
// and versioned upgrade checks for all released targets.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/conformance"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/hardening"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

const reportVersion = 2

var allTargets = []string{"go", "typescript", "python", "java", "csharp", "rust", "c", "swift"}

type targetList []string

func (t *targetList) String() string { return strings.Join(*t, ",") }
func (t *targetList) Set(v string) error {
	for _, name := range strings.Split(v, ",") {
		found := false
		for _, known := range allTargets {
			found = found || name == known
		}
		if !found {
			return fmt.Errorf("unknown target %q", name)
		}
		*t = append(*t, name)
	}
	return nil
}

type result struct {
	Target            string   `json:"target"`
	TargetVersion     string   `json:"target_version"`
	Status            string   `json:"status"`
	Cases             int      `json:"cases"`
	ContractCases     int      `json:"contract_cases,omitempty"`
	BuildSHA256       string   `json:"build_sha256,omitempty"`
	ObservationSHA256 string   `json:"observation_sha256,omitempty"`
	CompileMS         []int64  `json:"compile_ms,omitempty"`
	RunMS             []int64  `json:"run_ms,omitempty"`
	Failures          []string `json:"failures,omitempty"`
}

type report struct {
	SchemaVersion         int               `json:"schema_version"`
	Mode                  string            `json:"mode"`
	Compiler              string            `json:"compiler"`
	SourceProfile         string            `json:"source_profile"`
	Revision              string            `json:"revision"`
	Dirty                 bool              `json:"dirty"`
	ContractCatalogSHA256 string            `json:"contract_catalog_sha256"`
	ToolchainsLockSHA256  string            `json:"toolchains_lock_sha256"`
	Toolchains            map[string]string `json:"toolchains"`
	Host                  map[string]string `json:"host"`
	Seed                  uint64            `json:"seed,omitempty"`
	Cases                 int               `json:"cases,omitempty"`
	Operations            int               `json:"operations,omitempty"`
	BudgetSeconds         int               `json:"budget_seconds"`
	ElapsedMS             int64             `json:"elapsed_ms"`
	Results               []result          `json:"results"`
	Changes               []string          `json:"changes,omitempty"`
	Reproducer            string            `json:"reproducer,omitempty"`
}

func main() {
	mode := flag.String("mode", "compat", "compat, baseline, or fuzz")
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "", "JSON report path (required)")
	previous := flag.String("previous", "", "previous compatible report for upgrade comparison")
	seed := flag.Uint64("seed", 1, "fuzz campaign seed")
	cases := flag.Int("cases", 2, "maximum fuzz programs")
	ops := flag.Int("ops", 64, "operations per fuzz program")
	budget := flag.Duration("budget", 15*time.Minute, "campaign wall-time budget, checked between target runs")
	samples := flag.Int("samples", 2, "baseline measurements per target")
	var targets targetList
	flag.Var(&targets, "target", "target name or comma-separated names; repeatable")
	flag.Parse()
	if *out == "" || (*mode != "compat" && *mode != "baseline" && *mode != "fuzz") || *cases < 1 || *ops < 1 || *samples < 1 || *budget <= 0 {
		fmt.Fprintln(os.Stderr, "usage: hardening -mode compat|baseline|fuzz -out report.json [-target names] [-budget 15m]")
		os.Exit(2)
	}
	if len(targets) == 0 {
		targets = append(targets, allTargets...)
	}
	seen := map[string]bool{}
	for _, name := range targets {
		if seen[name] {
			fmt.Fprintln(os.Stderr, "duplicate target:", name)
			os.Exit(2)
		}
		seen[name] = true
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	if err := os.Chdir(abs); err != nil {
		fail(err)
	}
	if _, err := os.Stat("go.mod"); err != nil {
		fail(fmt.Errorf("%s is not the repository root: %w", abs, err))
	}
	start := time.Now()
	lock, err := os.ReadFile("toolchains.lock")
	if err != nil {
		fail(err)
	}
	r := report{SchemaVersion: reportVersion, Mode: *mode, Compiler: link.CompilerVersion,
		SourceProfile: frontend.SourceLanguage, Revision: command("git", "rev-parse", "HEAD"),
		Dirty: command("git", "status", "--porcelain") != "", ToolchainsLockSHA256: hashText(string(lock)),
		Toolchains: toolchains(), Host: host(),
		BudgetSeconds: int(budget.Seconds())}
	deadline := start.Add(*budget)
	switch *mode {
	case "compat":
		r.Results = compatibility(abs, targets, deadline)
	case "baseline":
		r.Results = baseline(abs, targets, *samples, deadline)
	case "fuzz":
		r.Seed, r.Cases, r.Operations = *seed, *cases, *ops
		r.Results, r.Reproducer = fuzz(abs, targets, *seed, *cases, *ops, deadline, *out)
	}
	cat, ds := catalog.Load(os.DirFS(abs))
	if len(ds) > 0 {
		fail(fmt.Errorf("catalog: %v", ds))
	}
	for i := range r.Results {
		r.Results[i].TargetVersion = cat.Targets[r.Results[i].Target].Version
	}
	r.ContractCatalogSHA256, err = catalogHash(abs)
	if err != nil {
		fail(err)
	}
	r.ElapsedMS = time.Since(start).Milliseconds()
	if *previous != "" {
		old, err := readReport(*previous)
		if err != nil {
			fail(err)
		}
		if old.Mode != r.Mode {
			fail(fmt.Errorf("previous report mode %s differs from %s", old.Mode, r.Mode))
		}
		r.Changes = changes(old, r)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fail(err)
	}
	bad := false
	for _, tr := range r.Results {
		fmt.Printf("%s: %s (%d cases)\n", tr.Target, tr.Status, tr.Cases)
		bad = bad || tr.Status != "pass"
	}
	for _, change := range r.Changes {
		fmt.Println("change:", change)
	}
	fmt.Println("report:", *out)
	if bad {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "hardening:", err)
	os.Exit(2)
}

func command(name string, args ...string) string {
	env := driver.ToolEnv()
	if !filepath.IsAbs(name) {
		for _, item := range env {
			key, value, ok := strings.Cut(item, "=")
			if key != "PATH" || !ok {
				continue
			}
			for _, dir := range filepath.SplitList(value) {
				candidate := filepath.Join(dir, name)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
					name = candidate
					break
				}
			}
			break
		}
	}
	c := exec.Command(name, args...)
	c.Env = env
	data, err := c.CombinedOutput()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
}

func toolchains() map[string]string {
	out := map[string]string{
		"go": command("go", "version"), "node": command("node", "--version"),
		"python": command("python3", "--version"), "java": command("javac", "-version"),
		"dotnet": command("dotnet", "--version"), "rust": command("rustc", "--version"),
		"c": command("cc", "--version"), "bdwgc": bdwgcVersion(), "swift": command("swift", "--version"),
	}
	if clang, err := testutil.SanitizerClang(); err == nil {
		out["clang"] = command(clang, "--version")
	} else {
		out["clang"] = "unavailable: " + err.Error()
	}
	return out
}

func bdwgcVersion() string {
	tc := driver.ToolchainRoot()
	if tc == "" {
		return "unavailable: .toolchains not found"
	}
	data, err := os.ReadFile(filepath.Join(tc, "bdwgc", "include", "gc", "gc_version.h"))
	if err != nil {
		return "unavailable: " + err.Error()
	}
	version, err := parseBDWGCVersion(data)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return version
}

func parseBDWGCVersion(data []byte) (string, error) {
	want := []string{"GC_TMP_VERSION_MAJOR", "GC_TMP_VERSION_MINOR", "GC_TMP_VERSION_MICRO"}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "#define" {
			if _, err := strconv.Atoi(fields[2]); err == nil {
				values[fields[1]] = fields[2]
			}
		}
	}
	for _, key := range want {
		if values[key] == "" {
			return "", fmt.Errorf("gc_version.h lacks %s", key)
		}
	}
	return values[want[0]] + "." + values[want[1]] + "." + values[want[2]], nil
}

func host() map[string]string {
	out := map[string]string{"goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"logical_cpus": fmt.Sprint(runtime.NumCPU()), "kernel": command("uname", "-sr")}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok && strings.TrimSpace(key) == "model name" {
				out["cpu_model"] = strings.TrimSpace(value)
				break
			}
		}
	}
	return out
}

func readReport(path string) (report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return report{}, err
	}
	var r report
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	if r.SchemaVersion != reportVersion {
		return r, fmt.Errorf("%s: unsupported report schema %d", path, r.SchemaVersion)
	}
	return r, nil
}

func changes(old, now report) []string {
	var out []string
	if old.Mode == "fuzz" && (old.Seed != now.Seed || old.Cases != now.Cases || old.Operations != now.Operations) {
		out = append(out, fmt.Sprintf("fuzz inputs: seed=%d cases=%d ops=%d -> seed=%d cases=%d ops=%d",
			old.Seed, old.Cases, old.Operations, now.Seed, now.Cases, now.Operations))
	}
	if old.ContractCatalogSHA256 != now.ContractCatalogSHA256 {
		out = append(out, "canonical contract catalog changed")
	}
	if old.ToolchainsLockSHA256 != now.ToolchainsLockSHA256 {
		out = append(out, "toolchains.lock changed")
	}
	if old.Compiler != now.Compiler || old.SourceProfile != now.SourceProfile {
		out = append(out, fmt.Sprintf("compiler/profile: %s/%s -> %s/%s", old.Compiler, old.SourceProfile, now.Compiler, now.SourceProfile))
	}
	if old.Revision != now.Revision || old.Dirty != now.Dirty {
		out = append(out, fmt.Sprintf("revision/dirty: %s/%t -> %s/%t", old.Revision, old.Dirty, now.Revision, now.Dirty))
	}
	keys := make([]string, 0, len(now.Toolchains))
	for k := range now.Toolchains {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if old.Toolchains[k] != now.Toolchains[k] {
			out = append(out, fmt.Sprintf("%s: %s -> %s", k, old.Toolchains[k], now.Toolchains[k]))
		}
	}
	hostKeys := make([]string, 0, len(now.Host))
	for k := range now.Host {
		hostKeys = append(hostKeys, k)
	}
	sort.Strings(hostKeys)
	for _, k := range hostKeys {
		if old.Host[k] != now.Host[k] {
			out = append(out, fmt.Sprintf("host %s: %s -> %s", k, old.Host[k], now.Host[k]))
		}
	}
	prior := map[string]result{}
	for _, v := range old.Results {
		prior[v.Target] = v
	}
	for _, v := range now.Results {
		p, ok := prior[v.Target]
		if !ok {
			out = append(out, v.Target+": added")
			continue
		}
		if p.Status != v.Status || p.Cases != v.Cases || p.ContractCases != v.ContractCases {
			out = append(out, fmt.Sprintf("%s: %s/%d cases/%d contracts -> %s/%d cases/%d contracts", v.Target, p.Status, p.Cases, p.ContractCases, v.Status, v.Cases, v.ContractCases))
		}
		if p.TargetVersion != v.TargetVersion {
			out = append(out, fmt.Sprintf("%s target version: %s -> %s", v.Target, p.TargetVersion, v.TargetVersion))
		}
		if old.Mode != "fuzz" && p.BuildSHA256 != v.BuildSHA256 {
			out = append(out, v.Target+": generated build hash changed")
		}
		if p.ObservationSHA256 != v.ObservationSHA256 {
			out = append(out, v.Target+": observation hash changed")
		}
		if old.Mode == "baseline" && len(p.CompileMS) > 0 && len(v.CompileMS) > 0 && len(p.RunMS) > 0 && len(v.RunMS) > 0 {
			out = append(out, fmt.Sprintf("%s median compile/run ms: %d/%d -> %d/%d", v.Target,
				median(p.CompileMS), median(p.RunMS), median(v.CompileMS), median(v.RunMS)))
		}
	}
	current := map[string]bool{}
	for _, v := range now.Results {
		current[v.Target] = true
	}
	for _, v := range old.Results {
		if !current[v.Target] {
			out = append(out, v.Target+": removed")
		}
	}
	return out
}

func median(values []int64) int64 {
	copyOf := append([]int64{}, values...)
	sort.Slice(copyOf, func(i, j int) bool { return copyOf[i] < copyOf[j] })
	if len(copyOf)%2 == 0 {
		return (copyOf[len(copyOf)/2-1] + copyOf[len(copyOf)/2]) / 2
	}
	return copyOf[len(copyOf)/2]
}

func build(dir, target, out string) ([]diagnostics.Diagnostic, time.Duration) {
	start := time.Now()
	gate := subset.Sequential
	if src, err := os.ReadFile(filepath.Join(dir, "main.go")); err == nil {
		if value := testutil.ParseDirectives(string(src)).Gate; value != "" {
			gate = subset.Gate(value)
		}
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: dir, Gate: gate})
	if !diagnostics.HasErrors(ds) {
		ds = driver.Emit(target, res, out)
	}
	return ds, time.Since(start)
}

func treeHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		_, err = h.Write(data)
		return err
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func catalogHash(root string) (string, error) {
	types, err := treeHash(filepath.Join(root, "specs", "types"))
	if err != nil {
		return "", err
	}
	runtime, err := treeHash(filepath.Join(root, "specs", "runtime"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(types + runtime))
	return hex.EncodeToString(sum[:]), nil
}

type measurement struct {
	buildHash       string
	observationHash string
	compileMS       int64
	runMS           int64
}

func fixture(dir, target string, repeat bool, expected *testutil.Observation) (measurement, error) {
	var m measurement
	out, err := os.MkdirTemp("", "goalchemy-hardening-out-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(out)
	ds, compileTime := build(dir, target, out)
	m.compileMS = compileTime.Milliseconds()
	if len(ds) > 0 {
		return m, fmt.Errorf("compile: %v", ds)
	}
	m.buildHash, err = treeHash(out)
	if err != nil {
		return m, err
	}
	if repeat {
		other, err := os.MkdirTemp("", "goalchemy-hardening-repeat-")
		if err != nil {
			return m, err
		}
		defer os.RemoveAll(other)
		if ds, _ := build(dir, target, other); len(ds) > 0 {
			return m, fmt.Errorf("repeat compile: %v", ds)
		}
		second, err := treeHash(other)
		if err != nil || second != m.buildHash {
			return m, fmt.Errorf("non-repeatable build: %s != %s: %v", m.buildHash, second, err)
		}
	}
	started := time.Now()
	got, err := testutil.Runners[target](out)
	m.runMS = time.Since(started).Milliseconds()
	if err != nil {
		return m, err
	}
	var want testutil.Observation
	if expected == nil {
		want, err = testutil.Native(dir, out)
		if err != nil {
			return m, err
		}
	} else {
		want = *expected
	}
	got, want = testutil.Normalize(got), testutil.Normalize(want)
	m.observationHash = hashText(got.String())
	if got != want {
		return m, fmt.Errorf("observation mismatch: native %s target %s", want, got)
	}
	return m, nil
}

func native(dir string) (testutil.Observation, error) {
	work, err := os.MkdirTemp("", "goalchemy-hardening-native-")
	if err != nil {
		return testutil.Observation{}, err
	}
	defer os.RemoveAll(work)
	got, err := testutil.Native(dir, work)
	return testutil.Normalize(got), err
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func compatibility(root string, targets []string, deadline time.Time) []result {
	cat, ds := catalog.Load(os.DirFS(root))
	if len(ds) > 0 {
		fail(fmt.Errorf("catalog: %v", ds))
	}
	fixtures := []string{"tests/language/testdata/calls_results", "tests/language/testdata/slices_alias"}
	var results []result
	for _, target := range targets {
		r := result{Target: target, Status: "pass"}
		if time.Now().After(deadline) {
			r.Status, r.Failures = "budget_exhausted", []string{"budget expired before target"}
			results = append(results, r)
			continue
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		cases, err := conformance.RunContext(ctx, cat, root, target)
		cancel()
		if err != nil {
			r.Failures = append(r.Failures, "contracts: "+err.Error())
			if errors.Is(err, context.DeadlineExceeded) {
				r.Status = "budget_exhausted"
				results = append(results, r)
				continue
			}
		} else {
			for _, c := range cases {
				if c.Pass {
					r.ContractCases++
				} else {
					r.Failures = append(r.Failures, c.Case+": "+c.Detail)
				}
			}
		}
		for _, name := range fixtures {
			if time.Now().After(deadline) {
				r.Failures = append(r.Failures, "budget exhausted")
				r.Status = "budget_exhausted"
				break
			}
			m, err := fixture(filepath.Join(root, name), target, true, nil)
			r.Cases++
			r.CompileMS, r.RunMS = append(r.CompileMS, m.compileMS), append(r.RunMS, m.runMS)
			if name == fixtures[0] {
				r.BuildSHA256 = m.buildHash
			}
			r.ObservationSHA256 = hashText(r.ObservationSHA256 + m.observationHash)
			if err != nil {
				r.Failures = append(r.Failures, name+": "+err.Error())
			}
		}
		if len(r.Failures) > 0 && r.Status == "pass" {
			r.Status = "fail"
		}
		results = append(results, r)
	}
	return results
}

func baseline(root string, targets []string, samples int, deadline time.Time) []result {
	var results []result
	dir := filepath.Join(root, "examples/calc")
	for _, target := range targets {
		r := result{Target: target, Status: "pass"}
		for i := 0; i < samples; i++ {
			if time.Now().After(deadline) {
				r.Failures = append(r.Failures, "budget exhausted")
				r.Status = "budget_exhausted"
				break
			}
			m, err := fixture(dir, target, false, nil)
			r.Cases++
			r.CompileMS, r.RunMS = append(r.CompileMS, m.compileMS), append(r.RunMS, m.runMS)
			if i == 0 {
				r.BuildSHA256 = m.buildHash
			} else if m.buildHash != r.BuildSHA256 {
				r.Failures = append(r.Failures, "generated build hash changed between samples")
			}
			if i == 0 {
				r.ObservationSHA256 = m.observationHash
			} else if m.observationHash != r.ObservationSHA256 {
				r.Failures = append(r.Failures, "observation changed between samples")
			}
			if err != nil {
				r.Failures = append(r.Failures, err.Error())
			}
		}
		if len(r.Failures) > 0 && r.Status == "pass" {
			r.Status = "fail"
		}
		results = append(results, r)
	}
	return results
}

func fuzz(root string, targets []string, seed uint64, cases, ops int, deadline time.Time, output string) ([]result, string) {
	results := make([]result, len(targets))
	for i, t := range targets {
		results[i] = result{Target: t, Status: "pass"}
	}
	work, err := os.MkdirTemp(root, ".hardening-")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(work)
	for n := 0; n < cases && time.Now().Before(deadline); n++ {
		program := hardening.Generate(seed+uint64(n), ops)
		dir := filepath.Join(work, "case")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(program.Source()), 0o644); err != nil {
			fail(err)
		}
		want, err := native(dir)
		if err != nil {
			results[0].Status = "fail"
			results[0].Failures = append(results[0].Failures, fmt.Sprintf("seed %d native Go: %v", program.Seed, err))
			markUnrun(results, cases)
			return results, ""
		}
		for i, target := range targets {
			if time.Now().After(deadline) {
				for j := range results {
					if results[j].Cases < cases && results[j].Status == "pass" {
						results[j].Status = "budget_exhausted"
						results[j].Failures = append(results[j].Failures, "budget exhausted before all cases")
					}
				}
				return results, ""
			}
			m, err := fixture(dir, target, false, &want)
			r := &results[i]
			r.Cases++
			r.CompileMS, r.RunMS = append(r.CompileMS, m.compileMS), append(r.RunMS, m.runMS)
			r.ObservationSHA256 = hashText(r.ObservationSHA256 + m.observationHash)
			if err == nil {
				continue
			}
			r.Status = "fail"
			r.Failures = append(r.Failures, fmt.Sprintf("seed %d: %v", program.Seed, err))
			markUnrun(results, cases)
			// Only observation mismatches are reduced. A build or host failure
			// must remain visible as such, never turn into a corpus regression.
			if !strings.Contains(err.Error(), "observation mismatch") {
				return results, ""
			}
			check := func(candidate hardening.Program) bool {
				if time.Now().After(deadline) {
					return false
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(candidate.Source()), 0o644); err != nil {
					return false
				}
				want, e := native(dir)
				if e != nil {
					return false
				}
				_, e = fixture(dir, target, false, &want)
				return e != nil && strings.Contains(e.Error(), "observation mismatch")
			}
			reduced, _ := hardening.Reduce(program, check, 24)
			repro := output + ".repro"
			meta, _ := json.MarshalIndent(map[string]any{"schema_version": 1, "seed": program.Seed, "target": target,
				"compiler": link.CompilerVersion, "toolchain": frontend.ReferenceToolchain, "operations": reduced.Operations}, "", "  ")
			if e := os.MkdirAll(repro, 0o755); e != nil {
				r.Failures = append(r.Failures, "reproducer: "+e.Error())
				return results, ""
			}
			if e := os.WriteFile(filepath.Join(repro, "main.go"), []byte(reduced.Source()), 0o644); e != nil {
				r.Failures = append(r.Failures, "reproducer: "+e.Error())
				return results, ""
			}
			if e := os.WriteFile(filepath.Join(repro, "meta.json"), append(meta, '\n'), 0o644); e != nil {
				r.Failures = append(r.Failures, "reproducer: "+e.Error())
				return results, ""
			}
			return results, repro
		}
	}
	for i := range results {
		if results[i].Cases < cases && results[i].Status == "pass" {
			results[i].Status = "budget_exhausted"
			results[i].Failures = append(results[i].Failures, "budget exhausted before all cases")
		}
	}
	return results, ""
}

func markUnrun(results []result, cases int) {
	for i := range results {
		if results[i].Status == "pass" && results[i].Cases < cases {
			results[i].Status = "not_run"
			results[i].Failures = append(results[i].Failures, "campaign stopped after another target failed")
		}
	}
}
