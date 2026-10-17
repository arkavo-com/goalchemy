package ts

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated
// TypeScript: reserved words, the type and global names generated code uses
// unqualified, the runtime import and the members of runtime frames.
func NameRules(p *ir.Program) naming.Rules {
	reserved := []string{
		"break", "case", "catch", "class", "const", "continue", "debugger", "default", "delete", "do", "else",
		"enum", "export", "extends", "false", "finally", "for", "function", "if", "import", "in", "instanceof",
		"new", "null", "return", "super", "switch", "this", "throw", "true", "try", "typeof", "var", "void",
		"while", "with", "yield", "let", "static", "implements", "interface", "package", "private", "protected",
		"public", "await", "async", "arguments", "eval", "of", "as", "type", "declare", "namespace", "module",
		"any", "unknown", "never", "object", "number", "bigint", "boolean", "string", "symbol", "undefined",
		"NaN", "Infinity", "globalThis", "Array", "BigInt", "Boolean", "Error", "JSON", "Map", "Math", "Number",
		"Object", "Promise", "Record", "Set", "String", "Symbol", "Uint8Array", "rt",
	}
	if p != nil && p.Library {
		reserved = append(reserved, publicNames(p)...)
	}
	return naming.Rules{Reserved: reserved, Members: []string{"pc", "defers", "parent", "panicking", "step", "results", "constructor", "__proto__"}, TypePrefix: "S$"}
}

// publicNames lists a library's public API names and parameter names, which
// share the entry module with imported source names.
func publicNames(p *ir.Program) []string {
	names := []string{"LibraryError", "CallOptions", "Callback", "Settlement", "setLogHandler", "LogRecord", "LogHandler",
		"SourceStream", "SourceReader", "SourceWriter", "SourceSeeker", "SourceCloser", "options"}
	abi, err := p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return names
	}
	roots := map[string]bool{}
	most := 0
	funcs := append([]*ir.Func{}, p.Exports...)
	for _, h := range abi.Handles {
		names = append(names, h.Name)
		funcs = append(funcs, h.Methods...)
	}
	for _, f := range funcs {
		roots[f.Pkg] = true
		names = append(names, f.Name[strings.LastIndex(f.Name, ".")+1:])
		most = max(most, len(f.Sig.Params))
	}
	for _, t := range p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && token.IsExported(t.Obj) {
			names = append(names, t.Obj)
		}
	}
	for i := 0; i < most; i++ {
		names = append(names, fmt.Sprintf("a%d", i))
	}
	return names
}
