package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/apidoc"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
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
		targets = strings.Split(*target, ",")
		for i := range targets {
			targets[i] = strings.TrimSpace(targets[i])
		}
	}
	catalog, ds, err := apidoc.Generate(ctx, opts, *title, targets)
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
	if err := catalog.Write(*out, *format); err != nil {
		fmt.Fprintln(stderr, "docs:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s documentation for %d symbols to %s\n", *format, len(catalog.Symbols), *out)
	for _, target := range catalog.Targets {
		if target.Status != "available" {
			fmt.Fprintf(stderr, "docs: %s is unavailable: %s\n", target.Name, target.Reason)
		}
	}
	return 0
}
