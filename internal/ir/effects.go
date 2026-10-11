package ir

import "strings"

// ComputeEffects determines which functions may suspend. Channel
// operations, select, and suspending capability calls seed the analysis.
// Calls through function values and interfaces are resolved conservatively
// to every function in the program that could be the callee: functions
// whose value is taken with a matching signature, and methods of boxed
// types with the called method identity. Effects propagate through the call
// graph to a fixed point.
//
// The analysis also classifies function types and method identities: a
// function type is suspending when any value of it may suspend, and every
// value of such a type uses the resumable calling convention (a starter
// returning a frame). The same holds for interface methods. The result marks
// Func.MaySuspend, Call.Suspends, Program.SuspTypes, Program.SuspMethods,
// and Program.Cooperative.
func ComputeEffects(p *Program) {
	valueFuncs := map[*Type][]*Func{}
	methodImpls := map[string][]*Func{}
	ibound := map[*Type][]string{}
	for _, f := range p.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *MakeClosure:
					valueFuncs[i.Dst.Type.U()] = append(valueFuncs[i.Dst.Type.U()], i.Func)
				case *MakeBound:
					valueFuncs[i.Dst.Type.U()] = append(valueFuncs[i.Dst.Type.U()], i.Func)
				case *MakeIfaceBound:
					ibound[i.Dst.Type.U()] = append(ibound[i.Dst.Type.U()], i.Method)
				}
				forEachValue(in, func(v Value) {
					if fr, ok := v.(*FuncRef); ok {
						valueFuncs[fr.Type.U()] = append(valueFuncs[fr.Type.U()], fr.Func)
					}
				})
			}
		}
	}
	for _, t := range p.Types.All {
		for _, m := range t.MethodSet {
			methodImpls[m.ID] = append(methodImpls[m.ID], m.Func)
		}
	}
	p.SuspTypes = map[*Type]bool{}
	p.SuspMethods = map[string]bool{}
	direct := func(in Instr) bool {
		switch i := in.(type) {
		case *Send, *Recv, *Select:
			return true
		case *Call:
			return i.Kind == CallExtern && i.Extern.MaySuspend
		case *Defer:
			return i.Call.Kind == CallExtern && i.Call.Extern.MaySuspend
		}
		return false
	}
	for _, f := range p.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch in.(type) {
				case *Send, *Recv, *Select, *Go, *MakeChan, *Close:
					p.Cooperative = true
				}
				if c, ok := in.(*Call); ok && c.Kind == CallExtern && c.Extern.MaySuspend {
					p.Cooperative = true
				}
				if direct(in) {
					f.MaySuspend = true
				}
			}
		}
	}
	callSuspends := func(c *Call) bool {
		switch c.Kind {
		case CallStatic:
			return c.Func.MaySuspend
		case CallValue:
			return p.SuspTypes[c.Fn.IRType().U()]
		case CallInterface:
			return p.SuspMethods[c.Method]
		case CallExtern:
			return c.Extern.MaySuspend
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		for id, impls := range methodImpls {
			if p.SuspMethods[id] {
				continue
			}
			for _, f := range impls {
				if f.MaySuspend {
					p.SuspMethods[id] = true
					changed = true
					break
				}
			}
		}
		for t, fs := range valueFuncs {
			if p.SuspTypes[t] {
				continue
			}
			for _, f := range fs {
				if f.MaySuspend {
					p.SuspTypes[t] = true
					changed = true
					break
				}
			}
		}
		for t, ids := range ibound {
			if p.SuspTypes[t] {
				continue
			}
			for _, id := range ids {
				if p.SuspMethods[id] {
					p.SuspTypes[t] = true
					changed = true
					break
				}
			}
		}
		for _, f := range p.Funcs {
			if f.MaySuspend {
				continue
			}
		scan:
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					var c *Call
					switch i := in.(type) {
					case *Call:
						c = i
					case *Defer:
						c = i.Call
					}
					if c != nil && callSuspends(c) {
						f.MaySuspend = true
						changed = true
						break scan
					}
				}
			}
		}
	}
	for _, f := range p.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *Call:
					i.Suspends = callSuspends(i)
				case *Defer:
					i.Call.Suspends = callSuspends(i.Call)
				case *Go:
					i.Call.Suspends = callSuspends(i.Call)
				}
			}
		}
	}
	if p.Entry != nil && p.Entry.MaySuspend {
		p.Cooperative = true
	}
}

