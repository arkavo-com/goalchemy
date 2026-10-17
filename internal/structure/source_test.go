package structure_test

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/ir"
	"github.com/eugenioenko/goalchemy/internal/structure"
	"github.com/eugenioenko/goalchemy/internal/subset"
)

// lowered compiles a Go program to IR.
func lowered(t *testing.T, src string) *ir.Program {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mod := "module p\n\ngo 1.25\n\nrequire github.com/eugenioenko/goalchemy v0.0.0\n\nreplace github.com/eugenioenko/goalchemy => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: dir, Patterns: []string{"."}, Gate: subset.Cooperative})
	for _, d := range ds {
		t.Log(d.Message)
	}
	if res == nil || res.IR == nil {
		t.Fatal("no IR")
	}
	return res.IR
}

func function(t *testing.T, p *ir.Program, name string) *ir.Func {
	t.Helper()
	for _, f := range p.Funcs {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no function %s", name)
	return nil
}

// srcEmitter renders statements as the IR printer spells them.
type srcEmitter struct{}

func (srcEmitter) Code(b *ir.Block) string {
	var out []string
	for _, in := range b.Instrs {
		out = append(out, ir.InstrString(in))
	}
	return strings.Join(out, "\n")
}

func (srcEmitter) Leaf(b *ir.Block, t ir.Terminator) string {
	switch t := t.(type) {
	case *ir.Pause:
		return fmt.Sprintf("pause %s -> b%d", ir.InstrString(t.Op), t.Next.ID)
	case *ir.Return:
		return "return"
	case *ir.Panic:
		return "panic(" + ir.ValueString(t.X) + ")"
	}
	return fmt.Sprintf("%T", t)
}

func (srcEmitter) Pause(p *ir.Pause) string { return ir.InstrString(p.Op) }
func (srcEmitter) Cond(v ir.Value) string   { return ir.ValueString(v) }
func (srcEmitter) Mark(token.Pos) string    { return "" }

var syntax = structure.Syntax{
	Indent: "\t", If: "if %s {", ElseIf: "} else if %s {", Else: "} else {", End: "}",
	Loop: "for {", While: "for %s {", LoopLabel: "%s: ", LabelName: "L%d",
	Break: "break", BreakTo: "break %s", Continue: "continue", ContinueTo: "continue %s",
	Block: "%s: {", Not: "!%s", And: "%s && %s", Or: "%s || %s",
	DispatchOpen: []string{"%sfor {", "switch pc {"}, CaseOpen: "case %d:", DispatchClose: []string{"}", "}"}, SetPC: "pc = %d",
}

func render(f *ir.Func, inline bool) string {
	return structure.Resolve(structure.Print(structure.Build(f, structure.Options{Inline: inline}), syntax, srcEmitter{}, 0))
}

func TestExplore(t *testing.T) {
	path := os.Getenv("EXPLORE")
	if path == "" {
		t.Skip("set EXPLORE to a Go file")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := lowered(t, string(src))
	for _, f := range p.Funcs {
		if f.Pkg != "main" && !strings.HasPrefix(f.Name, "main.") {
			continue
		}
		t.Logf("func %s (suspends %v)\n%s", f.Name, f.MaySuspend, render(f, os.Getenv("INLINE") != ""))
	}
}
