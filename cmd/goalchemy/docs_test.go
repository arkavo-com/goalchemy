package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/apidoc"
)

func TestDocsCommandAndConfig(t *testing.T) {
	fixture, err := filepath.Abs("../../internal/apidoc/testdata/library")
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "goalchemy.yaml")
	if err := os.WriteFile(config, []byte("schema_version: 1\npackages: [.]\ntargets:\n  typescript: {out: ignored}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "markdown", "site"} {
		t.Run(format, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "reference")
			var stdout, stderr bytes.Buffer
			args := []string{"docs", "-config", config, "-C", fixture, "-target", "go", "-format", format, "-out", out, "-title", "Consumer SDK"}
			if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, &stderr)
			}
			catalogFile := "api.json"
			if format == "site" {
				catalogFile = "public/api.json"
			}
			raw, err := os.ReadFile(filepath.Join(out, catalogFile))
			if err != nil {
				t.Fatal(err)
			}
			var catalog apidoc.Catalog
			if err := json.Unmarshal(raw, &catalog); err != nil {
				t.Fatal(err)
			}
			if catalog.Title != "Consumer SDK" || len(catalog.Symbols) == 0 || len(catalog.Targets) != 1 || catalog.Targets[0].Name != "go" {
				t.Fatal("explicit flags did not override project configuration")
			}
			if err := catalog.Check(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDocsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"docs"},
		{"docs", "-out", t.TempDir(), "-format", "html"},
		{"docs", "-out", t.TempDir(), "-proto-services", "example.Service"},
		{"docs", "-out", t.TempDir(), "-proto-descriptor", "services.binpb", "-target", "go"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
			t.Fatalf("%v: exit %d, stderr %s", args, code, &stderr)
		}
	}
}

func TestDocsRPCWithoutGoSource(t *testing.T) {
	fixture, err := filepath.Abs("../../internal/protoc/testdata")
	if err != nil {
		t.Fatal(err)
	}
	content, err := filepath.Abs("../../internal/apidoc/testdata/rpc-content.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "markdown", "site"} {
		t.Run(format, func(t *testing.T) {
			out := t.TempDir()
			var stdout, stderr bytes.Buffer
			args := []string{"docs", "-C", fixture, "-proto-descriptor", "platform.binpb", "-proto-services", "policy.attributes.AttributesService", "-content", content, "-format", format, "-out", out}
			if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, &stderr)
			}
			catalogFile := "api.json"
			if format == "site" {
				catalogFile = "public/api.json"
			}
			raw, err := os.ReadFile(filepath.Join(out, catalogFile))
			if err != nil {
				t.Fatal(err)
			}
			var catalog apidoc.Catalog
			if err := json.Unmarshal(raw, &catalog); err != nil {
				t.Fatal(err)
			}
			if len(catalog.Symbols) != 0 || catalog.RPC == nil || len(catalog.RPC.Services) != 1 || len(catalog.RPC.Services[0].Methods) != 21 || len(catalog.Examples) != 1 || len(catalog.Examples[0].Snippets) != 8 {
				t.Fatal("RPC-only documentation lost services or native examples")
			}
			if err := catalog.Check(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
