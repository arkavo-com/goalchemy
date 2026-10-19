package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/project"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

func runBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", project.FileName, "project configuration file")
	asJSON := fs.Bool("json", false, "write diagnostics as JSON")
	nf := registerNameFlags(fs)
	host := registerHostFlag(fs)
	pauseModel := fs.String("pause-model", "frames", "override pause_model in the project configuration: frames or native")
	lineDirectives := fs.Bool("line-directives", true, "override line_directives in the project configuration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := project.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "goalchemy build:", err)
		return 2
	}
	names, collisions, err := nf.resolve(fs, cfg.NameMode(), cfg.NameCollisions)
	if err != nil {
		fmt.Fprintln(stderr, "goalchemy build:", err)
		return 2
	}
	hostFiles := cfg.HostFiles("go")
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pause-model" {
			cfg.PauseModel = *pauseModel
		}
		if f.Name == "line-directives" {
			cfg.LineDirectives = lineDirectives
		}
		if f.Name == "go-host" {
			hostFiles = *host
		}
	})
	res, ds := driver.Build(ctx, driver.Options{Dir: cfg.Dir, Patterns: cfg.Packages, Tags: cfg.Tags, Gate: subset.Gate(cfg.Gate)})
	if diagnostics.HasErrors(ds) {
		_ = diagnostics.Write(stderr, ds, *asJSON)
		return 1
	}
	writeWarnings(stderr, ds, *asJSON)
	for _, t := range cfg.TargetNames() {
		out := cfg.OutDir(t)
		if err := os.MkdirAll(out, 0o755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		opts := driver.EmitOptions{Names: names, NameCollisions: collisions, PauseModel: cfg.PauseModel, NoLineDirectives: cfg.LineDirectives != nil && !*cfg.LineDirectives}
		if t == "go" {
			opts.Host = hostFiles
		}
		if eds := driver.EmitWithOptions(t, res, out, opts); len(eds) > 0 {
			_ = diagnostics.Write(stderr, eds, *asJSON)
			return 1
		}
		fmt.Fprintf(stdout, "%s: wrote %s\n", t, out)
	}
	return 0
}

// runRun compiles a program to a temporary directory and runs it.
func runRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sf sourceFlags
	sf.register(fs)
	target := fs.String("target", "typescript", "target to run")
	keep := fs.Bool("keep", false, "keep the generated directory and print its path")
	nf := registerNameFlags(fs)
	host := registerHostFlag(fs)
	pauseModel := fs.String("pause-model", "frames", "suspending function form: frames or native")
	lineDirectives := fs.Bool("line-directives", true, "annotate generated code with source positions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	names, collisions, err := nf.resolve(fs, "", "")
	if err != nil {
		fmt.Fprintln(stderr, "goalchemy run:", err)
		return 2
	}
	res, ds := driver.Build(ctx, driver.Options{Dir: sf.dir, Patterns: fs.Args(), Tags: sf.tagList(), Gate: subset.Gate(sf.gate)})
	if diagnostics.HasErrors(ds) {
		_ = diagnostics.Write(stderr, ds, sf.json)
		return 1
	}
	writeWarnings(stderr, ds, sf.json)
	out, err := os.MkdirTemp("", "goalchemy-run-")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *keep {
		fmt.Fprintln(stderr, "generated:", out)
	} else {
		defer os.RemoveAll(out)
	}
	if eds := driver.EmitWithOptions(*target, res, out, driver.EmitOptions{Names: names, NameCollisions: collisions, PauseModel: *pauseModel, NoLineDirectives: !*lineDirectives, Host: *host}); len(eds) > 0 {
		_ = diagnostics.Write(stderr, eds, sf.json)
		return 1
	}
	var cmd *exec.Cmd
	switch *target {
	case "go":
		build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(out, "prog"), ".")
		build.Dir, build.Stdout, build.Stderr = out, stderr, stderr
		if err := build.Run(); err != nil {
			return 1
		}
		cmd = exec.CommandContext(ctx, filepath.Join(out, "prog"))
	case "typescript":
		cmd = exec.CommandContext(ctx, "node", "--enable-source-maps", filepath.Join(out, "main.ts"))
	default:
		cmd = runnerFor(ctx, *target, out)
		if cmd == nil {
			fmt.Fprintf(stderr, "goalchemy run: no runner for target %s\n", *target)
			return 2
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var runners = map[string]func(ctx context.Context, out string) *exec.Cmd{
	"python": func(ctx context.Context, out string) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", filepath.Join(out, "main.py"))
	},
	"java":   shellRunner,
	"csharp": shellRunner,
	"rust":   shellRunner,
	"tinygo": shellRunner,
	"c":      shellRunner,
	"swift":  shellRunner,
}

func shellRunner(ctx context.Context, out string) *exec.Cmd {
	c := exec.CommandContext(ctx, "sh", filepath.Join(out, "run.sh"))
	c.Env = driver.ToolEnv()
	return c
}

func runnerFor(ctx context.Context, target, out string) *exec.Cmd {
	if r, ok := runners[target]; ok {
		return r(ctx, out)
	}
	return nil
}
