// Package memory checks the Rust target's traced heap: cyclic garbage is
// collected, long-lived structures survive collections intact, and interior
// references (field pointers, subslices, closures, interfaces, deferred
// calls, values in flight between tasks) keep their objects alive.
package memory

import (
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/testutil"
)

var stats = regexp.MustCompile(`heap: peak (\d+) live (\d+) collections (\d+)\n`)

func TestRustHeap(t *testing.T) { testRustHeap(t, "rust") }

func TestRustNoStdHeap(t *testing.T) { testRustHeap(t, "rust-nostd") }

func testRustHeap(t *testing.T, target string) {
	t.Helper()
	if testing.Short() {
		t.Skip("heap stress tests run in the full suite")
	}
	cases := []struct {
		name      string
		gate      string
		threshold string
		maxPeak   int
	}{
		{"cycles", "", "", 100000},
		{"retained", "", "", 120000},
		{"interior", "", "1000", 0},
		{"tasks", "cooperative", "1000", 0},
	}
	for _, c := range cases {
		if target == "rust-nostd" && c.gate == "cooperative" {
			continue
		}
		if target == "rust-nostd" && c.name == "cycles" {
			c.maxPeak = 512
		}
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join("testdata", c.name)
			want, err := testutil.Native(dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			if ds := testutil.CompileGate(dir, target, out, c.gate); len(ds) > 0 {
				t.Fatal(ds)
			}
			t.Setenv("GOALCHEMY_HEAP_STATS", "1")
			t.Setenv("GOALCHEMY_GC_THRESHOLD", c.threshold)
			got, err := testutil.Runners[target](out)
			if err != nil {
				t.Fatal(err)
			}
			m := stats.FindStringSubmatch(got.Stderr)
			if m == nil {
				t.Fatalf("no heap statistics:\n%s", got)
			}
			got.Stderr = stats.ReplaceAllString(got.Stderr, "")
			if got.String() != want.String() {
				t.Fatalf("rust differs from native Go\n=== native\n%s=== rust\n%s", want, got)
			}
			peak, _ := strconv.Atoi(m[1])
			collections, _ := strconv.Atoi(m[3])
			if collections == 0 {
				t.Errorf("no collections ran (peak %d)", peak)
			}
			if c.maxPeak > 0 && peak > c.maxPeak {
				t.Errorf("peak live objects %d exceeds %d", peak, c.maxPeak)
			}
			t.Logf("peak %s live %s collections %s", m[1], m[2], m[3])
		})
	}
}

var cStats = regexp.MustCompile(`heap: size (\d+) collections (\d+)\n`)

// TestCHeap runs the same programs on the C target, whose Boehm collector
// must keep the heap bounded while retaining every live object.
func TestCHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("heap stress tests run in the full suite")
	}
	for _, c := range []struct{ name, gate string }{
		{"cycles", ""}, {"retained", ""}, {"interior", ""}, {"tasks", "cooperative"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join("testdata", c.name)
			want, err := testutil.Native(dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			if ds := testutil.CompileGate(dir, "c", out, c.gate); len(ds) > 0 {
				t.Fatal(ds)
			}
			t.Setenv("GOALCHEMY_HEAP_STATS", "1")
			got, err := testutil.Runners["c"](out)
			if err != nil {
				t.Fatal(err)
			}
			m := cStats.FindStringSubmatch(got.Stderr)
			if m == nil {
				t.Fatalf("no heap statistics:\n%s", got)
			}
			got.Stderr = cStats.ReplaceAllString(got.Stderr, "")
			if got.String() != want.String() {
				t.Fatalf("c differs from native Go\n=== native\n%s=== c\n%s", want, got)
			}
			size, _ := strconv.Atoi(m[1])
			collections, _ := strconv.Atoi(m[2])
			if collections == 0 {
				t.Errorf("no collections ran (heap %d bytes)", size)
			}
			if size > 64<<20 {
				t.Errorf("heap grew to %d bytes", size)
			}
			t.Logf("heap %s bytes, %s collections", m[1], m[2])
		})
	}
}
