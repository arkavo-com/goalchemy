package structure

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// oracle decides branch conditions by block and visit count, so a block
// graph and its structured form can be run on the same inputs.
type oracle struct {
	seed   int64
	visits map[*ir.Block]int
}

func (o *oracle) cond(b *ir.Block) bool {
	n := o.visits[b]
	o.visits[b] = n + 1
	r := rand.New(rand.NewSource(o.seed*7919 + int64(b.ID)*104729 + int64(n)))
	return r.Intn(3) != 0
}

const steps = 200

// runGraph executes f's blocks, recording the blocks with instructions and
// every pause, until a leaf terminator or the step limit.
func runGraph(f *ir.Func, o *oracle) []string {
	var trace []string
	b := f.Blocks[0]
	for fuel := 5000; len(trace) < steps && fuel > 0; fuel-- {
		if len(b.Instrs) > 0 {
			trace = append(trace, fmt.Sprint(b.ID))
		}
		switch t := b.Term.(type) {
		case *ir.Jump:
			b = t.Target
		case *ir.If:
			if o.cond(b) {
				b = t.Then
			} else {
				b = t.Else
			}
		case *ir.Pause:
			trace = append(trace, fmt.Sprintf("pause%d", t.Next.ID))
			b = t.Next
		default:
			return append(trace, "end")
		}
	}
	if len(trace) > steps {
		trace = trace[:steps]
	}
	return trace
}

type signal struct {
	brk, cont Stmt
	goTo      *ir.Block
	end       bool
}

type runner struct {
	fuel  int
	o     *oracle
	conds map[ir.Value]*ir.Block
	trace []string
}

func (r *runner) full() bool {
	r.fuel--
	return len(r.trace) >= steps || r.fuel < 0
}

func (r *runner) eval(c Cond) bool {
	switch c := c.(type) {
	case Value:
		return r.o.cond(r.conds[c.V])
	case Not:
		return !r.eval(c.X)
	case And:
		return r.eval(c.X) && r.eval(c.Y)
	case Or:
		return r.eval(c.X) || r.eval(c.Y)
	}
	panic("cond")
}

func (r *runner) seq(ss []Stmt) *signal {
	for _, s := range ss {
		if r.full() {
			return &signal{end: true}
		}
		if sig := r.stmt(s); sig != nil {
			return sig
		}
	}
	return nil
}

func (r *runner) stmt(s Stmt) *signal {
	switch s := s.(type) {
	case *Code:
		if len(s.Block.Instrs) > 0 {
			r.trace = append(r.trace, fmt.Sprint(s.Block.ID))
		}
	case *Leaf:
		if p, ok := s.Term.(*ir.Pause); ok {
			r.trace = append(r.trace, fmt.Sprintf("pause%d", p.Next.ID))
			return &signal{goTo: p.Next}
		}
		r.trace = append(r.trace, "end")
		return &signal{end: true}
	case *Pause:
		r.trace = append(r.trace, fmt.Sprintf("pause%d", s.Pause.Next.ID))
	case *If:
		if r.eval(s.Cond) {
			return r.seq(s.Then)
		}
		return r.seq(s.Else)
	case *Loop:
		for {
			if r.full() {
				return &signal{end: true}
			}
			if s.While != nil && !r.eval(s.While) {
				return nil
			}
			sig := r.seq(s.Body)
			switch {
			case sig == nil, sig.cont == s:
			case sig.brk == s:
				return nil
			default:
				return sig
			}
		}
	case *Block:
		sig := r.seq(s.Body)
		if sig != nil && sig.brk == s {
			return nil
		}
		return sig
	case *Break:
		return &signal{brk: s.Target}
	case *Continue:
		return &signal{cont: s.Target}
	case *Goto:
		return &signal{goTo: s.Target}
	case *Dispatch:
		entry := s.Entries[0]
		for !r.full() {
			sig := r.seq(entry.Body)
			if sig == nil || sig.goTo == nil {
				if sig == nil {
					panic("dispatch entry completed")
				}
				return sig
			}
			found := false
			for _, e := range s.Entries {
				if e.Head == sig.goTo {
					entry, found = e, true
				}
			}
			if !found {
				panic(fmt.Sprintf("goto to unknown entry b%d", sig.goTo.ID))
			}
		}
		return &signal{end: true}
	}
	return nil
}

