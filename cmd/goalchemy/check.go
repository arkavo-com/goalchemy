package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

type sourceFlags struct {
	json bool
	tags string
	gate string
	dir  string
}

func (s *sourceFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&s.json, "json", false, "write diagnostics as JSON")
	fs.StringVar(&s.tags, "tags", "", "comma-separated build tags")
	fs.StringVar(&s.gate, "gate", string(subset.Sequential), "language gate: sequential or cooperative")
	fs.StringVar(&s.dir, "C", "", "directory to load packages from")
}

func (s *sourceFlags) tagList() []string {
	var out []string
	for _, t := range strings.Split(s.tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// loadChecked loads packages and validates them against the gate.
func loadChecked(ctx context.Context, sf *sourceFlags, patterns []string) (*frontend.Program, *catalog.Registry, []diagnostics.Diagnostic) {
	cat, cds := catalog.Load(catalog.FS())
	if diagnostics.HasErrors(cds) {
		return nil, nil, cds
	}
	reg := catalog.NewRegistry(cat)
	prog, ds := frontend.Load(ctx, frontend.Options{Dir: sf.dir, Patterns: patterns, Tags: sf.tagList(), External: reg.Package, SourceLib: catalog.IsStd})
	gate := subset.Gate(sf.gate)
	if gate != subset.Sequential && gate != subset.Cooperative {
		ds = append(ds, diagnostics.Diagnostic{Code: "GCL006", Severity: diagnostics.Error,
			Message: "unknown gate " + sf.gate, Remedy: "Use -gate sequential or -gate cooperative."})
		return prog, reg, ds
	}
	if len(prog.Source) > 0 {
		ds = append(ds, subset.Check(prog, subset.Options{Gate: gate, External: reg.External, ExternalPackage: reg.Package})...)
	}
	diagnostics.Sort(ds)
	return prog, reg, diagnostics.Dedup(ds)
}

func runCheck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sf sourceFlags
	sf.register(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_, _, ds := loadChecked(ctx, &sf, fs.Args())
	if sf.json {
		_ = diagnostics.Write(stdout, ds, true)
	} else {
		_ = diagnostics.Write(stderr, ds, false)
		if !diagnostics.HasErrors(ds) {
			fmt.Fprintf(stdout, "ok: accepted by the %s gate\n", sf.gate)
		}
	}
	if diagnostics.HasErrors(ds) {
		return 1
	}
	return 0
}

func writeWarnings(w io.Writer, ds []diagnostics.Diagnostic, asJSON bool) {
	if len(ds) > 0 {
		_ = diagnostics.Write(w, ds, asJSON)
	}
}
