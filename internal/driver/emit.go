package driver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/link"
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
	CompactNames bool
}

// Emit writes a target with readable private identifiers.
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
	if res != nil && res.IR != nil {
		result := *res
		program := *res.IR
		program.CompactNames = opts.CompactNames
		result.IR = &program
		res = &result
	}
	e, ok := emitters[name]
	if !ok {
		return []diagnostics.Diagnostic{{Code: "GCE001", Severity: diagnostics.Error, Feature: "target",
			Message: "unknown or unimplemented target " + name, Remedy: "Choose one of: " + joinTargets()}}
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
