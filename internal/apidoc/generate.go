package apidoc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/link"
)

// Generate loads the checked library and captures declarations from selected native emitters.
func Generate(ctx context.Context, opts driver.Options, title string, targets []string) (*Catalog, []diagnostics.Diagnostic, error) {
	res, ds := driver.Build(ctx, opts)
	if diagnostics.HasErrors(ds) {
		return nil, ds, nil
	}
	if !res.IR.Library {
		return nil, ds, fmt.Errorf("API documentation requires non-main library packages")
	}
	c := Extract(res.Program, title, link.CompilerVersion)
	byID := map[string]*Symbol{}
	for _, s := range c.Symbols {
		byID[s.ID] = s
	}
	if len(targets) == 0 {
		targets = driver.Targets()
	}
	targets = append([]string{}, targets...)
	sort.Strings(targets)
	known := map[string]bool{}
	for _, target := range driver.Targets() {
		known[target] = true
	}
	temp, err := os.MkdirTemp("", "goalchemy-api-")
	if err != nil {
		return nil, ds, err
	}
	defer os.RemoveAll(temp)
	seen := map[string]bool{}
	for _, target := range targets {
		if !known[target] {
			return nil, ds, fmt.Errorf("unknown documentation target %q", target)
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		if err := ctx.Err(); err != nil {
			return nil, ds, err
		}
		var declarations []artifact.APIDeclaration
		errors := driver.EmitWithOptions(target, res, filepath.Join(temp, target), driver.EmitOptions{ObserveAPI: func(items []artifact.APIDeclaration) { declarations = items }})
		t := Target{Name: target, Status: "available"}
		if diagnostics.HasErrors(errors) {
			t.Status = "unavailable"
			var reasons []string
			for _, d := range errors {
				if d.Severity == diagnostics.Error {
					reasons = append(reasons, d.Code+": "+d.Message)
				}
			}
			t.Reason = strings.Join(reasons, "; ")
		} else {
			for _, d := range declarations {
				ids := map[string]bool{}
				if f := d.Function; f != nil {
					name := f.Name[strings.LastIndexByte(f.Name, '.')+1:]
					if f.RecvType != nil {
						if recv := named(f.RecvType.Go); recv != nil {
							name = recv.Obj().Name() + "." + name
						}
					}
					ids[SymbolID(f.Pkg, name)] = true
				}
				if d.Type != nil {
					// Emitters share structural value representations. A converter may
					// first see the unnamed shape; attach its public declaration to
					// each documented named source type using that representation.
					for _, source := range res.IR.Types.All {
						if source.Go != nil && source.U() == d.Type.U() {
							if n := named(source.Go); n != nil {
								ids[objectID(n.Obj())] = true
							}
						}
					}
				}
				binding := Binding{Target: target, Name: d.Name, Signature: strings.TrimSpace(d.Signature), Invocation: d.Invocation}
				for id := range ids {
					if s := byID[id]; s != nil {
						duplicate := false
						for _, existing := range s.Bindings {
							if existing == binding {
								duplicate = true
								break
							}
						}
						if !duplicate {
							s.Bindings = append(s.Bindings, binding)
						}
					}
				}
			}
		}
		c.Targets = append(c.Targets, t)
	}
	for _, s := range c.Symbols {
		sort.SliceStable(s.Bindings, func(i, j int) bool {
			a, b := s.Bindings[i], s.Bindings[j]
			if a.Target != b.Target {
				return a.Target < b.Target
			}
			return a.Signature < b.Signature
		})
	}
	return c, ds, c.Check()
}
