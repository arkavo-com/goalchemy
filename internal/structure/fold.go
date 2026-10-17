package structure

import (
	"fmt"
	"go/constant"
	"sort"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// Probe renders a function's statements for fold analysis. While Analyze
// runs, and while Folds renders a folded expression, the target must route
// local names through Folds.Name and operand reads through Folds.Use.
type Probe interface {
	Instr(in ir.Instr) string
	// Pause renders an inline pause point (native pause model).
	Pause(p *ir.Pause) string
	Cond(v ir.Value) string
	Leaf(b *ir.Block, t ir.Terminator) string
	// Assign returns the text before and after the expression of a
	// statement that assigns l.
	Assign(l *ir.Local) (prefix, suffix string)
}

// Order says how a target sequences the operands of one expression.
type Order int

const (
	// Unsequenced operands may evaluate in any order (C).
	Unsequenced Order = iota
	// CallsInOrder evaluates calls left to right but may reorder other
	// operations that can panic around them (Go).
	CallsInOrder
	// LeftToRight evaluates every operand left to right.
	LeftToRight
)

// FoldOptions configures Analyze.
type FoldOptions struct {
	// Inline continues straight-line sequences through pause points.
	Inline bool
	// PauseAlone folds a pause only into a use that evaluates nothing
	// before it that a temporary would hold across the pause: the target's
	// collector sees locals, not temporaries.
	PauseAlone bool
	// CallAlone applies the temporary-root rule to synchronous calls too.
	CallAlone bool
	Order     Order
}

// Folds records the single-use temporaries a target prints inside the
// expression that uses them, instead of as their own statements. A nil
// *Folds folds nothing.
type Folds struct {
	pr     Probe
	opt    FoldOptions
	def    map[*ir.Local]item
	folded map[*ir.Local]bool
	skip   map[any]bool
	names  map[*ir.Local]string
	uses   map[*ir.Local]string
	exprs  map[*ir.Local]string
}

// item is a statement position: an instruction, an inline pause, or a
// block's terminator.
type item struct {
	in    ir.Instr
	pause *ir.Pause
	term  *ir.Block
}

func (it item) key() any {
	switch {
	case it.in != nil:
		return it.in
	case it.pause != nil:
		return it.pause
	}
	return it.term
}

// Name returns the substitute name of l while probing or rendering a folded
// expression.
func (fs *Folds) Name(l *ir.Local) (string, bool) {
	if fs == nil {
		return "", false
	}
	s, ok := fs.names[l]
	return s, ok
}

// Use returns the text of a read of l when it is a folded temporary or
// being probed.
func (fs *Folds) Use(l *ir.Local) (string, bool) {
	if fs == nil {
		return "", false
	}
	if s, ok := fs.uses[l]; ok {
		return s, true
	}
	if _, ok := fs.def[l]; !ok || !fs.folded[l] {
		return "", false
	}
	if s, ok := fs.exprs[l]; ok {
		return s, true
	}
	s, ok := fs.render(l)
	if !ok {
		panic(fmt.Sprintf("structure: folded %s no longer renders as an expression", l.Name))
	}
	fs.exprs[l] = s
	return s, true
}

// Def returns the instruction defining a folded temporary.
func (fs *Folds) Def(l *ir.Local) (ir.Instr, bool) {
	if fs == nil || !fs.folded[l] {
		return nil, false
	}
	it, ok := fs.def[l]
	return it.in, ok && it.in != nil
}

// Folded reports whether an instruction is printed inside its use.
func (fs *Folds) Folded(in ir.Instr) bool { return fs != nil && fs.skip[in] }

// FoldedPause reports whether an inline pause point is printed inside its use.
func (fs *Folds) FoldedPause(p *ir.Pause) bool { return fs != nil && fs.skip[p] }

// Temp reports whether l is a folded temporary, which needs no storage.
func (fs *Folds) Temp(l *ir.Local) bool { return fs != nil && fs.folded[l] }

// Empty reports whether b prints no statements of its own.
func (fs *Folds) Empty(b *ir.Block, inline bool) bool {
	if b.ResumeOf != nil && !inline {
		return false
	}
	for _, in := range b.Instrs {
		if !fs.Folded(in) {
			return false
		}
	}
	return true
}

// marks lists the probe sentinel ids in s.
func marks(s string) []int {
	var out []int
	for i := 0; i < len(s); i++ {
		if s[i] != nameMark && s[i] != useMark {
			continue
		}
		j := strings.IndexByte(s[i+1:], s[i])
		if j < 0 {
			break
		}
		var n int
		fmt.Sscanf(s[i+1:i+1+j], "%d", &n)
		out = append(out, n)
		i += j + 1
	}
	return out
}

func sentinel(kind byte, i int) string { return fmt.Sprintf("%c%d%c", kind, i, kind) }

const (
	nameMark = '\x04'
	useMark  = '\x05'
)

func (fs *Folds) render(l *ir.Local) (string, bool) {
	it := fs.def[l]
	saveN, saveU := fs.names, fs.uses
	fs.names = map[*ir.Local]string{l: sentinel(nameMark, 0)}
	fs.uses = map[*ir.Local]string{l: sentinel(useMark, 0)}
	defer func() { fs.names, fs.uses = saveN, saveU }()
	text := fs.text(it)
	pre, suf := fs.pr.Assign(l)
	return extract(text, pre, suf)
}

func (fs *Folds) text(it item) string {
	switch {
	case it.in != nil:
		return fs.pr.Instr(it.in)
	case it.pause != nil:
		return fs.pr.Pause(it.pause)
	}
	switch t := it.term.Term.(type) {
	case *ir.If:
		return fs.pr.Cond(t.Cond)
	case *ir.Panic, *ir.Pause:
		return fs.pr.Leaf(it.term, t)
	}
	return ""
}

func extract(text, pre, suf string) (string, bool) {
	text = strings.TrimSpace(stripMarks(text))
	pre, suf = strings.TrimSpace(pre), strings.TrimSpace(suf)
	if !strings.HasPrefix(text, pre) || !strings.HasSuffix(text, suf) || len(text) < len(pre)+len(suf) {
		return "", false
	}
	expr := strings.TrimSpace(text[len(pre) : len(text)-len(suf)])
	if expr == "" || strings.ContainsAny(expr, "\n\x04") || !balanced(expr) {
		return "", false
	}
	return Wrap(expr), true
}

func balanced(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			i = skipString(s, i) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth < 0 {
				return false
			}
		case ';':
			if depth == 0 {
				return false
			}
		}
	}
	return depth == 0
}

