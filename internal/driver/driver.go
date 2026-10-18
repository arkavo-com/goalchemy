// Package driver runs the compiler pipeline: load, validate, lower, emit.
package driver

import (
	"context"

	"github.com/eugenioenko/goalchemy/internal/catalog"
	"github.com/eugenioenko/goalchemy/internal/contracts"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/frontend"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/lower"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

type Options struct {
	Dir      string
	Patterns []string
	Tags     []string
	Gate     subset.Gate
}

type Result struct {
	// ObserveAPI receives native public declarations during documentation generation.
	ObserveAPI func([]artifact.APIDeclaration)
	Catalog    *contracts.Catalog
	Registry   *catalog.Registry
	Program    *frontend.Program
	IR         *ir.Program
}

// Build loads and validates the source and lowers it to IR.
func Build(ctx context.Context, opts Options) (*Result, []diagnostics.Diagnostic) {
	cat, ds := catalog.Load(catalog.FS())
	if diagnostics.HasErrors(ds) {
		return nil, ds
	}
	reg := catalog.NewRegistry(cat)
	prog, ds := frontend.Load(ctx, frontend.Options{Dir: opts.Dir, Patterns: opts.Patterns, Tags: opts.Tags, External: reg.Package, SourceLib: catalog.IsStd})
	res := &Result{Catalog: cat, Registry: reg, Program: prog}
	if opts.Gate == "" {
		opts.Gate = subset.Sequential
	}
	if len(prog.Source) > 0 {
		ds = append(ds, subset.Check(prog, subset.Options{Gate: opts.Gate, External: reg.External, ExternalPackage: reg.Package})...)
	}
	if diagnostics.HasErrors(ds) {
		diagnostics.Sort(ds)
		return res, diagnostics.Dedup(ds)
	}
	p, lds := lower.Lower(prog, reg)
	res.IR = p
	ds = append(ds, lds...)
	if p.Main == nil && !p.Library {
		ds = append(ds, diagnostics.Diagnostic{Code: "GCI003", Severity: diagnostics.Error,
			Feature: "entry point", Message: "no main function in package main",
			Remedy: "Compile a main package."})
	}
	diagnostics.Sort(ds)
	return res, ds
}
