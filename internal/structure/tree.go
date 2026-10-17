// Package structure rebuilds structured control flow (if, loops, labelled
// blocks, break and continue) from a function's block graph, so targets print
// source-like code instead of a block-dispatch loop. Regions that cannot be
// structured, and loops that contain a pause under the frame model, keep a
// dispatch over their entry blocks.
package structure

import (
	"go/token"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// Stmt is a node of a structured body.
type Stmt interface{ stmt() }

// Code runs the instructions of Block (and, for a resumed block under the
// frame model, first receives the paused operation's results).
type Code struct{ Block *ir.Block }

// Leaf ends control flow with Block's terminator: Return, Panic,
// PanicRuntime, Unreachable, or a frame-model Pause.
type Leaf struct {
	Block *ir.Block
	Term  ir.Terminator
}

// Pause runs a pause point inline and continues with the statements that
// follow it (native pause model).
type Pause struct{ Pause *ir.Pause }

type If struct {
	Cond       Cond
	Then, Else []Stmt
	Pos        token.Pos
}

// Loop repeats Body until a Break leaves it. While, when set, is tested
// before every iteration, including those started by Continue.
type Loop struct {
	While Cond
	Body  []Stmt
}

// Block runs Body; a Break targeting it continues after the block.
type Block struct{ Body []Stmt }

// Break leaves Target, a *Loop or *Block.
type Break struct{ Target Stmt }

// Continue starts the next iteration of Target.
type Continue struct{ Target *Loop }

// Goto continues at another dispatch entry.
type Goto struct {
	Dispatch *Dispatch
	Target   *ir.Block
}

// Dispatch selects one of its entries by block and runs it; entries leave
// through Goto or a Leaf.
type Dispatch struct{ Entries []*Entry }

type Entry struct {
	Head *ir.Block
	Body []Stmt
}

func (*Code) stmt()     {}
func (*Leaf) stmt()     {}
func (*Pause) stmt()    {}
func (*If) stmt()       {}
func (*Loop) stmt()     {}
func (*Block) stmt()    {}
func (*Break) stmt()    {}
func (*Continue) stmt() {}
func (*Goto) stmt()     {}
func (*Dispatch) stmt() {}

// Cond is a Boolean condition: a Value, or a short-circuit combination
// rebuilt from the block graph.
type Cond interface{ cond() }

type Value struct{ V ir.Value }
type Not struct{ X Cond }
type And struct{ X, Y Cond }
type Or struct{ X, Y Cond }

func (Value) cond() {}
func (Not) cond()   {}
func (And) cond()   {}
func (Or) cond()    {}

func negate(c Cond) Cond {
	if n, ok := c.(Not); ok {
		return n.X
	}
	return Not{c}
}

// Body is a structured function body.
type Body struct {
	Stmts []Stmt
	// Dispatch is the dispatch statement when the body needs one; it is
	// then the only statement of Stmts, or of the exit block.
	Dispatch *Dispatch
}
