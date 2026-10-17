package artifact

import "github.com/eugenioenko/goalchemy/internal/ir"

// APIDeclaration records a public declaration using the library emitter's native types.
// Exactly one of Function or Type identifies its source declaration.
type APIDeclaration struct {
	Function   *ir.Func
	Type       *ir.Type
	Name       string
	Signature  string
	Invocation string
}
