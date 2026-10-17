package structure

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// dumpEmitter prints blocks by number, for structure tests.
type dumpEmitter struct{ names map[ir.Value]string }

func (d dumpEmitter) Code(b *ir.Block) string {
	if len(b.Instrs) == 0 {
		return ""
	}
	return fmt.Sprintf("b%d()", b.ID)
}

func (d dumpEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	switch t := t.(type) {
	case *ir.Pause:
		return fmt.Sprintf("pause -> b%d", t.Next.ID)
	case *ir.Panic:
		return "panic()"
	}
	return "return"
}

func (d dumpEmitter) Pause(p *ir.Pause) string { return "pause()" }

func (d dumpEmitter) Cond(v ir.Value) string {
	if n, ok := d.names[v]; ok {
		return n
	}
	if l, ok := v.(*ir.Local); ok {
		return l.Name
	}
	return "?"
}

func (d dumpEmitter) Mark(token.Pos) string { return "" }

var goSyntax = Syntax{
	Indent: "\t", If: "if %s {", ElseIf: "} else if %s {", Else: "} else {", End: "}",
	Loop: "for {", While: "for %s {", LoopLabel: "%s: ", LabelName: "L%d",
	Break: "break", BreakTo: "break %s", Continue: "continue", ContinueTo: "continue %s",
	Goto: "goto %s", Label: "%s:", Not: "!%s", And: "%s && %s", Or: "%s || %s",
	EntryLabel: "b%d:", GotoEntry: "goto b%d",
	Resume: func(hs []*ir.Block) string {
		var b strings.Builder
		b.WriteString("switch pc {\n")
		for _, h := range hs {
			fmt.Fprintf(&b, "case %d:\n\tgoto b%d\n", h.ID, h.ID)
		}
		b.WriteString("}")
		return b.String()
	},
}

var pySyntax = Syntax{
	Indent: "    ", If: "if %s:", ElseIf: "elif %s:", Else: "else:",
	Loop: "while True:", While: "while %s:", Break: "break", Continue: "continue",
	Python: true, Flag: "_x", Pass: "pass", Not: "not %s", And: "%s and %s", Or: "%s or %s",
	DispatchOpen: []string{"_pc = 0", "while True:"}, CaseFirst: "if _pc == %d:", CaseOpen: "elif _pc == %d:",
	Default: []string{"else:", "    raise fault()"}, SetPC: "_pc = %d",
}

// Dump renders a body in Go-like syntax.
func Dump(body *Body) string {
	return Resolve(Print(body, goSyntax, dumpEmitter{}, 0))
}
