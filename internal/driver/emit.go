package driver

import (
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// Emitter writes a complete target build directory.
type Emitter func(res *Result, out string) []diagnostics.Diagnostic

var emitters = map[string]Emitter{}

func Register(name string, e Emitter) { emitters[name] = e }

func Targets() []string {
	var out []string
	for n := range emitters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// EmitOptions controls the presentation of generated private identifiers.
type EmitOptions struct {
	// Names is source (the default), readable or compact.
	Names string
	// NameCollisions is rename (the default) or fail.
	NameCollisions string
	PauseModel     string
	// NoLineDirectives omits line directives, source maps and line tables.
	NoLineDirectives bool
}

// PauseModels lists the accepted pause models; the first is the default.
var PauseModels = []string{"frames", "native"}

// nativePause lists the targets that implement the native pause model.
var nativePause = map[string]bool{"go": true, "typescript": true, "csharp": true, "swift": true, "python": true, "rust": true, "java": true, "ir": true}

// Emit writes a target with the default emission options.
func Emit(name string, res *Result, out string) []diagnostics.Diagnostic {
	return EmitWithOptions(name, res, out, EmitOptions{})
}

// EmitWithOptions selects private naming for this emission alone. Copying the
// program header keeps repeated and concurrent emissions independent.
func EmitWithOptions(name string, res *Result, out string, opts EmitOptions) (ds []diagnostics.Diagnostic) {
	defer func() {
		if r := recover(); r != nil {
			ds = []diagnostics.Diagnostic{{Code: "GCE004", Severity: diagnostics.Error, Feature: "emission",
				Message: fmt.Sprintf("%s emitter failed: %v", name, r),
				Remedy:  "This is a compiler defect; please report it with the source program."}}
		}
	}()
	mode, err := naming.ParseMode(opts.Names)
	if err != nil {
		return namesErr(err)
	}
	policy, err := naming.ParsePolicy(opts.NameCollisions)
	if err != nil {
		return namesErr(err)
	}
	if res != nil && res.IR != nil {
		result := *res
		program := *res.IR
		program.Names, program.NameCollisions = string(mode), policy
		result.IR = &program
		res = &result
	}
	e, ok := emitters[name]
	if !ok {
		return []diagnostics.Diagnostic{{Code: "GCE001", Severity: diagnostics.Error, Feature: "target",
			Message: "unknown or unimplemented target " + name, Remedy: "Choose one of: " + joinTargets()}}
	}
	if opts.PauseModel == "" && nativePause[name] {
		// Test suites select a pause model for every target emission.
		opts.PauseModel = os.Getenv("GOALCHEMY_PAUSE_MODEL")
	}
	switch opts.PauseModel {
	case "", "frames":
	case "native":
		if !nativePause[name] {
			return []diagnostics.Diagnostic{{Code: "GCE008", Severity: diagnostics.Error, Feature: "pause model",
				Message: "target " + name + " does not implement pause model native",
				Remedy:  "Use -pause-model=frames for this target."}}
		}
	default:
		return []diagnostics.Diagnostic{{Code: "GCE008", Severity: diagnostics.Error, Feature: "pause model",
			Message: "unknown pause model " + opts.PauseModel, Remedy: "Choose frames or native."}}
	}
	if res != nil && res.IR != nil {
		res.IR.PauseModel = opts.PauseModel
		res.IR.NoLineDirectives = opts.NoLineDirectives
		if scan, ok := unqualifiedRuntime[name]; ok && mode == naming.Source && res.Catalog != nil {
			declared, called, err := runtimeNames(res.Catalog.FS, name, scan)
			if err != nil {
				return emitErr("GCE005", "runtime names: "+err.Error())
			}
			res.IR.ReservedNames, res.IR.ReservedGlobals = declared, called
		}
		if rules, ok := nameRules[name]; ok && mode == naming.Source && policy == naming.Fail {
			if ds := collisions(res.IR, naming.New(res.IR, rules(res.IR)).Collisions()); len(ds) > 0 {
				return ds
			}
		}
	}
	if res.IR != nil && res.IR.Library && name != "c" && name != "go" && name != "typescript" && name != "java" && name != "csharp" && name != "python" && name != "rust" && name != "swift" && name != "ir" {
		return []diagnostics.Diagnostic{{Code: "GCE006", Severity: diagnostics.Error, Feature: "library build",
			Message: "target " + name + " cannot build a library",
			Remedy:  "Compile package main for this target, or choose a target with a supported library API."}}
	}
	// Fail before target type lowering when a declared capability has no
	// implementation; opaque native keys must never become fake target values.
	if res.IR != nil && res.Catalog != nil && name != "ir" {
		var used []string
		for _, external := range res.IR.Externals {
			used = append(used, external.Contract)
		}
		// Type-only opaque handles also need a runtime representation. Use
		// the first declaration-backed receiver contract as its owner.
		ids := res.Catalog.IDs()
		if res.IR.Types != nil {
			for _, typ := range res.IR.Types.All {
				if typ.Kind != ir.KOpaque {
					continue
				}
				prefix := typ.Pkg + ".(" + typ.Obj + ")."
				for _, id := range ids {
					fn := res.Catalog.Functions[id]
					if fn != nil && strings.HasPrefix(fn.Signature.Symbol, prefix) {
						used = append(used, id)
						break
					}
				}
			}
		}
		sort.Strings(used)
		_, _, ds := link.Plan(res.Catalog, name, used)
		if diagnostics.HasErrors(ds) {
			return ds
		}
	}
	if name == "ir" {
		return e(res, out)
	}
	previous, err := managedFiles(out)
	if err != nil {
		return emitErr("GCE005", "output inventory: "+err.Error())
	}
	ds = e(res, out)
	if diagnostics.HasErrors(ds) {
		return ds
	}
	if err := cleanObsoleteFiles(out, previous); err != nil {
		return emitErr("GCE005", "output cleanup: "+err.Error())
	}
	return ds
}

// unqualifiedRuntime lists targets whose generated code names runtime
// declarations without a qualifier, so source names must avoid them. Java
// and C# see only the runtime's top-level types; Rust, C and Swift share a
// namespace with the runtime, so its own references to types count too, and
// on C runtime macros expand to host calls inside generated functions.
var unqualifiedRuntime = map[string]naming.RuntimeScan{
	"java": {}, "csharp": {}, "rust": {Types: true, Calls: true}, "swift": {Types: true, Calls: true},
	"c": {Types: true, Calls: true, LocalCalls: true},
}

func runtimeNames(fsys fs.FS, target string, scan naming.RuntimeScan) (declared, called []string, err error) {
	var roots []string
	for _, dir := range []string{"runtime", "types", "native"} {
		root := "targets/" + target + "/" + dir
		if _, err := fs.Stat(fsys, root); err == nil {
			roots = append(roots, root)
		}
	}
	return naming.RuntimeNames(fsys, scan, roots...)
}

func namesErr(err error) []diagnostics.Diagnostic {
	return []diagnostics.Diagnostic{{Code: "GCE009", Severity: diagnostics.Error, Feature: "names",
		Message: err.Error(), Remedy: "Use -names=source, readable or compact and -name-collisions=rename or fail."}}
}

// collisions reports source names that source naming would have renamed.
func collisions(p *ir.Program, cs []naming.Collision) []diagnostics.Diagnostic {
	var ds []diagnostics.Diagnostic
	for _, c := range cs {
		d := diagnostics.Diagnostic{Code: "GCE009", Severity: diagnostics.Error, Feature: "names", Symbol: c.Name,
			Remedy: "Rename the declaration, or use -name-collisions=rename to let the compiler rename it."}
		where := ""
		if p.Fset != nil && c.Pos.IsValid() {
			pos := p.Fset.Position(c.Pos)
			d.File, d.Line, d.Column = pos.Filename, pos.Line, pos.Column
		}
		if p.Fset != nil && c.Other.IsValid() {
			where = " declared at " + p.Fset.Position(c.Other).String()
		}
		switch {
		case c.OtherName != "":
			d.Message = fmt.Sprintf("%s collides with %s%s (%s); it would be emitted as %s", c.Name, c.OtherName, where, c.Reason, c.Emitted)
		default:
			d.Message = fmt.Sprintf("%s cannot keep its name (%s); it would be emitted as %s", c.Name, c.Reason, c.Emitted)
		}
		ds = append(ds, d)
	}
	return ds
}

func joinTargets() string {
	s := ""
	for i, t := range Targets() {
		if i > 0 {
			s += ", "
		}
		s += t
	}
	return s
}
