package structure

import (
	"sort"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// Options configures Build.
type Options struct {
	// Inline treats Pause terminators as straight-line operations that
	// continue at their Next block (native pause model). Otherwise every
	// pause continuation is a dispatch entry, reached when the task resumes.
	Inline bool
	// Empty reports whether a block prints no statements of its own, so its
	// condition can join a short-circuit condition of its predecessor.
	Empty func(*ir.Block) bool
	// ExitReturns wraps the body in a Block and turns Return leaves into
	// breaks out of it.
	ExitReturns bool
}

const (
	kJump = iota
	kIf
	kPause
	kLeaf
)

type node struct {
	b     *ir.Block
	kind  int
	succ  []*node
	cond  Cond
	preds []*node
	gone  bool

	rpo    int
	idom   *node
	body   map[*node]bool
	inner  *node
	parent *node
	kids   []*node
	merge  bool
	exit   bool
	done   bool
}

type builder struct {
	f      *ir.Func
	opt    Options
	heads  map[*ir.Block]bool
	nodes  map[*ir.Block]*node
	order  []*node
	disp   *Dispatch
	region []*node
	head   *node
}

type demote struct{ b *ir.Block }

// Build structures f's reachable blocks.
func Build(f *ir.Func, opt Options) *Body {
	if opt.Empty == nil {
		opt.Empty = func(b *ir.Block) bool { return len(b.Instrs) == 0 && (opt.Inline || b.ResumeOf == nil) }
	}
	heads := map[*ir.Block]bool{f.Blocks[0]: true}
	if !opt.Inline {
		for _, b := range f.Blocks {
			if p, ok := b.Term.(*ir.Pause); ok {
				heads[p.Next] = true
			}
		}
	}
	for {
		bd := &builder{f: f, opt: opt, heads: heads}
		body, d := bd.try()
		if d == nil {
			return body
		}
		heads[d] = true
	}
}

func (bd *builder) try() (body *Body, demoted *ir.Block) {
	defer func() {
		if r := recover(); r != nil {
			d, ok := r.(demote)
			if !ok {
				panic(r)
			}
			body, demoted = nil, d.b
		}
	}()
	bd.graph()
	if b := bd.partition(); b != nil {
		return nil, b
	}
	var hs []*node
	for _, n := range bd.order {
		if bd.heads[n.b] {
			hs = append(hs, n)
		}
	}
	if len(hs) > 1 {
		bd.disp = &Dispatch{}
	}
	var entries []*Entry
	for _, h := range hs {
		if b := bd.prepare(h); b != nil {
			return nil, b
		}
		entries = append(entries, &Entry{Head: h.b, Body: bd.doTree(h, nil)})
	}
	body = &Body{}
	if bd.disp != nil {
		bd.disp.Entries = entries
		body.Dispatch = bd.disp
		body.Stmts = []Stmt{bd.disp}
	} else {
		body.Stmts = entries[0].Body
	}
	if bd.opt.ExitReturns {
		exit := &Block{Body: body.Stmts}
		exitReturns(exit.Body, exit)
		body.Stmts = []Stmt{exit}
	}
	body.Stmts = simplify(body.Stmts)
	if body.Dispatch != nil {
		for _, e := range body.Dispatch.Entries {
			e.Body = simplify(e.Body)
		}
	}
	return body, nil
}

func (bd *builder) graph() {
	bd.nodes = map[*ir.Block]*node{}
	var visit func(b *ir.Block) *node
	visit = func(b *ir.Block) *node {
		if n := bd.nodes[b]; n != nil {
			return n
		}
		n := &node{b: b, kind: kLeaf}
		bd.nodes[b] = n
		switch t := b.Term.(type) {
		case *ir.Jump:
			n.kind = kJump
			n.succ = []*node{visit(t.Target)}
		case *ir.If:
			n.kind = kIf
			n.cond = Value{t.Cond}
			n.succ = []*node{visit(t.Then), visit(t.Else)}
		case *ir.Pause:
			visit(t.Next)
			if bd.opt.Inline {
				n.kind = kPause
				n.succ = []*node{bd.nodes[t.Next]}
			}
		}
		return n
	}
	visit(bd.f.Blocks[0])
	for _, b := range bd.f.Blocks {
		if n := bd.nodes[b]; n != nil {
			bd.order = append(bd.order, n)
		}
	}
}

// partition grows the head set until every non-head block is reached from
// exactly one head without passing another head, and returns a block that
// must become a head, or nil.
func (bd *builder) partition() *ir.Block {
	owner := map[*node]*node{}
	for _, h := range bd.order {
		if !bd.heads[h.b] {
			continue
		}
		owner[h] = h
		work := []*node{h}
		for len(work) > 0 {
			n := work[len(work)-1]
			work = work[:len(work)-1]
			for _, s := range n.succ {
				if bd.heads[s.b] {
					continue
				}
				switch owner[s] {
				case nil:
					owner[s] = h
					work = append(work, s)
				case h:
				default:
					return s.b
				}
			}
		}
	}
	return nil
}

// prepare computes the region headed by h: short-circuit conditions,
// reverse postorder, dominators, loops and the placement tree. It returns a
// block that must become a head when the region is irreducible.
func (bd *builder) prepare(h *node) *ir.Block {
	bd.head = h
	in := func(n *node) bool { return n == h || !bd.heads[n.b] }
	var region []*node
	seen := map[*node]bool{}
	var collect func(n *node)
	collect = func(n *node) {
		if seen[n] {
			return
		}
		seen[n] = true
		region = append(region, n)
		for _, s := range n.succ {
			if in(s) {
				collect(s)
			}
		}
	}
	collect(h)
	preds := func() {
		for _, n := range region {
			n.preds = nil
		}
		for _, n := range region {
			for _, s := range n.succ {
				if in(s) {
					s.preds = append(s.preds, n)
				}
			}
		}
	}
	preds()
	for changed := true; changed; {
		changed = false
		for _, a := range region {
			if a.gone || a.kind != kIf || a.succ[0] == a.succ[1] {
				continue
			}
			for side := 0; side < 2 && !changed; side++ {
				b := a.succ[side]
				if b == a || b == h || bd.heads[b.b] || b.kind != kIf || len(b.preds) != 1 || b.b.ResumeOf != nil || !bd.opt.Empty(b.b) {
					continue
				}
				other := a.succ[1-side]
				switch {
				case side == 0 && b.succ[1] == other:
					a.cond, a.succ[0] = And{a.cond, b.cond}, b.succ[0]
				case side == 0 && b.succ[0] == other:
					a.cond, a.succ[0] = And{a.cond, negate(b.cond)}, b.succ[1]
				case side == 1 && b.succ[0] == other:
					a.cond, a.succ[1] = Or{a.cond, b.cond}, b.succ[1]
				case side == 1 && b.succ[1] == other:
					a.cond, a.succ[1] = Or{a.cond, negate(b.cond)}, b.succ[0]
				default:
					continue
				}
				b.gone = true
				changed = true
			}
			if changed {
				break
			}
		}
		if changed {
			kept := region[:0]
			for _, n := range region {
				if !n.gone {
					kept = append(kept, n)
				}
			}
			region = kept
			preds()
		}
	}
	var post []*node
	visited := map[*node]bool{}
	var dfs func(n *node)
	dfs = func(n *node) {
		visited[n] = true
		for _, s := range n.succ {
			if in(s) && s != h && !visited[s] {
				dfs(s)
			}
		}
		post = append(post, n)
	}
	dfs(h)
	rpo := make([]*node, len(post))
	for i, n := range post {
		rpo[len(post)-1-i] = n
	}
	for i, n := range rpo {
		n.rpo = i
	}
	bd.region = rpo
	h.idom = h
	for changed := true; changed; {
		changed = false
		for _, n := range rpo[1:] {
			var d *node
			for _, p := range n.preds {
				if p.idom == nil {
					continue
				}
				if d == nil {
					d = p
				} else {
					d = intersect(p, d)
				}
			}
			if d != n.idom {
				n.idom = d
				changed = true
			}
		}
	}
	for _, n := range rpo {
		for _, s := range n.succ {
			if !in(s) || s.rpo > n.rpo {
				continue
			}
			if !dominates(s, n) {
				return s.b
			}
			if s.body == nil {
				s.body = map[*node]bool{s: true}
			}
			work := []*node{n}
			for len(work) > 0 {
				x := work[len(work)-1]
				work = work[:len(work)-1]
				if s.body[x] {
					continue
				}
				s.body[x] = true
				work = append(work, x.preds...)
			}
		}
	}
	var headers []*node
	for _, n := range rpo {
		if n.body != nil {
			headers = append(headers, n)
		}
	}
	for _, n := range rpo {
		for _, hd := range headers {
			if hd.body[n] && (n.inner == nil || len(hd.body) < len(n.inner.body)) {
				n.inner = hd
			}
		}
	}
	for _, n := range rpo {
		forward := 0
		for _, p := range n.preds {
			if p.rpo < n.rpo {
				forward++
			}
		}
		n.merge = forward > 1
	}
	for _, n := range rpo[1:] {
		d := n.idom
		var cand *node
		for l := d.inner; l != nil && !l.body[n]; l = outer(l) {
			cand = l
		}
		if cand != nil && (n.merge || !closed(n, in)) {
			n.parent, n.exit = cand, true
		} else {
			n.parent = d
		}
		n.parent.kids = append(n.parent.kids, n)
	}
	for _, n := range rpo {
		sort.Slice(n.kids, func(i, j int) bool { return n.kids[i].rpo > n.kids[j].rpo })
	}
	return nil
}

// closed reports whether the blocks n dominates end only in leaves, so n can
// stay where its dominator branches to it.
func closed(n *node, in func(*node) bool) bool {
	work := []*node{n}
	seen := map[*node]bool{n: true}
	for len(work) > 0 {
		x := work[len(work)-1]
		work = work[:len(work)-1]
		for _, s := range x.succ {
			if !in(s) || s.rpo <= x.rpo || s == n || !dominates(n, s) {
				return false
			}
			if !seen[s] {
				seen[s] = true
				work = append(work, s)
			}
		}
	}
	return true
}

func outer(l *node) *node {
	var best *node
	for _, x := range l.idomChain() {
		if x != l && x.body != nil && x.body[l] && (best == nil || len(x.body) < len(best.body)) {
			best = x
		}
	}
	return best
}

func (n *node) idomChain() []*node {
	var out []*node
	for x := n; ; x = x.idom {
		out = append(out, x)
		if x.idom == x || x.idom == nil {
			return out
		}
	}
}

func intersect(a, b *node) *node {
	for a != b {
		for a.rpo > b.rpo {
			a = a.idom
		}
		for b.rpo > a.rpo {
			b = b.idom
		}
	}
	return a
}

func dominates(a, b *node) bool {
	for x := b; ; x = x.idom {
		if x == a {
			return true
		}
		if x.idom == x || x.idom == nil {
			return false
		}
	}
}

type scope struct {
	loop   *node
	follow *node
	stmt   Stmt
}

func (bd *builder) doTree(x *node, ctx []scope) []Stmt {
	if x.done {
		panic(demote{x.b})
	}
	x.done = true
	var follows, exits []*node
	for _, k := range x.kids {
		switch {
		case x.body != nil && !x.body[k]:
			exits = append(exits, k)
		case k.merge || k.exit:
			follows = append(follows, k)
		}
	}
	if x.body == nil {
		return bd.within(x, follows, ctx, func(ctx []scope) []Stmt { return bd.code(x, ctx) })
	}
	return bd.within(x, exits, ctx, func(ctx []scope) []Stmt {
		l := &Loop{}
		l.Body = bd.within(x, follows, append(ctx, scope{loop: x, stmt: l}), func(ctx []scope) []Stmt { return bd.code(x, ctx) })
		return []Stmt{l}
	})
}

// within places ys (highest reverse postorder first) after the code that
// inner produces, each one after a Block that its branches break out of.
func (bd *builder) within(x *node, ys []*node, ctx []scope, inner func([]scope) []Stmt) []Stmt {
	if len(ys) == 0 {
		return inner(ctx)
	}
	y := ys[0]
	blk := &Block{}
	blk.Body = bd.within(x, ys[1:], append(ctx, scope{follow: y, stmt: blk}), inner)
	return append([]Stmt{blk}, bd.doTree(y, ctx)...)
}

func (bd *builder) code(x *node, ctx []scope) []Stmt {
	var out []Stmt
	if !bd.opt.Empty(x.b) {
		out = append(out, &Code{Block: x.b})
	}
	switch x.kind {
	case kJump:
		out = append(out, bd.branch(x, x.succ[0], ctx)...)
	case kIf:
		out = append(out, &If{Cond: x.cond, Then: bd.branch(x, x.succ[0], ctx), Else: bd.branch(x, x.succ[1], ctx), Pos: x.b.Term.Position()})
	case kPause:
		out = append(out, &Pause{Pause: x.b.Term.(*ir.Pause)})
		out = append(out, bd.branch(x, x.succ[0], ctx)...)
	default:
		out = append(out, &Leaf{Block: x.b, Term: x.b.Term})
	}
	return out
}

func (bd *builder) branch(x, y *node, ctx []scope) []Stmt {
	if bd.heads[y.b] && y != bd.head {
		return []Stmt{&Goto{Dispatch: bd.disp, Target: y.b}}
	}
	if y.rpo <= x.rpo {
		for i := len(ctx) - 1; i >= 0; i-- {
			if ctx[i].loop == y {
				return []Stmt{&Continue{Target: ctx[i].stmt.(*Loop)}}
			}
		}
		panic(demote{y.b})
	}
	for i := len(ctx) - 1; i >= 0; i-- {
		if ctx[i].follow == y {
			return []Stmt{&Break{Target: ctx[i].stmt}}
		}
	}
	if y.parent == x && !y.merge && !y.exit {
		return bd.doTree(y, ctx)
	}
	panic(demote{y.b})
}

func exitReturns(ss []Stmt, exit *Block) {
	for i, s := range ss {
		switch s := s.(type) {
		case *Leaf:
			if _, ok := s.Term.(*ir.Return); ok {
				ss[i] = &Break{Target: exit}
			}
		case *If:
			exitReturns(s.Then, exit)
			exitReturns(s.Else, exit)
		case *Loop:
			exitReturns(s.Body, exit)
		case *Block:
			exitReturns(s.Body, exit)
		case *Dispatch:
			for _, e := range s.Entries {
				exitReturns(e.Body, exit)
			}
		}
	}
}
