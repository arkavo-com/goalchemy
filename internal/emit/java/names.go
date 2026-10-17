package java

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated Java:
// keywords, the entry classes, the java.lang names generated code uses
// unqualified, the members of runtime frames and a library's public names.
// Runtime names come from ir.Program.ReservedNames.
func NameRules(p *ir.Program) naming.Rules {
	reserved := []string{
		"abstract", "assert", "boolean", "break", "byte", "case", "catch", "char", "class", "const", "continue",
		"default", "do", "double", "else", "enum", "extends", "final", "finally", "float", "for", "goto", "if",
		"implements", "import", "instanceof", "int", "interface", "long", "native", "new", "package", "private",
		"protected", "public", "return", "short", "static", "strictfp", "super", "switch", "synchronized", "this",
		"throw", "throws", "transient", "try", "void", "volatile", "while", "true", "false", "null", "var",
		"yield", "record", "sealed", "permits", "when", "module", "open", "exports", "requires",
		"Main", "Generated", "main", "runHost", "java", "rt", "Object", "String", "Long", "Integer", "Short",
		"Byte", "Boolean", "Character", "Float", "Double", "Number", "Void", "Math", "System", "Thread",
		"Runnable", "Iterable", "Exception", "RuntimeException", "Error", "Throwable", "Override",
		"SuppressWarnings", "FunctionalInterface", "Class", "Enum", "Record", "StringBuilder", "AutoCloseable",
	}
	if p != nil && p.Library {
		reserved = append(reserved, publicNames(p)...)
	}
	return naming.Rules{Reserved: reserved, Members: []string{"pc", "defers", "parent", "panicking", "step", "results"}, TypePrefix: "S$"}
}

// publicNames lists a library's public API names and parameter names, which
// share the program class with source names.
func publicNames(p *ir.Program) []string {
	names := []string{"Library", "Callback", "options", "Generated"}
	abi, err := p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return names
	}
	roots := map[string]bool{}
	most := 0
	funcs := append([]*ir.Func{}, p.Exports...)
	for _, h := range abi.Handles {
		names = append(names, h.Name)
		for _, f := range h.Methods {
			names = append(names, h.Name+ir.MethodName(f)+"Result")
		}
		funcs = append(funcs, h.Methods...)
	}
	for _, f := range funcs {
		roots[f.Pkg] = true
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		names = append(names, name, name+"Result")
		most = max(most, len(f.Sig.Params))
	}
	for _, t := range p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && token.IsExported(t.Obj) {
			names = append(names, t.Obj)
		}
		names = append(names, fmt.Sprintf("Value%d", t.ID))
	}
	for i := 0; i < most; i++ {
		names = append(names, fmt.Sprintf("a%d", i))
	}
	return names
}