func runBody(f *ir.Func, body *Body, o *oracle) []string {
	r := &runner{fuel: 20000, o: o, conds: map[ir.Value]*ir.Block{}}
	for _, b := range f.Blocks {
		if i, ok := b.Term.(*ir.If); ok {
			r.conds[i.Cond] = b
		}
	}
	sig := r.seq(body.Stmts)
	for sig != nil && sig.goTo == f.Blocks[0] && !r.full() {
		sig = r.seq(body.Stmts)
	}
	if sig != nil && sig.goTo != nil && !r.full() {
		panic("goto outside dispatch")
	}
	if len(r.trace) > steps {
		r.trace = r.trace[:steps]
	}
	return r.trace
}

// randomFunc builds a block graph: arbitrary jumps and branches, including
// irreducible ones, some empty blocks (short-circuit candidates), and pauses.
func randomFunc(r *rand.Rand, n int, pauses bool) *ir.Func {
	f := &ir.Func{Name: "random"}
	ts := ir.NewTypes()
	for i := 0; i < n; i++ {
		b := f.NewBlock("")
		if r.Intn(3) != 0 {
			b.Instrs = []ir.Instr{&ir.Print{}}
		}
	}
	for i, b := range f.Blocks {
		pick := func() *ir.Block {
			if r.Intn(3) == 0 {
				return f.Blocks[r.Intn(n)]
			}
			if i+1 < n {
				return f.Blocks[i+1+r.Intn(n-i-1)]
			}
			return f.Blocks[r.Intn(n)]
		}
		switch k := r.Intn(10); {
		case i == n-1 || k == 0:
			b.Term = &ir.Return{}
		case k < 4:
			b.Term = &ir.Jump{Target: pick()}
		case k == 4 && pauses:
			b.Term = &ir.Pause{Op: &ir.Send{}, Next: pick()}
		default:
			c := f.NewLocal(fmt.Sprintf("c%d", i), ts.Bool(), ir.LTemp)
			b.Term = &ir.If{Cond: c, Then: pick(), Else: pick()}
		}
	}
	return f
}

func TestRandomGraphs(t *testing.T) {
	for seed := int64(0); seed < 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := 2 + r.Intn(14)
		pauses := seed%3 == 0
		f := randomFunc(r, n, pauses)
		for _, inline := range []bool{false, true} {
			if inline && !pauses {
				continue
			}
			body := Build(f, Options{Inline: inline})
			for run := int64(0); run < 4; run++ {
				want := runGraph(f, &oracle{seed: run, visits: map[*ir.Block]int{}})
				got := runBody(f, body, &oracle{seed: run, visits: map[*ir.Block]int{}})
				k := len(want)
				if len(got) < k {
					k = len(got)
				}
				if k < 10 && len(want) != len(got) {
					t.Fatalf("seed %d: traces too short to compare: %v %v", seed, want, got)
				}
				if strings.Join(want[:k], " ") != strings.Join(got[:k], " ") {
					t.Fatalf("seed %d inline %v run %d:\nwant %v\ngot  %v\n%s", seed, inline, run, want, got, dumpGraph(f)+Dump(body))
				}
			}
		}
	}
}

func dumpGraph(f *ir.Func) string {
	var b strings.Builder
	for _, bl := range f.Blocks {
		fmt.Fprintf(&b, "b%d (%d instrs): ", bl.ID, len(bl.Instrs))
		switch t := bl.Term.(type) {
		case *ir.Jump:
			fmt.Fprintf(&b, "jump b%d", t.Target.ID)
		case *ir.If:
			fmt.Fprintf(&b, "if b%d b%d", t.Then.ID, t.Else.ID)
		case *ir.Pause:
			fmt.Fprintf(&b, "pause b%d", t.Next.ID)
		default:
			b.WriteString("return")
		}
		b.WriteString("\n")
	}
	return b.String()
}
