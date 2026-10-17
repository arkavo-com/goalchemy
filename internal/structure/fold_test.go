package structure

import (
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// toyProbe renders instructions as "dst = op(args)".
type toyProbe struct{ fs *Folds }

func (p *toyProbe) name(l *ir.Local) string {
	if n, ok := p.fs.Name(l); ok {
		return n
	}
	return l.Name
}

func (p *toyProbe) val(v ir.Value) string {
	if l, ok := v.(*ir.Local); ok {
		if s, ok := p.fs.Use(l); ok {
			return s
		}
		return p.name(l)
	}
	return "1"
}

func (p *toyProbe) Instr(in ir.Instr) string {
	switch i := in.(type) {
	case *ir.Assign:
		return p.val(i.Dst) + " = " + p.val(i.Src)
	case *ir.BinOp:
		return p.val(i.Dst) + " = " + p.val(i.X) + " + " + p.val(i.Y)
	case *ir.Call:
		var as []string
		for _, a := range i.Args {
			as = append(as, p.val(a))
		}
		call := i.Extern.Symbol + "(" + strings.Join(as, ", ") + ")"
		if len(i.Dsts) > 0 {
			return p.val(i.Dsts[0]) + " = " + call
		}
		return call
	}
	return "?"
}

func (p *toyProbe) Pause(ps *ir.Pause) string {
	c := ps.Op.(*ir.Call)
	return p.val(c.Dsts[0]) + " = await " + c.Func.Name + "()"
}
func (p *toyProbe) Cond(v ir.Value) string               { return p.val(v) }
func (p *toyProbe) Leaf(*ir.Block, ir.Terminator) string { return "" }
func (p *toyProbe) Assign(l *ir.Local) (string, string)  { return p.val(l) + " = ", "" }

func foldFunc(build func(f *ir.Func, ts *ir.Types, b *ir.Block)) *ir.Func {
	f := &ir.Func{Name: "f"}
	ts := ir.NewTypes()
	b := f.NewBlock("")
	build(f, ts, b)
	b.Term = &ir.Return{}
	return f
}

func folded(f *ir.Func, order Order) []string {
	p := &toyProbe{}
	fs := Analyze(f, FoldOptions{Order: order}, p, func(fs *Folds) { p.fs = fs })
	var out []string
	for _, in := range f.Blocks[0].Instrs {
		if !fs.Folded(in) {
			out = append(out, Resolve(p.Instr(in)))
		}
	}
	return out
}

func call(name string, dst *ir.Local, args ...ir.Value) *ir.Call {
	c := &ir.Call{Kind: ir.CallExtern, Extern: &ir.Extern{Symbol: name}, Args: args}
	if dst != nil {
		c.Dsts = []*ir.Local{dst}
	}
	return c
}

func TestFoldOrder(t *testing.T) {
	f := foldFunc(func(f *ir.Func, ts *ir.Types, b *ir.Block) {
		t1 := f.NewLocal("t1", ts.IntT(), ir.LTemp)
		t2 := f.NewLocal("t2", ts.IntT(), ir.LTemp)
		b.Instrs = []ir.Instr{call("f", t1), call("g", t2), call("h", nil, t1, t2)}
	})
	cases := []struct {
		order Order
		want  string
	}{
		{LeftToRight, "h(f(), g())"},
		{CallsInOrder, "h(f(), g())"},
		{Unsequenced, "t1 = f()|h(t1, g())"},
	}
	for _, c := range cases {
		if got := strings.Join(folded(f, c.order), "|"); got != c.want {
			t.Errorf("order %d: got %q, want %q", c.order, got, c.want)
		}
	}
}

func TestCallAlone(t *testing.T) {
	cases := []struct {
		name  string
		first func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr)
		want  bool
	}{
		{"allocated operand", func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			t1 := f.NewLocal("t1", ts.IntT(), ir.LTemp)
			return t1, call("alloc", t1)
		}, false},
		{"folded copy", func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			x := f.NewLocal("x", ts.IntT(), ir.LVar)
			t1 := f.NewLocal("t1", ts.IntT(), ir.LTemp)
			return t1, &ir.Assign{Dst: t1, Src: x}
		}, false},
		{"rooted local", func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			return f.NewLocal("x", ts.IntT(), ir.LVar), nil
		}, true},
		{"boxed local", func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			x := f.NewLocal("x", ts.IntT(), ir.LVar)
			x.Boxed = true
			return x, nil
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, alone := range []bool{false, true} {
				f := foldFunc(func(f *ir.Func, ts *ir.Types, b *ir.Block) {
					first, def := tc.first(f, ts)
					if def != nil {
						b.Instrs = append(b.Instrs, def)
					}
					t2 := f.NewLocal("t2", ts.IntT(), ir.LTemp)
					b.Instrs = append(b.Instrs, call("next", t2), call("consume", nil, first, t2))
				})
				p := &toyProbe{}
				fs := Analyze(f, FoldOptions{CallAlone: alone, Order: LeftToRight}, p, func(fs *Folds) { p.fs = fs })
				want := !alone || tc.want
				if got := fs.Folded(f.Blocks[0].Instrs[len(f.Blocks[0].Instrs)-2]); got != want {
					t.Errorf("CallAlone %t: folded %t, want %t", alone, got, want)
				}
			}
		})
	}
}

