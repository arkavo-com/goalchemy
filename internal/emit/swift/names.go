package swift

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated Swift:
// keywords, the module name and a library's public names. Runtime, standard
// library and Foundation names come from ir.Program.ReservedNames.
func NameRules(p *ir.Program) naming.Rules {
	reserved := []string{
		"associatedtype", "class", "deinit", "enum", "extension", "fileprivate", "func", "import", "init",
		"inout", "internal", "let", "open", "operator", "private", "precedencegroup", "protocol", "public",
		"rethrows", "static", "struct", "subscript", "typealias", "var", "break", "case", "catch", "continue",
		"default", "defer", "do", "else", "fallthrough", "for", "guard", "if", "in", "repeat", "return",
		"throw", "switch", "where", "while", "Any", "as", "await", "false", "is", "nil", "self", "Self",
		"super", "throws", "true", "try", "async", "actor", "convenience", "dynamic", "final", "indirect",
		"lazy", "mutating", "nonmutating", "optional", "override", "required", "some", "Type", "unowned",
		"weak", "consume", "copy", "discard", "borrowing", "consuming", "each", "macro", "package",
		"isolated", "nonisolated", "sending", "error", "print", "main", "Foundation", "Swift",
		"GoalchemyGenerated",
	}
	if p != nil && p.Library {
		reserved = append(reserved, publicNames(p)...)
	}
	return naming.Rules{Reserved: reserved}
}

// publicNames lists a library's public API names, which share the module
// with source names.
func publicNames(p *ir.Program) []string {
	names := []string{"options", "handle"}
	abi, err := p.LibraryHandles(func(t *ir.Type) bool { return libraryValue(t, map[*ir.Type]bool{}) })
	if err != nil {
		return names
	}
	roots := map[string]bool{}
	most := 0
	funcs := append([]*ir.Func{}, p.Exports...)
	for _, h := range abi.Handles {
		names = append(names, h.Name, h.Name+"_")
		funcs = append(funcs, h.Methods...)
	}
	for _, f := range funcs {
		roots[f.Pkg] = true
		names = append(names, f.Name[strings.LastIndex(f.Name, ".")+1:])
		most = max(most, len(f.Sig.Params))
	}
	for _, t := range p.Types.All {
		if t.Kind == ir.KNamed && roots[t.Pkg] && token.IsExported(t.Obj) {
			names = append(names, t.Obj, t.Obj+"_")
		}
		names = append(names, fmt.Sprintf("Value%d", t.ID))
	}
	for i := 0; i < most; i++ {
		names = append(names, fmt.Sprintf("a%d", i))
	}
	return names
}
