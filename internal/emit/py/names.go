package py

import (
	"fmt"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated Python:
// keywords, builtins, the runtime import and the members of runtime frames.
func NameRules(p *ir.Program) naming.Rules {
	reserved := []string{
		"False", "None", "True", "and", "as", "assert", "async", "await", "break", "class", "continue", "def",
		"del", "elif", "else", "except", "finally", "for", "from", "global", "if", "import", "in", "is",
		"lambda", "nonlocal", "not", "or", "pass", "raise", "return", "try", "while", "with", "yield", "match",
		"case", "type", "self",
		"abs", "aiter", "all", "anext", "any", "ascii", "bin", "bool", "breakpoint", "bytearray", "bytes",
		"callable", "chr", "classmethod", "compile", "complex", "delattr", "dict", "dir", "divmod", "enumerate",
		"eval", "exec", "exit", "filter", "float", "format", "frozenset", "getattr", "globals", "hasattr", "hash",
		"help", "hex", "id", "input", "int", "isinstance", "issubclass", "iter", "len", "list", "locals", "map",
		"max", "memoryview", "min", "next", "object", "oct", "open", "ord", "pow", "print", "property", "quit",
		"range", "repr", "reversed", "round", "set", "setattr", "slice", "sorted", "staticmethod", "str", "sum",
		"super", "tuple", "vars", "zip", "NotImplemented", "Ellipsis", "Exception", "BaseException", "rt",
	}
	if p != nil && p.Library {
		reserved = append(reserved, publicNames(p)...)
	}
	return naming.Rules{Reserved: reserved, Members: []string{"pc", "defers", "parent", "panicking", "step", "results"}, TypePrefix: "struct_"}
}

// publicNames lists a library's public API names and parameter names, which
// share the entry module with imported source names.
func publicNames(p *ir.Program) []string {
	names := []string{"set_log_handler", "logging_handler", "LogRecord", "options"}
	abi, err := p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return names
	}
	most := 0
	funcs := append([]*ir.Func{}, p.Exports...)
	for _, h := range abi.Handles {
		names = append(names, h.Name)
		funcs = append(funcs, h.Methods...)
	}
	for _, f := range funcs {
		names = append(names, f.Name[strings.LastIndex(f.Name, ".")+1:])
		most = max(most, len(f.Sig.Params))
	}
	for i := 0; i < most; i++ {
		names = append(names, fmt.Sprintf("a%d", i))
	}
	return names
}