type cand struct {
	t      *ir.Local
	seq    []item
	d, s   int
	effect bool
	call   bool
	reads  []*ir.Local
	pos    int
}

// Analyze decides which temporaries of f to fold for the target behind pr,
// which must already route names and reads through the returned Folds; set
// receives it before probing starts.
func Analyze(f *ir.Func, opt FoldOptions, pr Probe, set func(*Folds)) *Folds {
	fs := &Folds{pr: pr, opt: opt, def: map[*ir.Local]item{}, folded: map[*ir.Local]bool{}, skip: map[any]bool{}, exprs: map[*ir.Local]string{}}
	set(fs)
	defs, uses := map[*ir.Local]int{}, map[*ir.Local]int{}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			for _, l := range Writes(in) {
				defs[l]++
			}
			for _, l := range locals(Reads(in)) {
				uses[l]++
			}
		}
		for _, l := range locals(termReads(b.Term)) {
			uses[l]++
		}
		if p, ok := b.Term.(*ir.Pause); ok {
			for _, l := range Writes(p.Op) {
				defs[l]++
			}
			for _, l := range locals(Reads(p.Op)) {
				uses[l]++
			}
		}
	}
	for _, l := range f.Results {
		uses[l]++
	}
	cont := map[*ir.Block]bool{}
	if opt.Inline {
		for _, b := range f.Blocks {
			if p, ok := b.Term.(*ir.Pause); ok {
				cont[p.Next] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				ws := Writes(in)
				if fs.skip[in] || len(ws) != 1 {
					continue
				}
				l := ws[0]
				if uses[l] != 0 || defs[l] != 1 || l.Kind != ir.LTemp || l.Boxed {
					continue
				}
				if c := classify(item{in: in}); c != nil && !c.effect {
					fs.skip[in] = true
					fs.folded[l] = true
					for _, r := range locals(Reads(in)) {
						uses[r]--
					}
					changed = true
				}
			}
		}
	}
	cands := map[*ir.Local]*cand{}
	var seqs [][]item
	for _, b := range f.Blocks {
		if cont[b] {
			continue
		}
		var seq []item
		for x := b; ; {
			for _, in := range x.Instrs {
				if !fs.skip[in] {
					seq = append(seq, item{in: in})
				}
			}
			p, ok := x.Term.(*ir.Pause)
			if !ok || !opt.Inline {
				seq = append(seq, item{term: x})
				break
			}
			seq = append(seq, item{pause: p})
			x = p.Next
		}
		seqs = append(seqs, seq)
		defAt := map[*ir.Local]int{}
		for i, it := range seq {
			for _, l := range locals(itemReads(it)) {
				d, ok := defAt[l]
				if !ok || defs[l] != 1 || uses[l] != 1 || l.Kind != ir.LTemp || l.Boxed {
					continue
				}
				c := classify(seq[d])
				if c == nil {
					continue
				}
				c.t, c.seq, c.d, c.s = l, seq, d, i
				cands[l] = c
			}
			if w := itemDst(it); w != nil {
				defAt[w] = i
			}
		}
	}
	if len(cands) == 0 {
		return fs
	}
	ids := map[*ir.Local]int{}
	var order []*ir.Local
	for l := range cands {
		order = append(order, l)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].ID < order[j].ID })
	fs.names, fs.uses = map[*ir.Local]string{}, map[*ir.Local]string{}
	for i, l := range order {
		ids[l] = i + 1
		fs.names[l] = sentinel(nameMark, i+1)
		fs.uses[l] = sentinel(useMark, i+1)
	}
	texts := map[any]string{}
	seen := map[*ir.Local][]any{}
	for _, seq := range seqs {
		has := false
		for _, it := range seq {
			if w := itemDst(it); w != nil && cands[w] != nil {
				has = true
			}
		}
		if !has {
			continue
		}
		for _, it := range seq {
			t := fs.text(it)
			texts[it.key()] = t
			for _, id := range marks(t) {
				if id > 0 && id <= len(order) {
					seen[order[id-1]] = append(seen[order[id-1]], it.key())
				}
			}
		}
	}
	for _, l := range order {
		c := cands[l]
		dk, sk := c.seq[c.d].key(), c.seq[c.s].key()
		pre, suf := pr.Assign(l)
		expr, ok := extract(texts[dk], pre, suf)
		ok = ok && !strings.Contains(expr, fs.uses[l])
		st := texts[sk]
		ok = ok && strings.Count(st, fs.uses[l]) == 1 && !strings.Contains(st, fs.names[l])
		for _, k := range seen[l] {
			ok = ok && (k == dk || k == sk)
		}
		if ok {
			c.pos = strings.Index(st, fs.uses[l])
		} else {
			delete(cands, l)
		}
	}
	fs.names, fs.uses = nil, nil
	for changed := true; changed; {
		changed = false
		byDef := map[any]*cand{}
		bySink := map[any][]*cand{}
		for _, c := range cands {
			byDef[c.seq[c.d].key()] = c
		}
		for _, c := range cands {
			if len(c.effects(cands)) > 0 {
				bySink[c.seq[c.s].key()] = append(bySink[c.seq[c.s].key()], c)
			}
		}
		root := func(c *cand) any {
			for {
				up, ok := byDef[c.seq[c.s].key()]
				if !ok {
					return c.seq[c.s].key()
				}
				c = up
			}
		}
		var drop []*ir.Local
		for _, c := range cands {
			if !c.valid(cands, byDef, bySink[c.seq[c.s].key()], root, opt) {
				drop = append(drop, c.t)
			}
		}
		for _, l := range drop {
			delete(cands, l)
			changed = true
		}
	}
	for l, c := range cands {
		fs.folded[l] = true
		fs.def[l] = c.seq[c.d]
		fs.skip[c.seq[c.d].key()] = true
	}
	return fs
}

