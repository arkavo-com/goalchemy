package c

import (
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names Go source identifiers avoid in generated C.
// Functions, globals and helpers live in the generated namespace, so source
// names appear only as function-scoped constants; those avoid keywords and
// the macros of the standard headers the runtime includes. Runtime and host
// names come from ir.Program.ReservedNames.
func NameRules(p *ir.Program) naming.Rules {
	return naming.Rules{Reserved: []string{
		"auto", "break", "case", "char", "const", "continue", "default", "do", "double", "else", "enum", "extern",
		"float", "for", "goto", "if", "inline", "int", "long", "register", "restrict", "return", "short",
		"signed", "sizeof", "static", "struct", "switch", "typedef", "union", "unsigned", "void", "volatile",
		"while", "alignas", "alignof", "bool", "constexpr", "false", "nullptr", "static_assert",
		"thread_local", "true", "typeof", "typeof_unqual", "asm", "typeof", "main",
		"NULL", "errno", "assert", "offsetof", "setjmp", "longjmp", "sigsetjmp", "siglongjmp", "complex",
		"imaginary", "noreturn", "unreachable", "sa_handler", "sa_sigaction", "si_pid", "si_uid", "si_status",
		"si_addr", "si_value", "si_band", "si_fd", "st_atime", "st_mtime", "st_ctime", "minor", "major",
		"makedev", "stdin", "stdout", "stderr",
	}}
}
