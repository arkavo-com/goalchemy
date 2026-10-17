package structure

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/eugenioenko/goalchemy/internal/ir"
)

// Emitter renders the target-specific parts of a structured body. Each
// method returns statement text in the target's own indentation; the printer
// re-indents it.
type Emitter interface {
	// Code renders a block's instructions.
	Code(b *ir.Block) string
	// Leaf renders a terminator that ends control flow.
	Leaf(b *ir.Block, t ir.Terminator) string
	// Pause renders an inline pause point.
	Pause(p *ir.Pause) string
	// Cond renders a Boolean value as a target condition.
	Cond(v ir.Value) string
	// Mark returns the source marker for a statement at pos, or "".
	Mark(pos token.Pos) string
}

// Syntax describes a target's statement forms. Formats take a condition or a
// label where they contain %s.
type Syntax struct {
	Indent               string
	If, ElseIf, Else     string
	End                  string
	Loop, While          string
	Block                string
	LoopLabel            string
	LabelName            string
	Break, BreakTo       string
	Continue, ContinueTo string
	// Goto and Label, when set, express labelled blocks as a jump to a
	// label after the block.
	Goto, Label string
	// GotoLoops expresses labelled break and continue through Goto too.
	GotoLoops bool
	// LabelInBlocks requires labels on every jump that leaves a labelled
	// block (Rust).
	LabelInBlocks bool
	// Python marks indentation-delimited blocks with no labels: labelled
	// jumps are lowered to an exit code in Flag and plain break/continue,
	// and labelled blocks run as one-iteration loops.
	Python  bool
	Flag    string
	Pass    string
	Not     string
	And, Or string
	// LoopStart is printed at the start of every loop body.
	LoopStart string

	// Dispatch forms. With Goto set, entries are printed in sequence under
	// EntryLabel, Resume is printed first, and a jump is GotoEntry. Otherwise
	// a DispatchOpen line with %s takes a loop label prefix, each entry opens
	// with CaseOpen (CaseFirst for the first one, when set) and closes with
	// CaseClose, and a jump is SetPC followed by a continue of the dispatch.
	// Leading tabs in DispatchOpen and DispatchClose lines add indentation.
	EntryLabel, GotoEntry string
	Resume                func(heads []*ir.Block) string
	DispatchOpen          []string
	CaseOpen, CaseFirst   string
	CaseClose             string
	Default               []string
	DispatchClose         []string
	SetPC                 string
	// Fallthrough lets an entry end by running into the next one.
	Fallthrough bool
}

// Print renders body at depth.
func Print(body *Body, syn Syntax, em Emitter, depth int) string {
	p := &printer{syn: syn, em: em, labels: map[Stmt]string{}, ends: map[Stmt]string{}, nexts: map[Stmt]string{}, codes: map[jump]int{}, crossing: map[Stmt][]jump{}}
	p.plan(body.Stmts, nil)
	if p.flagUsed {
		p.line(depth, syn.Flag+" = 0")
	}
	p.seq(body.Stmts, depth)
	return p.b.String()
}

type jump struct {
	target Stmt
	cont   bool
}

type printer struct {
	syn      Syntax
	em       Emitter
	b        strings.Builder
	stack    []Stmt
	n        int
	labels   map[Stmt]string
	ends     map[Stmt]string
	nexts    map[Stmt]string
	codes    map[jump]int
	crossing map[Stmt][]jump
	flagUsed bool
}

func (p *printer) label(s Stmt) string {
	if l, ok := p.labels[s]; ok {
		return l
	}
	p.n++
	l := fmt.Sprintf(p.syn.LabelName, p.n)
	p.labels[s] = l
	return l
}

// captures reports whether an unlabelled break or continue inside s reaches s.
func (p *printer) captures(s Stmt) bool {
	switch s.(type) {
	case *Loop, *Dispatch:
		return true
	case *Block:
		return p.syn.Python
	}
	return false
}

// plan decides which constructs need labels or exit codes.
func (p *printer) plan(ss []Stmt, stack []Stmt) {
	for _, s := range ss {
		switch s := s.(type) {
		case *If:
			p.plan(s.Then, stack)
			p.plan(s.Else, stack)
		case *Loop:
			p.plan(s.Body, append(stack, s))
		case *Block:
			p.plan(s.Body, append(stack, s))
		case *Dispatch:
			for _, e := range s.Entries {
				p.plan(e.Body, append(stack, s))
			}
		case *Break:
			p.planJump(jump{target: s.Target}, stack)
		case *Continue:
			p.planJump(jump{target: s.Target, cont: true}, stack)
		case *Goto:
			if p.syn.Goto == "" {
				p.planJump(jump{target: s.Dispatch, cont: true}, stack)
			}
		}
	}
}