// valid checks a candidate against the current fold set: a pure expression
// may move past statements that do not write what it reads; an expression
// with effects only past statements folded into the same use; its use must
// accept an operand with effects; and the target's operand order must keep
// the source order of effects.
func (c *cand) valid(cands map[*ir.Local]*cand, byDef map[any]*cand, siblings []*cand, root func(*cand) any, opt FoldOptions) bool {
	order := opt.Order
	if (opt.PauseAlone && c.seq[c.d].pause != nil || opt.CallAlone && c.call) && !c.alone(cands, byDef) {
		return false
	}
	reads := c.treeReads(cands)
	effects := c.effects(cands)
	for k := c.d + 1; k < c.s; k++ {
		it := c.seq[k]
		other, folded := byDef[it.key()]
		if !folded {
			if len(effects) > 0 {
				return false
			}
			for _, w := range itemWrites(it) {
				for _, r := range reads {
					if w == r {
						return false
					}
				}
			}
			continue
		}
		if len(effects) > 0 && len(other.effects(cands)) > 0 && root(other) != root(c) {
			return false
		}
	}
	if len(effects) == 0 {
		return true
	}
	if !effectSink(c.seq[c.s]) {
		return false
	}
	first := c.firstEffect(cands)
	calls := true
	for _, o := range siblings {
		for _, e := range o.effects(cands) {
			calls = calls && e.call
		}
	}
	for _, o := range siblings {
		if o == c {
			continue
		}
		of := o.firstEffect(cands)
		if (of < first) != (o.pos < c.pos) {
			return false
		}
		if of > first && (order == Unsequenced || order == CallsInOrder && !calls) {
			return false
		}
	}
	return true
}

