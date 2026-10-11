package subset_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

var wantRE = regexp.MustCompile(`// want(:-?\d+)? ((?:GC[A-Z]\d{3} ?)+)`)

func expectations(t *testing.T, dir string) []string {
	var out []string
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		line := 0
		for sc.Scan() {
			line++
			m := wantRE.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			l := line
			if m[1] != "" {
				d, _ := strconv.Atoi(m[1][1:])
				l += d
			}
			for _, code := range strings.Fields(m[2]) {
				out = append(out, fmt.Sprintf("%s:%d:%s", filepath.Base(f), l, code))
			}
		}
		fh.Close()
	}
	sort.Strings(out)
	return out
}

func check(t *testing.T, name string) []string {
	t.Helper()
	cat, cds := catalog.Load(catalog.FS())
	if len(cds) > 0 {
		t.Fatalf("catalog: %v", cds)
	}
	reg := catalog.NewRegistry(cat)
	reg.Symbols["errors.New"] = "test.errors.new"
	reg.Packages["errors"] = true
	prog, ds := frontend.Load(context.Background(), frontend.Options{Dir: filepath.Join("testdata", name), External: reg.Package})
	ds = append(ds, subset.Check(prog, subset.Options{External: reg.External, ExternalPackage: reg.Package})...)
	var got []string
	for _, d := range ds {
		got = append(got, fmt.Sprintf("%s:%d:%s", filepath.Base(d.File), d.Line, d.Code))
		if d.Remedy == "" || d.Message == "" {
			t.Errorf("diagnostic without message or remedy: %+v", d)
		}
	}
	sort.Strings(got)
	return got
}

func TestFixtures(t *testing.T) {
	for _, name := range []string{"accept", "reject_types", "reject_stmts", "reject_imports", "reject_generic", "reject_elem", "reject_json", "library_globals", "executable_globals"} {
		t.Run(name, func(t *testing.T) {
			want := expectations(t, filepath.Join("testdata", name))
			got := check(t, name)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("diagnostics mismatch\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

func TestNewerLanguageVersion(t *testing.T) {
	got := check(t, "newer")
	if len(got) == 0 || !strings.Contains(strings.Join(got, " "), "GCL004") {
		t.Fatalf("expected GCL004, got %v", got)
	}
}

func TestCooperativeGateAllowsGo(t *testing.T) {
	prog, ds := frontend.Load(context.Background(), frontend.Options{Dir: filepath.Join("testdata", "reject_stmts")})
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	for _, d := range subset.Check(prog, subset.Options{Gate: subset.Cooperative}) {
		if d.Code == "GCS011" {
			t.Errorf("cooperative gate reported %v", d)
		}
	}
}