func (p *printer) innermost(stack []Stmt) (Stmt, bool) {
	inBlock := false
	for i := len(stack) - 1; i >= 0; i-- {
		if p.captures(stack[i]) {
			return stack[i], inBlock
		}
		if _, ok := stack[i].(*Block); ok {
			inBlock = true
		}
	}
	return nil, inBlock
}

func (p *printer) planJump(j jump, stack []Stmt) {
	in, inBlock := p.innermost(stack)
	if in == j.target && !(p.syn.LabelInBlocks && inBlock) {
		return
	}
	if p.syn.Python {
		if _, ok := p.codes[j]; !ok {
			p.codes[j] = len(p.codes) + 1
		}
		p.flagUsed = true
		for i := len(stack) - 1; i >= 0 && stack[i] != j.target; i-- {
			if p.captures(stack[i]) {
				p.crossing[stack[i]] = appendJump(p.crossing[stack[i]], j)
			}
		}
		return
	}
	_, isBlock := j.target.(*Block)
	switch {
	case isBlock && p.syn.Goto != "":
		p.ends[j.target] = p.label(j.target)
	case !isBlock && p.syn.GotoLoops && j.cont:
		p.nexts[j.target] = p.label(j.target) + "_next"
	case !isBlock && p.syn.GotoLoops:
		p.ends[j.target] = p.label(j.target) + "_end"
	default:
		p.label(j.target)
	}
}

func appendJump(js []jump, j jump) []jump {
	for _, x := range js {
		if x == j {
			return js
		}
	}
	return append(js, j)
}

func (p *printer) line(depth int, s string) {
	for _, l := range strings.Split(s, "\n") {
		p.b.WriteString(strings.Repeat(p.syn.Indent, depth))
		p.b.WriteString(l)
		p.b.WriteString("\n")
	}
}

// text writes emitter output re-indented to depth, keeping the relative
// indentation of its lines and any leading source markers.
func (p *printer) text(depth int, s string) {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(stripMarks(s)) == "" {
		return
	}
	lines := strings.Split(s, "\n")
	type part struct {
		marks, rest string
		ws          int
	}
	parts := make([]part, len(lines))
	min := -1
	for i, l := range lines {
		var marks strings.Builder
		ws := 0
		for {
			if strings.HasPrefix(l, "\x00@") {
				k := strings.IndexByte(l[2:], 0)
				if k < 0 {
					break
				}
				marks.WriteString(l[:k+3])
				l = l[k+3:]
				continue
			}
			if l != "" && (l[0] == ' ' || l[0] == '\t') {
				if l[0] == '\t' {
					ws += 4
				} else {
					ws++
				}
				l = l[1:]
				continue
			}
			break
		}
		parts[i] = part{marks.String(), l, ws}
		if l != "" && (min < 0 || ws < min) {
			min = ws
		}
	}
	for _, pt := range parts {
		if pt.rest == "" && pt.marks == "" {
			continue
		}
		p.b.WriteString(strings.Repeat(p.syn.Indent, depth))
		p.b.WriteString(pt.marks)
		if pt.ws > min {
			p.b.WriteString(strings.Repeat(" ", pt.ws-min))
		}
		p.b.WriteString(pt.rest)
		p.b.WriteString("\n")
	}
}

func stripMarks(s string) string {
	for {
		i := strings.Index(s, "\x00@")
		if i < 0 {
			return s
		}
		k := strings.IndexByte(s[i+2:], 0)
		if k < 0 {
			return s
		}
		s = s[:i] + s[i+k+3:]
	}
}

func (p *printer) seq(ss []Stmt, depth int) {
	for _, s := range ss {
		p.stmt(s, depth)
	}
}

func (p *printer) body(ss []Stmt, depth int) {
	start := p.b.Len()
	p.seq(ss, depth)
	if p.syn.Python && p.b.Len() == start {
		p.line(depth, p.syn.Pass)
	}
}

func (p *printer) push(s Stmt) { p.stack = append(p.stack, s) }
func (p *printer) pop()        { p.stack = p.stack[:len(p.stack)-1] }

