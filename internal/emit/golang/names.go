package golang

import (
	"fmt"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated Go:
// keywords, predeclared identifiers, imported packages, the entry points and
// a library's public names. Package-level source names start lowercase, so
// the generated package exports only the library's public API.
func NameRules(p *ir.Program) naming.Rules {
	reserved := []string{
		"break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough", "for", "func",
		"go", "goto", "if", "import", "interface", "map", "package", "range", "return", "select", "struct",
		"switch", "type", "var",
		"any", "append", "bool", "byte", "cap", "clear", "close", "comparable", "complex", "complex64",
		"complex128", "copy", "delete", "error", "false", "float32", "float64", "imag", "int", "int8", "int16",
		"int32", "int64", "iota", "len", "make", "max", "min", "new", "nil", "panic", "print", "println", "real",
		"recover", "rune", "string", "true", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"rt", "context", "io", "reflect", "main", "init",
	}
	if p != nil && p.Library {
		reserved = append(reserved, publicNames(p)...)
	}
	return naming.Rules{Reserved: reserved, Members: []string{"FrameBase", "PC", "Defers", "Base", "Step", "Results"}, TypePrefix: "type_", Lower: true}
}

// publicNames lists the public names and parameter names of a library's
// generated API, which keep their spelling.
func publicNames(p *ir.Program) []string {
	e := &emitter{p: p}
	names := []string{"Callbacks", "Callback", "LibraryError", "ctx", "callbacks", "err", "x"}
	if e.libraryNames() != nil {
		return names
	}
	for _, alias := range e.publicTypes {
		names = append(names, alias)
	}
	for _, h := range e.abi.Handles {
		names = append(names, h.Name)
	}
	most := 0
	for _, f := range e.libraryFuncs() {
		names = append(names, f.Name[strings.LastIndex(f.Name, ".")+1:])
		most = max(most, len(f.Sig.Params), len(f.Sig.Results))
	}
	for i := 0; i < most; i++ {
		names = append(names, fmt.Sprintf("a%d", i), fmt.Sprintf("r%d", i))
	}
	return names
}