// IsPause reports whether an instruction in a suspending function is a
// pause point: the task may stop there and resume later.
func IsPause(in Instr) bool {
	switch i := in.(type) {
	case *Send, *Recv, *Select:
		return true
	case *Call:
		return i.Suspends
	}
	return false
}

// SplitPauses lowers every suspending function to resumable form: each
// pause point ends its block with a Pause terminator whose continuation
// block starts by receiving the paused operation's results. Together with
// a frame holding the function's locals, the block index of a continuation
// is the whole resumption state, so every target can resume a function
// without native coroutines.
func SplitPauses(p *Program) {
	for _, f := range p.Funcs {
		if !f.MaySuspend {
			continue
		}
		for bi := 0; bi < len(f.Blocks); bi++ {
			b := f.Blocks[bi]
			for k, in := range b.Instrs {
				if !IsPause(in) {
					continue
				}
				next := &Block{Comment: "resume", Instrs: append([]Instr(nil), b.Instrs[k+1:]...), Term: b.Term}
				next.ResumeOf = in
				b.Instrs = b.Instrs[:k]
				b.Term = &Pause{At: At{Pos: in.Position()}, Op: in, Next: next}
				f.Blocks = append(f.Blocks, next)
				break
			}
		}
		for i, b := range f.Blocks {
			b.ID = i
		}
	}
}

// forEachValue calls fn for each operand of an instruction.
func forEachValue(in Instr, fn func(Value)) {
	vals := func(vs ...Value) {
		for _, v := range vs {
			if v != nil {
				fn(v)
			}
		}
	}
	place := func(p *Place) {
		switch r := p.Root.(type) {
		case DerefRoot:
			vals(r.Ptr)
		case SliceRoot:
			vals(r.Slice, r.Index)
		case ValueRoot:
			vals(r.Value)
		}
		for _, pr := range p.Path {
			if pr.Index != nil {
				vals(pr.Index)
			}
		}
	}
	switch i := in.(type) {
	case *DeclVar:
		if i.Init != nil {
			vals(i.Init)
		}
	case *Assign:
		vals(i.Src)
	case *Load:
		place(i.Place)
	case *Store:
		place(i.Place)
		vals(i.V)
	case *AddrOf:
		place(i.Place)
	case *UnOp:
		vals(i.X)
	case *BinOp:
		vals(i.X, i.Y)
	case *Convert:
		vals(i.X)
	case *MakeInterface:
		vals(i.X)
	case *TypeAssert:
		vals(i.X)
	case *Call:
		vals(i.Fn, i.Recv)
		vals(i.Args...)
	case *Defer:
		vals(i.Call.Fn, i.Call.Recv)
		vals(i.Call.Args...)
	case *Go:
		vals(i.Call.Fn, i.Call.Recv)
		vals(i.Call.Args...)
	case *MakeBound:
		vals(i.Recv)
	case *MakeIfaceBound:
		vals(i.Recv)
	case *Len:
		vals(i.X)
	case *Cap:
		vals(i.X)
	case *MakeSlice:
		vals(i.Len, i.Cap)
	case *Append:
		vals(i.S, i.Spread)
		vals(i.Elems...)
	case *Copy:
		vals(i.Dst, i.Src)
	case *IndexString:
		vals(i.S, i.I)
	case *SliceOp:
		vals(i.X, i.Lo, i.Hi, i.Max)
	case *MapLookup:
		vals(i.M, i.K)
	case *MapStore:
		vals(i.M, i.K, i.V)
	case *MapDelete:
		vals(i.M, i.K)
	case *Clear:
		vals(i.X)
	case *DecodeRune:
		vals(i.S, i.I)
	case *MapIterInit:
		vals(i.M)
	case *Print:
		vals(i.Args...)
	case *MakeChan:
		vals(i.Size)
	case *Send:
		vals(i.Ch, i.V)
	case *Recv:
		vals(i.Ch)
	case *Close:
		vals(i.Ch)
	case *Select:
		for _, c := range i.Cases {
			vals(c.Ch, c.V)
		}
	}
}

// HostEntry reports whether an executable linking contracts must start on
// its target's host entry: host capabilities register native operations,
// which the virtual-time entry rejects on most targets.
func HostEntry(contracts map[string]bool) bool {
	for c := range contracts {
		if strings.HasPrefix(c, "lib.crypto.") || strings.HasPrefix(c, "lib.http.") || c == "lib.callback.request" {
			return true
		}
	}
	return false
}