func (p *printer) stmt(s Stmt, depth int) {
	switch s := s.(type) {
	case *Code:
		p.text(depth, p.em.Code(s.Block))
	case *Leaf:
		p.text(depth, p.em.Leaf(s.Block, s.Term))
	case *Pause:
		p.text(depth, p.em.Pause(s.Pause))
	case *If:
		p.ifStmt(s, depth, p.syn.If)
		if p.syn.End != "" {
			p.line(depth, p.syn.End)
		}
	case *Loop:
		p.loop(s, depth)
	case *Block:
		p.block(s, depth)
	case *Break:
		p.jump(jump{target: s.Target}, depth)
	case *Continue:
		p.jump(jump{target: s.Target, cont: true}, depth)
	case *Goto:
		if p.syn.Goto != "" {
			p.line(depth, fmt.Sprintf(p.syn.GotoEntry, s.Target.ID))
			return
		}
		p.line(depth, fmt.Sprintf(p.syn.SetPC, s.Target.ID))
		p.jump(jump{target: s.Dispatch, cont: true}, depth)
	case *Dispatch:
		p.dispatch(s, depth)
	default:
		panic(fmt.Sprintf("structure: statement %T", s))
	}
}

func (p *printer) ifStmt(s *If, depth int, open string) {
	p.line(depth, p.em.Mark(s.Pos)+fmt.Sprintf(open, p.cond(s.Cond)))
	p.body(s.Then, depth+1)
	if len(s.Else) == 0 {
		return
	}
	if len(s.Else) == 1 {
		if e, ok := s.Else[0].(*If); ok {
			p.ifStmt(e, depth, p.syn.ElseIf)
			return
		}
	}
	p.line(depth, p.syn.Else)
	p.body(s.Else, depth+1)
}

func (p *printer) prefix(s Stmt) string {
	if l, ok := p.labels[s]; ok && p.syn.LoopLabel != "" && !p.syn.GotoLoops && !p.syn.Python {
		return fmt.Sprintf(p.syn.LoopLabel, l)
	}
	return ""
}

func (p *printer) loop(l *Loop, depth int) {
	open := p.syn.Loop
	if l.While != nil {
		open = fmt.Sprintf(p.syn.While, p.cond(l.While))
	}
	p.line(depth, p.prefix(l)+open)
	p.push(l)
	if p.syn.LoopStart != "" {
		p.line(depth+1, p.syn.LoopStart)
	}
	p.body(l.Body, depth+1)
	if n, ok := p.nexts[l]; ok {
		p.line(depth+1, fmt.Sprintf(p.syn.Label, n))
	}
	p.pop()
	if p.syn.End != "" {
		p.line(depth, p.syn.End)
	}
	p.after(l, depth)
}

func (p *printer) block(b *Block, depth int) {
	switch {
	case p.syn.Python:
		p.line(depth, p.syn.Loop)
		p.push(b)
		p.seq(b.Body, depth+1)
		p.line(depth+1, p.syn.Break)
		p.pop()
	case p.syn.Goto != "":
		p.push(b)
		p.seq(b.Body, depth)
		p.pop()
	default:
		p.line(depth, fmt.Sprintf(p.syn.Block, p.label(b)))
		p.push(b)
		p.body(b.Body, depth+1)
		p.pop()
		p.line(depth, p.syn.End)
	}
	p.after(b, depth)
}

// after writes the label after a construct that jumps leave through goto,
// or the exit-code checks of Python.
func (p *printer) after(s Stmt, depth int) {
	if e, ok := p.ends[s]; ok {
		p.line(depth, fmt.Sprintf(p.syn.Label, e))
	}
	js := p.crossing[s]
	if len(js) == 0 {
		return
	}
	parent, _ := p.innermost(p.stack)
	beyond := false
	for _, j := range js {
		if j.target != parent {
			beyond = true
			continue
		}
		p.line(depth, fmt.Sprintf(p.syn.If, fmt.Sprintf("%s == %d", p.syn.Flag, p.codes[j])))
		p.line(depth+1, p.syn.Flag+" = 0")
		if j.cont {
			p.line(depth+1, p.syn.Continue)
		} else {
			p.line(depth+1, p.syn.Break)
		}
	}
	if beyond {
		p.line(depth, fmt.Sprintf(p.syn.If, p.syn.Flag))
		p.line(depth+1, p.syn.Break)
	}
}

