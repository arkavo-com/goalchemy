package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostFilesConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	data := "schema_version: 1\npackages: [.]\ntargets:\n  go: {out: out/go, host: [src/hosts/go, adapters.go, /abs/host.go]}\n  rust: {out: out/rs}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "src/hosts/go"), filepath.Join(dir, "adapters.go"), "/abs/host.go"}
	if got := c.HostFiles("go"); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("host files = %v, want %v", got, want)
	}
	if got := c.HostFiles("rust"); len(got) != 0 {
		t.Fatalf("rust host files = %v", got)
	}
	data = "schema_version: 1\npackages: [.]\ntargets:\n  rust: {out: out/rs, host: [a.go]}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "only supported for the go target") {
		t.Fatalf("accepted host files on rust: %v", err)
	}
}
