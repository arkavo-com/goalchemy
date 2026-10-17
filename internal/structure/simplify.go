package structure

// simplify rewrites a structured body into a more source-like form without
// changing its meaning: loop exits become loop breaks, jumps to where control
// would fall anyway disappear, blocks nothing breaks out of are spliced, early
// exits are hoisted out of if/else, and leading exit tests become while
// conditions.
func simplify(ss []Stmt) []Stmt {
	for i := 0; i < 8; i++ {
		before := count(ss)
		loopBreaks(ss)
		ss = tails(ss, nil)
		ss = splice(ss)
		ss = hoist(ss)
		ss = tails(ss, nil)
		ss = splice(ss)
		whiles(ss)
		if count(ss) == before {
			break
		}
	}
	return ss
}

func count(ss []Stmt) int {
	n := 0
	walk(ss, func(s Stmt) {
		n++
		if l, ok := s.(*Loop); ok && l.While != nil {
			n++
		}
	})
	return n
}

func walk(ss []Stmt, fn func(Stmt)) {
	for _, s := range ss {
		fn(s)
		switch s := s.(type) {
		case *If:
			walk(s.Then, fn)
			walk(s.Else, fn)
		case *Loop:
			walk(s.Body, fn)
		case *Block:
			walk(s.Body, fn)
		case *Dispatch:
			for _, e := range s.Entries {
				walk(e.Body, fn)
			}
		}
	}
}

// loopBreaks retargets breaks out of a block whose last statement is a loop
// to that loop: leaving the loop reaches the end of the block.
func loopBreaks(ss []Stmt) {
	walk(ss, func(s Stmt) {
		b, ok := s.(*Block)
		if !ok || len(b.Body) == 0 {
			return
		}
		l, ok := b.Body[len(b.Body)-1].(*Loop)
		if !ok {
			return
		}
		walk(l.Body, func(s Stmt) {
			if br, ok := s.(*Break); ok && br.Target == b {
				br.Target = l
			}
		})
	})
}

// fall describes where control goes when a statement sequence completes:
// out of each block in brk, or to the next iteration of cont.
type fall struct {
	brk  []Stmt
	cont *Loop
}

func (f *fall) matches(s Stmt) bool {
	if f == nil {
		return false
	}
	switch s := s.(type) {
	case *Break:
		for _, b := range f.brk {
			if s.Target == b {
				return true
			}
		}
	case *Continue:
		return f.cont != nil && s.Target == f.cont
	}
	return false
}

func tails(ss []Stmt, f *fall) []Stmt {
	for i, s := range ss {
		var ft *fall
		if i == len(ss)-1 {
			ft = f
		}
		switch s := s.(type) {
		case *If:
			s.Then = tails(s.Then, ft)
			s.Else = tails(s.Else, ft)
		case *Loop:
			s.Body = tails(s.Body, &fall{cont: s})
		case *Block:
			inner := &fall{brk: []Stmt{s}}
			if ft != nil {
				inner.brk = append(inner.brk, ft.brk...)
				inner.cont = ft.cont
			}
			s.Body = tails(s.Body, inner)
		case *Dispatch:
			for _, e := range s.Entries {
				e.Body = tails(e.Body, nil)
			}
		}
	}
	if n := len(ss); n > 0 && f.matches(ss[n-1]) {
		ss = ss[:n-1]
	}
	return ss
}

func targeted(ss []Stmt, t Stmt) bool {
	found := false
	walk(ss, func(s Stmt) {
		switch s := s.(type) {
		case *Break:
			found = found || s.Target == t
		case *Continue:
			found = found || s.Target == t
		}
	})
	return found
}

func splice(ss []Stmt) []Stmt {
	var out []Stmt
	for _, s := range ss {
		switch s := s.(type) {
		case *If:
			s.Then = splice(s.Then)
			s.Else = splice(s.Else)
		case *Loop:
			s.Body = splice(s.Body)
		case *Block:
			s.Body = splice(s.Body)
			if !targeted(s.Body, s) {
				out = append(out, s.Body...)
				continue
			}
		case *Dispatch:
			for _, e := range s.Entries {
				e.Body = splice(e.Body)
			}
		}
		out = append(out, s)
	}
	return out
}

// completes reports whether control can leave ss at its end.
func completes(ss []Stmt) bool {
	for _, s := range ss {
		if !completesStmt(s) {
			return false
		}
	}
	return true
}

func completesStmt(s Stmt) bool {
	switch s := s.(type) {
	case *Leaf, *Break, *Continue, *Goto, *Dispatch:
		return false
	case *If:
		return completes(s.Then) || completes(s.Else)
	case *Loop:
		return s.While != nil || targeted(s.Body, s)
	case *Block:
		return completes(s.Body) || targeted(s.Body, s)
	}
	return true
}

func size(ss []Stmt) int {
	n := 0
	walk(ss, func(Stmt) { n++ })
	return n
}

// hoist moves the arm of an if/else that follows an arm which cannot
// complete out after the if, keeping the shorter exit inside.
func hoist(ss []Stmt) []Stmt {
	var out []Stmt
	for _, s := range ss {
		switch s := s.(type) {
		case *If:
			s.Then = hoist(s.Then)
			s.Else = hoist(s.Else)
			if len(s.Then) == 0 && len(s.Else) > 0 {
				s.Cond, s.Then, s.Else = negate(s.Cond), s.Else, nil
			}
			if len(s.Else) == 0 {
				break
			}
			tc, ec := completes(s.Then), completes(s.Else)
			switch {
			case !tc && (ec || size(s.Then) <= size(s.Else)):
				rest := s.Else
				s.Else = nil
				out = append(out, s)
				out = append(out, rest...)
				continue
			case !ec:
				rest := s.Then
				s.Cond, s.Then, s.Else = negate(s.Cond), s.Else, nil
				out = append(out, s)
				out = append(out, rest...)
				continue
			}
		case *Loop:
			s.Body = hoist(s.Body)
		case *Block:
			s.Body = hoist(s.Body)
		case *Dispatch:
			for _, e := range s.Entries {
				e.Body = hoist(e.Body)
			}
		}
		out = append(out, s)
	}
	return out
}

// whiles turns a loop whose body starts with a test that breaks out of it
// into a loop with a while condition.
func whiles(ss []Stmt) {
	walk(ss, func(s Stmt) {
		l, ok := s.(*Loop)
		if !ok || l.While != nil || len(l.Body) == 0 {
			return
		}
		i, ok := l.Body[0].(*If)
		if !ok || len(i.Else) != 0 || len(i.Then) != 1 {
			return
		}
		if br, ok := i.Then[0].(*Break); ok && br.Target == l {
			l.While = negate(i.Cond)
			l.Body = l.Body[1:]
		}
	})
}