func (p *printer) jump(j jump, depth int) {
	in, inBlock := p.innermost(p.stack)
	plain := in == j.target && !(p.syn.LabelInBlocks && inBlock)
	if p.syn.Python {
		if !plain {
			p.line(depth, fmt.Sprintf("%s = %d", p.syn.Flag, p.codes[j]))
			p.line(depth, p.syn.Break)
			return
		}
		if j.cont {
			p.line(depth, p.syn.Continue)
		} else {
			p.line(depth, p.syn.Break)
		}
		return
	}
	_, isBlock := j.target.(*Block)
	switch {
	case plain && !isBlock && j.cont:
		p.line(depth, p.syn.Continue)
	case plain && !isBlock:
		p.line(depth, p.syn.Break)
	case isBlock && p.syn.Goto != "":
		p.line(depth, fmt.Sprintf(p.syn.Goto, p.ends[j.target]))
	case p.syn.GotoLoops && j.cont:
		p.line(depth, fmt.Sprintf(p.syn.Goto, p.nexts[j.target]))
	case p.syn.GotoLoops:
		p.line(depth, fmt.Sprintf(p.syn.Goto, p.ends[j.target]))
	case j.cont:
		p.line(depth, fmt.Sprintf(p.syn.ContinueTo, p.labels[j.target]))
	default:
		p.line(depth, fmt.Sprintf(p.syn.BreakTo, p.labels[j.target]))
	}
}

func (p *printer) dispatch(d *Dispatch, depth int) {
	bodies := make([][]Stmt, len(d.Entries))
	for i, e := range d.Entries {
		bodies[i] = e.Body
		if n := len(e.Body); n > 0 && i+1 < len(d.Entries) && (p.syn.Goto != "" || p.syn.Fallthrough) {
			if g, ok := e.Body[n-1].(*Goto); ok && g.Target == d.Entries[i+1].Head {
				bodies[i] = e.Body[:n-1]
			}
		}
	}
	if p.syn.Goto != "" {
		var resumes []*ir.Block
		for _, e := range d.Entries {
			if e.Head.ResumeOf != nil {
				resumes = append(resumes, e.Head)
			}
		}
		used := map[*ir.Block]bool{}
		if len(resumes) > 0 && p.syn.Resume != nil {
			p.text(depth, p.syn.Resume(resumes))
			for _, h := range resumes {
				used[h] = true
			}
		}
		for _, b := range bodies {
			walk(b, func(s Stmt) {
				if g, ok := s.(*Goto); ok {
					used[g.Target] = true
				}
			})
		}
		p.push(d)
		for i, e := range d.Entries {
			if used[e.Head] {
				p.line(depth, fmt.Sprintf(p.syn.EntryLabel, e.Head.ID))
			}
			p.seq(bodies[i], depth)
		}
		p.pop()
		return
	}
	inner := 1
	for _, l := range p.syn.DispatchOpen {
		n := len(l) - len(strings.TrimLeft(l, "\t"))
		if strings.Contains(l, "%s") {
			l = fmt.Sprintf(l, p.prefix(d))
		}
		p.line(depth+n, strings.TrimLeft(l, "\t"))
		if n+1 > inner {
			inner = n + 1
		}
	}
	p.push(d)
	for i, e := range d.Entries {
		open := p.syn.CaseOpen
		if i == 0 && p.syn.CaseFirst != "" {
			open = p.syn.CaseFirst
		}
		p.line(depth+inner, fmt.Sprintf(open, e.Head.ID))
		p.body(bodies[i], depth+inner+1)
		if p.syn.CaseClose != "" {
			p.line(depth+inner, p.syn.CaseClose)
		}
	}
	p.pop()
	for _, l := range p.syn.Default {
		p.line(depth+inner, l)
	}
	for _, l := range p.syn.DispatchClose {
		n := len(l) - len(strings.TrimLeft(l, "\t"))
		p.line(depth+n, strings.TrimLeft(l, "\t"))
	}
	p.after(d, depth)
}

func (p *printer) cond(c Cond) string {
	switch c := c.(type) {
	case Value:
		return p.em.Cond(c.V)
	case Not:
		x := p.cond(c.X)
		if _, ok := c.X.(Value); !ok {
			x = Open + x + Close
		}
		return fmt.Sprintf(p.syn.Not, x)
	case And:
		return fmt.Sprintf(p.syn.And, p.operand(c.X, false), p.operand(c.Y, false))
	case Or:
		return fmt.Sprintf(p.syn.Or, p.operand(c.X, true), p.operand(c.Y, true))
	}
	panic(fmt.Sprintf("structure: condition %T", c))
}

func (p *printer) operand(c Cond, or bool) string {
	s := p.cond(c)
	switch c.(type) {
	case And:
		if or {
			return Open + s + Close
		}
	case Or:
		if !or {
			return Open + s + Close
		}
	case Not:
		if p.syn.Python {
			return Open + s + Close
		}
	}
	return s
}
