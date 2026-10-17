package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

func runCompile(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sf sourceFlags
	sf.register(fs)
	target := fs.String("target", "go", "output target: "+targetNames()+", or ir")
	out := fs.String("out", "", "output directory")
	compactNames := fs.Bool("compact-names", false, "use short private generated identifiers")
	pauseModel := fs.String("pause-model", "frames", "suspending function form: frames or native")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	res, ds := driver.Build(ctx, driver.Options{Dir: sf.dir, Patterns: fs.Args(), Tags: sf.tagList(), Gate: subset.Gate(sf.gate)})
	if diagnostics.HasErrors(ds) {
		_ = diagnostics.Write(stderr, ds, sf.json)
		return 1
	}
	writeWarnings(stderr, ds, sf.json)
	if *target == "ir" {
		ir.Dump(stdout, res.IR)
		return 0
	}
	if *out == "" {
		fmt.Fprintln(stderr, "compile: -out is required")
		return 2
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	eds := driver.EmitWithOptions(*target, res, *out, driver.EmitOptions{CompactNames: *compactNames, PauseModel: *pauseModel})
	if len(eds) > 0 {
		_ = diagnostics.Write(stderr, eds, sf.json)
		return 1
	}
	return 0
}