// alone reports whether the uses that a folded pause c is printed in, up
// to the statement or pause containing it, evaluate no folded expression
// before the pause and read only locals no other task writes, whose
// storage keeps their values across it.
func (c *cand) alone(cands map[*ir.Local]*cand, byDef map[any]*cand) bool {
	for x := c; ; {
		sink := x.seq[x.s]
		for _, l := range locals(itemReads(sink)) {
			if o, ok := cands[l]; ok {
				if o != x && o.pos < x.pos {
					return false
				}
			} else if l.Boxed || l.Kind == ir.LEnv {
				return false
			}
		}
		up, ok := byDef[sink.key()]
		if !ok || sink.pause != nil {
			return true
		}
		x = up
	}
}

func (c *cand) firstEffect(cands map[*ir.Local]*cand) int {
	first := -1
	for _, e := range c.effects(cands) {
		if first < 0 || e.d < first {
			first = e.d
		}
	}
	return first
}

// treeReads lists the locals an expression reads, including those of the
// temporaries folded into it.
func (c *cand) treeReads(cands map[*ir.Local]*cand) []*ir.Local {
	var out []*ir.Local
	for _, l := range c.reads {
		if o, ok := cands[l]; ok {
			out = append(out, o.treeReads(cands)...)
			continue
		}
		out = append(out, l)
	}
	return out
}

func (c *cand) effects(cands map[*ir.Local]*cand) []*cand {
	var out []*cand
	if c.effect {
		out = append(out, c)
	}
	for _, l := range c.reads {
		if o, ok := cands[l]; ok {
			out = append(out, o.effects(cands)...)
		}
	}
	return out
}

// classify returns a candidate for a defining statement that targets can
// print as one expression, or nil.
func classify(it item) *cand {
	c := &cand{}
	if it.pause != nil {
		call, ok := it.pause.Op.(*ir.Call)
		if !ok || len(call.Dsts) != 1 || call.Kind == ir.CallExtern {
			return nil
		}
		c.effect, c.call = true, true
		c.reads = locals(Reads(call))
		return c
	}
	if it.in == nil {
		return nil
	}
	c.reads = locals(Reads(it.in))
	pure := true
	for _, v := range Reads(it.in) {
		if l, ok := v.(*ir.Local); ok && (l.Boxed || l.Kind == ir.LEnv) {
			pure = false
		}
	}
	switch i := it.in.(type) {
	case *ir.Assign, *ir.UnOp, *ir.MakeInterface, *ir.Len, *ir.Cap:
	case *ir.BinOp:
		pure = pure && pureBinOp(i)
	case *ir.Convert:
		pure = pure && i.Kind != ir.ConvSliceToArray
	case *ir.Load:
		switch i.Place.Root.(type) {
		case ir.LocalRoot, ir.ValueRoot:
			pure = pure && !i.Place.HasElement()
		default:
			pure = false
		}
	case *ir.Call:
		if len(i.Dsts) != 1 || i.Suspends {
			return nil
		}
		pure, c.call = false, true
	case *ir.IndexString, *ir.SliceOp, *ir.Append, *ir.MakeSlice, *ir.MakeMap, *ir.MakeChan, *ir.New:
		pure = false
	case *ir.MapLookup:
		if i.Ok != nil {
			return nil
		}
		pure = false
	case *ir.TypeAssert:
		if i.Ok != nil {
			return nil
		}
		pure = false
	default:
		return nil
	}
	c.effect = !pure
	return c
}

func pureBinOp(i *ir.BinOp) bool {
	switch i.Op {
	case ir.Div, ir.Rem:
		return i.X.IRType().U().Kind == ir.KFloat
	case ir.Shl, ir.Shr:
		c, ok := i.Y.(*ir.Const)
		return ok && c.Val != nil && constant.Sign(c.Val) >= 0
	case ir.Eq, ir.Ne:
		return !mayHoldInterface(i.X.IRType(), map[*ir.Type]bool{})
	}
	return true
}