func TestFoldKeepsEffectsInPlace(t *testing.T) {
	f := foldFunc(func(f *ir.Func, ts *ir.Types, b *ir.Block) {
		x := f.NewLocal("x", ts.IntT(), ir.LVar)
		t1 := f.NewLocal("t1", ts.IntT(), ir.LTemp)
		t2 := f.NewLocal("t2", ts.IntT(), ir.LTemp)
		b.Instrs = []ir.Instr{
			call("f", t1),
			call("log", nil),
			&ir.BinOp{Dst: t2, Op: ir.Add, X: x, Y: &ir.Const{}},
			&ir.Assign{Dst: x, Src: &ir.Const{}},
			call("h", nil, t1, t2),
		}
	})
	got := strings.Join(folded(f, LeftToRight), "|")
	want := "t1 = f()|log()|t2 = x + 1|x = 1|h(t1, t2)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFoldPureAcrossStatements(t *testing.T) {
	f := foldFunc(func(f *ir.Func, ts *ir.Types, b *ir.Block) {
		x := f.NewLocal("x", ts.IntT(), ir.LVar)
		t1 := f.NewLocal("t1", ts.IntT(), ir.LTemp)
		b.Instrs = []ir.Instr{
			&ir.BinOp{Dst: t1, Op: ir.Add, X: x, Y: &ir.Const{}},
			call("log", nil),
			call("h", nil, t1),
		}
	})
	got := strings.Join(folded(f, LeftToRight), "|")
	if want := "log()|h(x + 1)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestIrreducible checks the dispatch fallback: a loop with two entries
// cannot be structured, so its blocks become dispatch entries.
func TestIrreducible(t *testing.T) {
	f := &ir.Func{Name: "irreducible"}
	ts := ir.NewTypes()
	b0, b1, b2, b3 := f.NewBlock(""), f.NewBlock(""), f.NewBlock(""), f.NewBlock("")
	for _, b := range []*ir.Block{b0, b1, b2, b3} {
		b.Instrs = []ir.Instr{&ir.Print{}}
	}
	c0 := f.NewLocal("c0", ts.Bool(), ir.LTemp)
	c1 := f.NewLocal("c1", ts.Bool(), ir.LTemp)
	b0.Term = &ir.If{Cond: c0, Then: b1, Else: b2}
	b1.Term = &ir.If{Cond: c1, Then: b2, Else: b3}
	b2.Term = &ir.Jump{Target: b1}
	b3.Term = &ir.Return{}
	body := Build(f, Options{})
	if body.Dispatch == nil {
		t.Fatalf("irreducible graph structured without a dispatch:\n%s", Dump(body))
	}
	for run := int64(0); run < 20; run++ {
		want := runGraph(f, &oracle{seed: run, visits: map[*ir.Block]int{}})
		got := runBody(f, body, &oracle{seed: run, visits: map[*ir.Block]int{}})
		if strings.Join(want, " ") != strings.Join(got, " ") {
			t.Fatalf("run %d: want %v got %v\n%s", run, want, got, Dump(body))
		}
	}
	py := Resolve(Print(body, pySyntax, dumpEmitter{}, 0))
	if !strings.Contains(py, "_pc == ") {
		t.Errorf("python dispatch missing:\n%s", py)
	}
}

// TestPauseAlone checks that a pause folds only into a use that holds no
// earlier operand across it in a temporary, except locals no other task
// writes.
func TestPauseAlone(t *testing.T) {
	cases := []struct {
		first      func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr)
		alone, any bool
	}{
		{func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			t1 := f.NewLocal("t1", ts.IntT(), ir.LTemp)
			return t1, call("alloc", t1)
		}, false, true},
		{func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			return f.NewLocal("x", ts.IntT(), ir.LVar), nil
		}, true, true},
		{func(f *ir.Func, ts *ir.Types) (ir.Value, ir.Instr) {
			x := f.NewLocal("x", ts.IntT(), ir.LVar)
			x.Boxed = true
			return x, nil
		}, false, true},
	}
	for i, c := range cases {
		for _, alone := range []bool{false, true} {
			f := &ir.Func{Name: "f"}
			ts := ir.NewTypes()
			b0, b1 := f.NewBlock(""), f.NewBlock("")
			first, def := c.first(f, ts)
			if def != nil {
				b0.Instrs = []ir.Instr{def}
			}
			t2 := f.NewLocal("t2", ts.IntT(), ir.LTemp)
			pause := &ir.Pause{Op: &ir.Call{Kind: ir.CallStatic, Func: &ir.Func{Name: "g"}, Dsts: []*ir.Local{t2}}, Next: b1}
			b0.Term = pause
			b1.Instrs = []ir.Instr{call("h", nil, first, t2)}
			b1.Term = &ir.Return{}
			p := &toyProbe{}
			fs := Analyze(f, FoldOptions{Inline: true, PauseAlone: alone, Order: LeftToRight}, p, func(fs *Folds) { p.fs = fs })
			want := c.any
			if alone {
				want = c.alone
			}
			if got := fs.FoldedPause(pause); got != want {
				t.Errorf("case %d, PauseAlone %t: folded %t, want %t (%s)", i, alone, got, want, Resolve(p.Instr(b1.Instrs[0])))
			}
		}
	}
}
