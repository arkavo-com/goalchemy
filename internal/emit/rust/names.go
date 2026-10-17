package rust

import (
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/naming"
)

// NameRules lists the names source identifiers avoid in this target.
func NameRules(p *ir.Program) naming.Rules {
	return naming.Rules{}
}