func mayHoldInterface(t *ir.Type, seen map[*ir.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	u := t.U()
	switch u.Kind {
	case ir.KInterface:
		return true
	case ir.KArray:
		return mayHoldInterface(u.Elem, seen)
	case ir.KStruct:
		for _, f := range u.Fields {
			if mayHoldInterface(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

// effectSink reports whether a statement evaluates an operand that has
// effects before any effect of its own on every target.
func effectSink(it item) bool {
	if it.pause != nil {
		c, ok := it.pause.Op.(*ir.Call)
		return ok && (c.Kind == ir.CallStatic && c.Func.Recv == nil || c.Kind == ir.CallExtern)
	}
	if it.term != nil {
		switch it.term.Term.(type) {
		case *ir.If, *ir.Panic:
			return true
		}
		return false
	}
	switch i := it.in.(type) {
	case *ir.Assign, *ir.DeclVar, *ir.UnOp, *ir.BinOp, *ir.Convert, *ir.MakeInterface, *ir.Len, *ir.Cap,
		*ir.Append, *ir.IndexString, *ir.MapLookup, *ir.MapStore, *ir.Print, *ir.MakeSlice, *ir.MakeChan:
		return true
	case *ir.Store:
		_, ok := i.Place.Root.(ir.LocalRoot)
		return ok && !i.Place.HasElement()
	case *ir.Call:
		return i.Kind == ir.CallStatic && i.Func.Recv == nil || i.Kind == ir.CallExtern
	}
	return false
}

func locals(vs []ir.Value) []*ir.Local {
	var out []*ir.Local
	for _, v := range vs {
		if l, ok := v.(*ir.Local); ok {
			out = append(out, l)
		}
	}
	return out
}

func itemReads(it item) []ir.Value {
	switch {
	case it.in != nil:
		return Reads(it.in)
	case it.pause != nil:
		return Reads(it.pause.Op)
	}
	return termReads(it.term.Term)
}

func itemWrites(it item) []*ir.Local {
	switch {
	case it.in != nil:
		return Writes(it.in)
	case it.pause != nil:
		return Writes(it.pause.Op)
	}
	return nil
}

func itemDst(it item) *ir.Local {
	ws := itemWrites(it)
	if len(ws) == 1 {
		return ws[0]
	}
	return nil
}

func termReads(t ir.Terminator) []ir.Value {
	switch t := t.(type) {
	case *ir.If:
		return []ir.Value{t.Cond}
	case *ir.Panic:
		return []ir.Value{t.X}
	case *ir.Pause:
		return Reads(t.Op)
	}
	return nil
}

// Reads lists the operands an instruction reads, including locals it reads
// through their storage.
func Reads(in ir.Instr) []ir.Value {
	var out []ir.Value
	vals := func(vs ...ir.Value) {
		for _, v := range vs {
			if v != nil {
				out = append(out, v)
			}
		}
	}
	place := func(p *ir.Place) {
		switch r := p.Root.(type) {
		case ir.LocalRoot:
			vals(r.Local)
		case ir.DerefRoot:
			vals(r.Ptr)
		case ir.SliceRoot:
			vals(r.Slice, r.Index)
		case ir.ValueRoot:
			vals(r.Value)
		}
		for _, pr := range p.Path {
			vals(pr.Index)
		}
	}
	call := func(c *ir.Call) {
		vals(c.Fn, c.Recv)
		vals(c.Args...)
	}
	switch i := in.(type) {
	case *ir.DeclVar:
		vals(i.Init)
	case *ir.Assign:
		vals(i.Src)
	case *ir.Zero, *ir.New, *ir.MakeMap, *ir.Recover:
	case *ir.Load:
		place(i.Place)
	case *ir.Store:
		place(i.Place)
		vals(i.V)
	case *ir.AddrOf:
		place(i.Place)
	case *ir.UnOp:
		vals(i.X)
	case *ir.BinOp:
		vals(i.X, i.Y)
	case *ir.Convert:
		vals(i.X)
	case *ir.MakeInterface:
		vals(i.X)
	case *ir.TypeAssert:
		vals(i.X)
	case *ir.Call:
		call(i)
	case *ir.Defer:
		call(i.Call)
	case *ir.Go:
		call(i.Call)
	case *ir.MakeClosure:
		for _, l := range i.Env {
			vals(l)
		}
	case *ir.MakeBound:
		vals(i.Recv)
	case *ir.MakeIfaceBound:
		vals(i.Recv)
	case *ir.Len:
		vals(i.X)
	case *ir.Cap:
		vals(i.X)
	case *ir.MakeSlice:
		vals(i.Len, i.Cap)
	case *ir.Append:
		vals(i.S, i.Spread)
		vals(i.Elems...)
	case *ir.Copy:
		vals(i.Dst, i.Src)
	case *ir.IndexString:
		vals(i.S, i.I)
	case *ir.SliceOp:
		vals(i.X, i.Lo, i.Hi, i.Max)
	case *ir.MapLookup:
		vals(i.M, i.K)
	case *ir.MapStore:
		vals(i.M, i.K, i.V)
	case *ir.MapDelete:
		vals(i.M, i.K)
	case *ir.Clear:
		vals(i.X)
	case *ir.DecodeRune:
		vals(i.S, i.I)
	case *ir.MapIterInit:
		vals(i.M)
	case *ir.MapIterNext:
		vals(i.Iter)
	case *ir.Print:
		vals(i.Args...)
	case *ir.Rebind:
		vals(i.L)
	case *ir.MakeChan:
		vals(i.Size)
	case *ir.Send:
		vals(i.Ch, i.V)
	case *ir.Recv:
		vals(i.Ch)
	case *ir.Close:
		vals(i.Ch)
	case *ir.Select:
		for _, c := range i.Cases {
			vals(c.Ch, c.V)
		}
	case *ir.BoxParam:
		vals(i.L)
	default:
		panic(fmt.Sprintf("structure: instruction %T", in))
	}
	return out
}

// Writes lists the locals an instruction assigns, including a local whose
// storage it updates or whose address it takes.
func Writes(in ir.Instr) []*ir.Local {
	var out []*ir.Local
	ls := func(xs ...*ir.Local) {
		for _, l := range xs {
			if l != nil {
				out = append(out, l)
			}
		}
	}
	root := func(p *ir.Place) {
		if r, ok := p.Root.(ir.LocalRoot); ok {
			ls(r.Local)
		}
	}
	switch i := in.(type) {
	case *ir.DeclVar:
		ls(i.L)
	case *ir.Assign:
		ls(i.Dst)
	case *ir.Zero:
		ls(i.Dst)
	case *ir.Load:
		ls(i.Dst)
	case *ir.Store:
		root(i.Place)
	case *ir.AddrOf:
		ls(i.Dst)
		root(i.Place)
	case *ir.New:
		ls(i.Dst)
	case *ir.UnOp:
		ls(i.Dst)
	case *ir.BinOp:
		ls(i.Dst)
	case *ir.Convert:
		ls(i.Dst)
	case *ir.MakeInterface:
		ls(i.Dst)
	case *ir.TypeAssert:
		ls(i.Dst, i.Ok)
	case *ir.Call:
		ls(i.Dsts...)
	case *ir.MakeClosure:
		ls(i.Dst)
		ls(i.Env...)
	case *ir.MakeBound:
		ls(i.Dst)
	case *ir.MakeIfaceBound:
		ls(i.Dst)
	case *ir.Len:
		ls(i.Dst)
	case *ir.Cap:
		ls(i.Dst)
	case *ir.MakeSlice:
		ls(i.Dst)
	case *ir.MakeMap:
		ls(i.Dst)
	case *ir.Append:
		ls(i.Dst)
	case *ir.Copy:
		ls(i.N)
	case *ir.IndexString:
		ls(i.Dst)
	case *ir.SliceOp:
		ls(i.Dst)
	case *ir.MapLookup:
		ls(i.Dst, i.Ok)
	case *ir.DecodeRune:
		ls(i.Rune, i.Width)
	case *ir.MapIterInit:
		ls(i.Iter)
	case *ir.MapIterNext:
		ls(i.Ok, i.Key, i.Val, i.Iter)
	case *ir.Recover:
		ls(i.Dst)
	case *ir.Rebind:
		ls(i.L)
	case *ir.BoxParam:
		ls(i.L)
	case *ir.MakeChan:
		ls(i.Dst)
	case *ir.Recv:
		ls(i.Dst, i.Ok)
	case *ir.Select:
		ls(i.Index)
		for _, c := range i.Cases {
			ls(c.Dst, c.Ok)
		}
	case *ir.MapStore, *ir.MapDelete, *ir.Clear, *ir.Print, *ir.Defer, *ir.Go, *ir.Send, *ir.Close:
	default:
		panic(fmt.Sprintf("structure: instruction %T", in))
	}
	return out
}
