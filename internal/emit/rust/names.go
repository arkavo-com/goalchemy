package rust

import (
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated Rust:
// keywords, primitive types, prelude names and the crate's fixed modules and
// entry points. Runtime names come from ir.Program.ReservedNames.
func NameRules(p *ir.Program) naming.Rules {
	return naming.Rules{Reserved: []string{
		"as", "break", "const", "continue", "crate", "else", "enum", "extern", "false", "fn", "for", "if", "impl",
		"in", "let", "loop", "match", "mod", "move", "mut", "pub", "ref", "return", "self", "Self", "static",
		"struct", "super", "trait", "true", "type", "unsafe", "use", "where", "while", "async", "await", "dyn",
		"abstract", "become", "box", "do", "final", "macro", "override", "priv", "typeof", "unsized", "virtual",
		"yield", "try", "union", "gen", "raw", "safe",
		"i8", "i16", "i32", "i64", "i128", "isize", "u8", "u16", "u32", "u64", "u128", "usize", "f32", "f64",
		"bool", "char", "str", "Option", "Some", "None", "Result", "Ok", "Err", "Vec", "String", "Box", "Copy",
		"Clone", "Send", "Sync", "Sized", "Drop", "Fn", "FnMut", "FnOnce", "drop", "Iterator", "IntoIterator",
		"Default", "Eq", "PartialEq", "Ord", "PartialOrd", "ToString", "ToOwned", "From", "Into", "AsRef",
		"AsMut", "Extend", "std", "core", "alloc", "main", "run_host", "rt", "shared",
	}}
}

func NoStdNameRules(p *ir.Program) naming.Rules {
	r := NameRules(p)
	r.Reserved = append(r.Reserved, "run", "RuntimeOptions", "CallResult", "HeapStats", "NoStdError")
	return r
}
