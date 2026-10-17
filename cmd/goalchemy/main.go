// Command goalchemy validates and transpiles Goalchemy programs.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/eugenioenko/goalchemy/internal/link"
)

const Version = link.CompilerVersion

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string, stdout, stderr io.Writer) int
}

var commands []command

func init() {
	commands = []command{
		{"check", "load, type-check, and validate packages against the language gate", runCheck},
		{"build", "compile every target listed in goalchemy.yaml", runBuild},
		{"run", "compile to a temporary directory and run: run -target <name> [packages]", runRun},
		{"compile", "compile a program: compile -target <name> -out <dir> [packages]", runCompile},
		{"spec", "validate or generate contract specifications (spec validate | spec generate)", runSpec},
		{"docs", "generate SDK API documentation: docs -format json|markdown|site -out <dir> [packages]", runDocs},
		{"features", "list supported language features and targets", runFeatures},
		{"test", "run runtime contract cases through target harnesses", runTest},
		{"version", "print the compiler version", func(_ context.Context, _ []string, stdout, _ io.Writer) int {
			fmt.Fprintln(stdout, "goalchemy", Version)
			return 0
		}},
	}
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stderr)
		return 2
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(ctx, args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "goalchemy: unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: goalchemy <command> [arguments]")
	fmt.Fprintln(w)
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
}
