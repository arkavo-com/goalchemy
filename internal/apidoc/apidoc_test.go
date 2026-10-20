package apidoc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/link"
)

// This fixture exposes handles and context; embedded profiles deliberately
// reject that ABI and must remain explicit unavailable entries in the catalog.
var libraryFixtureTargets = []string{"c", "csharp", "go", "java", "python", "rust", "swift", "typescript"}

func fixture(t *testing.T) *Catalog {
	t.Helper()
	c, ds, err := Generate(context.Background(), driver.Options{Dir: "testdata/library"}, "Fixture SDK", nil)
	if err != nil || diagnostics.HasErrors(ds) {
		t.Fatalf("generate: %v %#v", err, ds)
	}
	return c
}
func find(c *Catalog, suffix string) *Symbol {
	for _, s := range c.Symbols {
		if strings.HasSuffix(s.ID, "#"+suffix) {
			return s
		}
	}
	return nil
}
func TestCatalogAndNativeBoundaries(t *testing.T) {
	c := fixture(t)
	if len(c.Targets) != len(driver.Targets()) {
		t.Fatalf("targets: %v", c.Targets)
	}
	for _, target := range c.Targets {
		if !slices.Contains(libraryFixtureTargets, target.Name) {
			if target.Status != "unavailable" || target.Reason == "" {
				t.Errorf("restricted library target must explain its unavailable status: %+v", target)
			}
			for _, symbol := range c.Symbols {
				for _, binding := range symbol.Bindings {
					if binding.Target == target.Name {
						t.Errorf("unavailable target %s has binding for %s", target.Name, symbol.ID)
					}
				}
			}
			continue
		}
		if target.Status != "available" {
			t.Errorf("%s: %s", target.Name, target.Reason)
		}
	}
	item, next, echo, alias := find(c, "Item"), find(c, "Client.Next"), find(c, "Echo"), find(c, "Alias")
	if item == nil || next == nil || echo == nil || alias == nil {
		t.Fatal("missing source exports")
	}
	if next.Parent != find(c, "Client").ID || next.Kind != "method" {
		t.Fatal("method identity lost")
	}
	if !strings.HasPrefix(next.Signature, "func (c *Client) Next(") {
		t.Fatalf("invalid Go receiver declaration: %s", next.Signature)
	}
	if method := find(c, "Reader.Read"); method == nil || method.Parent != find(c, "Reader").ID || !strings.Contains(method.Description, "accepts an Item") {
		t.Fatal("interface method identity or documentation lost")
	}
	if len(item.Fields) != 2 || item.Fields[0].Tag != "json:\"name\"" || !strings.Contains(item.Fields[0].Description, "Literal HTML") {
		t.Fatal("field docs lost")
	}
	if echo.Deprecated != "use Client.Next instead." || alias.Kind != "alias" || len(alias.References) != 1 {
		t.Fatal("deprecation or alias lost")
	}
	for _, s := range []*Symbol{next, echo} {
		seen := map[string]bool{}
		for _, b := range s.Bindings {
			seen[b.Target] = true
			if b.Target == "typescript" && !strings.Contains(b.Signature, "Promise<") {
				t.Fatal("TypeScript projection is not native")
			}
			if b.Target == "swift" && !strings.Contains(b.Signature, "Operation<") {
				t.Fatal("Swift projection is not native")
			}
			if b.Target == "c" && b.Invocation == "" {
				t.Fatal("missing C call selector")
			}
		}
		if len(seen) != len(libraryFixtureTargets) {
			t.Fatalf("%s target bindings: %v", s.ID, seen)
		}
	}
	pages := c.Markdown()
	assertLinks(t, pages)
	if len(item.Bindings) != len(libraryFixtureTargets)-1 {
		t.Fatalf("expected native value representations outside C's generic interface: %v", item.Bindings)
	}
	if bytes.Contains(bytes.Join(mapValues(pages), nil), []byte("such as <script>")) {
		t.Fatal("source comment HTML was not escaped")
	}
	for file, body := range pages {
		if strings.Contains(file, "echo-") && strings.Contains(string(body), "pkg.go.dev/") {
			t.Fatal("local doc references were not linked locally")
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var restored Catalog
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.Check(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.Markdown()["index.md"], pages["index.md"]) {
		t.Fatal("catalog roundtrip changed markdown")
	}
}

func mapValues(files map[string][]byte) [][]byte {
	var values [][]byte
	for _, data := range files {
		values = append(values, data)
	}
	return values
}

func TestUnavailableTargetIsExplicit(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"go.mod": "module example.com/map-sdk\n\ngo 1.25\n",
		"api.go": "package sdk\n// Echo returns its input.\nfunc Echo(v map[string]string) map[string]string { return v }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, ds, err := Generate(context.Background(), driver.Options{Dir: dir}, "Map SDK", []string{"c"})
	if err != nil || diagnostics.HasErrors(ds) {
		t.Fatalf("source documentation failed: %v %v", err, ds)
	}
	if len(c.Targets) != 1 || c.Targets[0].Status != "unavailable" || c.Targets[0].Reason == "" || len(find(c, "Echo").Bindings) != 0 {
		t.Fatalf("unsupported boundary was silently documented: %+v", c.Targets)
	}
}

func TestObsoletePagesAreRemovedSafely(t *testing.T) {
	c := &Catalog{SchemaVersion: 1, Title: "SDK", Packages: []Package{{ID: "example/sdk", Name: "sdk", Root: true, Symbols: []string{"example/sdk#Count"}}}, Symbols: []*Symbol{{ID: "example/sdk#Count", Package: "example/sdk", Name: "Count", Kind: "constant", Signature: "const Count = 1", Root: true}}}
	out := t.TempDir()
	if err := c.Write(out, "markdown"); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(out, newRenderer(c).files[c.Symbols[0].ID])
	c.Symbols = nil
	c.Packages[0].Symbols = nil
	if err := c.Write(out, "markdown"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("stale symbol page remains")
	}
}
func assertLinks(t *testing.T, pages map[string][]byte) {
	t.Helper()
	re := regexp.MustCompile(`\]\(([^)]+)\)`)
	for file, body := range pages {
		for _, m := range re.FindAllSubmatch(body, -1) {
			target := string(m[1])
			if strings.Contains(target, ":") || strings.HasPrefix(target, "#") {
				continue
			}
			target = filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(file), target)))
			if _, ok := pages[target]; !ok {
				t.Errorf("%s: broken link %s", file, m[1])
			}
		}
	}
}
func TestSafeRegeneration(t *testing.T) {
	c := &Catalog{SchemaVersion: 1, Title: "Tiny SDK", Compiler: link.CompilerVersion, Packages: []Package{}, Symbols: []*Symbol{}, Targets: []Target{}}
	out := t.TempDir()
	if err := c.Write(out, "site"); err != nil {
		t.Fatal(err)
	}
	custom := []byte("// customized config\n")
	config := filepath.Join(out, "astro.config.mjs")
	if err := os.WriteFile(config, custom, 0644); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(out, "notes.txt")
	os.WriteFile(unrelated, []byte("keep"), 0644)
	if err := c.Write(out, "site"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(config)
	if !bytes.Equal(got, custom) {
		t.Fatal("scaffold overwritten")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(out, "src/content/docs/api/index.md")
	os.WriteFile(api, []byte("manual edit"), 0644)
	if err := c.Write(out, "site"); err == nil {
		t.Fatal("edited reference overwritten")
	}
	if err := c.Write(out, "json"); err == nil {
		t.Fatal("format changed in owned directory")
	}
	fresh := t.TempDir()
	os.WriteFile(filepath.Join(fresh, "api.json"), []byte("unrelated"), 0644)
	if err := c.Write(fresh, "json"); err == nil {
		t.Fatal("unrelated API file overwritten")
	}
	outside := t.TempDir()
	symlink := t.TempDir()
	os.Symlink(outside, filepath.Join(symlink, "public"))
	if err := c.Write(symlink, "site"); err == nil {
		t.Fatal("symlink traversed")
	}
}
func TestObserverPreservesGeneratedSource(t *testing.T) {
	res, ds := driver.Build(context.Background(), driver.Options{Dir: "testdata/library"})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	for _, target := range libraryFixtureTargets {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
			var declarations []artifact.APIDeclaration
			for _, dir := range []string{first, second} {
				opts := driver.EmitOptions{}
				if dir == second {
					opts.ObserveAPI = func(api []artifact.APIDeclaration) { declarations = api }
				}
				if ds := driver.EmitWithOptions(target, res, dir, opts); diagnostics.HasErrors(ds) {
					t.Fatal(ds)
				}
			}
			if len(declarations) == 0 {
				t.Fatal("missing API declarations")
			}
			var source bytes.Buffer
			err := filepath.WalkDir(first, func(file string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(first, file)
				a, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				b, err := os.ReadFile(filepath.Join(second, rel))
				if err != nil {
					return err
				}
				if !bytes.Equal(a, b) {
					t.Errorf("observer changed %s", rel)
				}
				source.Write(a)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
			generated := compact(source.String())
			for _, d := range declarations {
				if d.Function != nil && !strings.Contains(generated, compact(d.Signature)) {
					t.Errorf("API signature is not emitted: %s", d.Signature)
				}
			}
		})
	}
}
