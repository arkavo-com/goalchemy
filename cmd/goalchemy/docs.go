package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/apidoc"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/project"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

func runDocs(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("docs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sf sourceFlags
	sf.register(fs)
	format := fs.String("format", "json", "documentation output: json, markdown, or site")
	out := fs.String("out", "", "output directory")
	title := fs.String("title", "SDK API", "documentation title")
	config := fs.String("config", "", "optional goalchemy.yaml project configuration")
	target := fs.String("target", "", "comma-separated targets; default: all native targets or configured targets")
	descriptor := fs.String("proto-descriptor", "", "protobuf FileDescriptorSet including imports and source info")
	services := fs.String("proto-services", "", "comma-separated fully qualified services; default: all descriptor services")
	content := fs.String("content", "", "JSON usage examples and native RPC bindings")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "docs: -out is required")
		return 2
	}
	if *format != "json" && *format != "markdown" && *format != "site" {
		fmt.Fprintln(stderr, "docs: -format must be json, markdown, or site")
		return 2
	}
	if *services != "" && *descriptor == "" {
		fmt.Fprintln(stderr, "docs: -proto-services requires -proto-descriptor")
		return 2
	}
	opts := driver.Options{Dir: sf.dir, Patterns: fs.Args(), Tags: sf.tagList(), Gate: subset.Gate(sf.gate)}
	var targets []string
	if *config != "" {
		cfg, err := project.Load(*config)
		if err != nil {
			fmt.Fprintln(stderr, "docs:", err)
			return 2
		}
		opts = driver.Options{Dir: cfg.Dir, Patterns: cfg.Packages, Tags: cfg.Tags, Gate: subset.Gate(cfg.Gate)}
		targets = cfg.TargetNames()
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "gate":
				opts.Gate = subset.Gate(sf.gate)
			case "tags":
				opts.Tags = sf.tagList()
			case "C":
				opts.Dir = sf.dir
			}
		})
		if len(fs.Args()) > 0 {
			fmt.Fprintln(stderr, "docs: package arguments cannot be combined with -config")
			return 2
		}
	}
	if *target != "" {
		if *descriptor != "" && *config == "" && len(opts.Patterns) == 0 {
			fmt.Fprintln(stderr, "docs: -target selects Go library projections; supply source packages as well as -proto-descriptor")
			return 2
		}
		targets = strings.Split(*target, ",")
		for i := range targets {
			targets[i] = strings.TrimSpace(targets[i])
		}
	}
	catalog := &apidoc.Catalog{SchemaVersion: 1, Compiler: link.CompilerVersion, Title: *title, Packages: []apidoc.Package{}, Symbols: []*apidoc.Symbol{}, Targets: []apidoc.Target{}}
	var ds []diagnostics.Diagnostic
	var err error
	if *descriptor == "" || *config != "" || len(opts.Patterns) > 0 {
		catalog, ds, err = apidoc.Generate(ctx, opts, *title, targets)
	}
	if len(ds) > 0 {
		_ = diagnostics.Write(stderr, ds, sf.json)
	}
	if diagnostics.HasErrors(ds) {
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "docs:", err)
		return 1
	}
	read := func(name string) ([]byte, error) {
		if !filepath.IsAbs(name) {
			name = filepath.Join(opts.Dir, name)
		}
		return os.ReadFile(name)
	}
	if *descriptor != "" {
		data, readErr := read(*descriptor)
		if readErr != nil {
			fmt.Fprintln(stderr, "docs:", readErr)
			return 1
		}
		var selected []string
		if *services != "" {
			for _, name := range strings.Split(*services, ",") {
				selected = append(selected, strings.TrimSpace(name))
			}
		}
		if err := catalog.AddDescriptors(data, selected); err != nil {
			fmt.Fprintln(stderr, "docs:", err)
			return 1
		}
	}
	if *content != "" {
		data, readErr := read(*content)
		if readErr != nil {
			fmt.Fprintln(stderr, "docs:", readErr)
			return 1
		}
		if err := catalog.AddContent(data); err != nil {
			fmt.Fprintln(stderr, "docs:", err)
			return 1
		}
	}
	if err := catalog.Write(*out, *format); err != nil {
		fmt.Fprintln(stderr, "docs:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s documentation for %d symbols to %s\n", *format, len(catalog.Symbols), *out)
	if catalog.RPC != nil {
		methods := 0
		for _, service := range catalog.RPC.Services {
			methods += len(service.Methods)
		}
		fmt.Fprintf(stdout, "included %d RPC services, %d methods, %d messages, and %d examples\n", len(catalog.RPC.Services), methods, len(catalog.RPC.Messages), len(catalog.Examples))
	}
	for _, target := range catalog.Targets {
		if target.Status != "available" {
			fmt.Fprintf(stderr, "docs: %s is unavailable: %s\n", target.Name, target.Reason)
		}
	}
	return 0
}
