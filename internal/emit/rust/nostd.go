package rust

import (
	"fmt"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/ir"
)

// no_std is a sequential, owned-value ABI. Rust's panic handler terminates
// source panics; accepting recover would silently change source behavior.
func validateNoStd(p *ir.Program) error {
	fail := func(message string) error { return &LibraryBoundaryError{Message: "rust-nostd: " + message} }
	if p.Cooperative {
		return fail("concurrency requires the rust target")
	}
	for _, f := range p.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				if _, ok := in.(*ir.Recover); ok {
					return fail("recover requires panic unwinding; use the rust target")
				}
			}
		}
	}
	for _, f := range p.Exports {
		for _, t := range f.Sig.Params {
			if !libraryValue(t, map[*ir.Type]bool{}) {
				return fail("export " + f.Name + " requires owned value parameters (no handles, context, or streams)")
			}
		}
		for i, t := range f.Sig.Results {
			if sourceError(t) && i == len(f.Sig.Results)-1 {
				continue
			}
			if !libraryValue(t, map[*ir.Type]bool{}) {
				return fail("export " + f.Name + " requires owned value results (no handles or streams)")
			}
		}
	}
	return nil
}

func (e *emitter) noStdLibrary(b *strings.Builder, typ func(*ir.Type) string) {
	for _, f := range e.p.Exports {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		var ps, args, results, outputs []string
		for i, t := range f.Sig.Params {
			ps = append(ps, fmt.Sprintf("a%d: %s", i, typ(t)))
			args = append(args, fmt.Sprintf("%s(a%d)", e.names.Type(t.U(), "input_"), i))
		}
		n := len(f.Sig.Results)
		hasError := n > 0 && sourceError(f.Sig.Results[n-1])
		if hasError {
			n--
		}
		for i, t := range f.Sig.Results[:n] {
			results = append(results, typ(t))
			value := "rv.clone()"
			if len(f.Sig.Results) > 1 {
				value = fmt.Sprintf("rv.at(%d)", i)
			}
			outputs = append(outputs, fmt.Sprintf("%s(%s)", e.names.Type(t.U(), "output_"), value))
		}
		result, output := "()", "()"
		if n == 1 {
			result, output = results[0], outputs[0]
		} else if n > 1 {
			result, output = "("+strings.Join(results, ",")+")", "("+strings.Join(outputs, ",")+")"
		}
		ps = append(ps, "options: RuntimeOptions")
		sig := fmt.Sprintf("pub fn %s(%s) -> Result<CallResult<%s>, NoStdError>", name, strings.Join(ps, ", "), result)
		e.api = append(e.api, artifact.APIDeclaration{Function: f, Name: name, Signature: sig})
		fmt.Fprintf(b, "%s {\nrt::invoke(options, %d, _init_zero_globals, || {\n%s();\nlet args = vec![%s];\nlet roots = temp_root(&args);\nlet rv = %s(&[], args);\nlet result_root = temp_root(&[rv.clone()]);\n", sig, len(e.p.Globals), e.symbol(e.p.Init.Sym, "f_"), strings.Join(args, ","), e.symbol(f.Sym, "w_"))
		if hasError {
			value := "rv.clone()"
			if len(f.Sig.Results) > 1 {
				value = fmt.Sprintf("rv.at(%d)", n)
			}
			fmt.Fprintf(b, "let error = %s;\nif !error.is_nil() { return Err(NoStdError::Source(icall(&error, \"Error\", vec![]).bytes().to_vec())); }\n", value)
		}
		fmt.Fprintf(b, "Ok(%s)\n})\n}\n", output)
	}
}
