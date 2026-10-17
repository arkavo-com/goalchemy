package csharp

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated C#:
// keywords, the program class and entry points, the framework names generated
// code uses unqualified, the members of runtime frames and a library's public
// names. Runtime names come from ir.Program.ReservedNames.
func NameRules(p *ir.Program) naming.Rules {
	reserved := []string{
		"abstract", "as", "base", "bool", "break", "byte", "case", "catch", "char", "checked", "class", "const",
		"continue", "decimal", "default", "delegate", "do", "double", "else", "enum", "event", "explicit",
		"extern", "false", "finally", "fixed", "float", "for", "foreach", "goto", "if", "implicit", "in", "int",
		"interface", "internal", "is", "lock", "long", "namespace", "new", "null", "object", "operator", "out",
		"override", "params", "private", "protected", "public", "readonly", "ref", "return", "sbyte", "sealed",
		"short", "sizeof", "stackalloc", "static", "string", "struct", "switch", "this", "throw", "true", "try",
		"typeof", "uint", "ulong", "unchecked", "unsafe", "ushort", "using", "virtual", "void", "volatile",
		"while", "var", "dynamic", "async", "await", "yield", "nameof", "record", "global", "partial", "value",
		"when", "where", "init", "notnull", "unmanaged", "managed", "args",
		"System", "Rt", "Goalchemy", "GoProgram", "Main", "runHost", "Array", "Exception", "Func", "List",
	}
	if p != nil && p.Library {
		reserved = append(reserved, publicNames(p)...)
	}
	return naming.Rules{Reserved: reserved, Members: []string{"pc", "defers", "parent", "depth", "panicking", "step", "results"}, TypePrefix: "s_"}
}

// publicNames lists a library's public API names and parameter names, which
// share the program class with source names.
func publicNames(p *ir.Program) []string {
	names := []string{"Library", "Callback", "options"}
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
