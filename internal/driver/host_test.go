package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostNames(t *testing.T) {
	dir := t.TempDir()
	services := `package generated

import (
	"log"
	stdlog "log/slog"
	_ "embed"
	. "strings"
	"gopkg.in/yaml.v3"
	"github.com/example/kit/v2"
)

type attributesServiceClient struct{}

type (
	Alpha int
	beta  int
)

var count, _ = 1, 2

const limit = 3

func New() {}

func (attributesServiceClient) Method() {}

func init() { log.Println(stdlog.LevelInfo, ToUpper(""), yaml.Node{}, kit.X) }
`
	if err := os.WriteFile(filepath.Join(dir, "services.go"), []byte(services), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	single := filepath.Join(t.TempDir(), "options.go")
	if err := os.WriteFile(single, []byte("package generated\n\nimport \"context\"\n\nvar log = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := HostNames([]string{dir, single})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range names {
		got = append(got, n.Name)
	}
	want := "log stdlog yaml kit attributesServiceClient Alpha beta count limit New init context"
	if strings.Join(got, " ") != want {
		t.Fatalf("host names = %q, want %q", strings.Join(got, " "), want)
	}
	if names[0].Pos != filepath.Join(dir, "services.go")+":4:2" {
		t.Fatalf("log position = %s", names[0].Pos)
	}
	if names[4].Pos != filepath.Join(dir, "services.go")+":12:6" {
		t.Fatalf("type position = %s", names[4].Pos)
	}
	if _, err := HostNames([]string{filepath.Join(dir, "missing.go")}); err == nil {
		t.Fatal("missing host file accepted")
	}
}

func TestImportName(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"log", "log"}, {"log/slog", "slog"}, {"gopkg.in/yaml.v3", "yaml"},
		{"github.com/example/kit/v2", "kit"}, {"v2", "v2"}, {"github.com/example/go-cmp", ""},
	} {
		if got := importName(tc.path); got != tc.want {
			t.Errorf("importName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
