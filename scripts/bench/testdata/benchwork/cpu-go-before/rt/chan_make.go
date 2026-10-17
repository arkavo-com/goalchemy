package rt

// selectState is shared by the waiters a blocked select registers.
type selectState struct {
	done  bool
	index int
}

type waiter struct {
	t   *Task
	val any
	sel *selectState
	idx int
}

type chanCore struct {
	buf    []any
	size   int
	closed bool
	recvq  []*waiter
	sendq  []*waiter
}

// Chan is a channel value: a handle to shared channel state. The zero Chan
// is the nil channel.
type Chan[T any] struct {
	c *chanCore
}

// MakeChan implements make(chan T, size).
func MakeChan[T any](size int) Chan[T] {
	if size < 0 {
		panic(PlainError("makechan: size out of range"))
	}
	return Chan[T]{&chanCore{size: size}}
}

// dequeue removes the first waiter that can still complete.
func dequeue(q *[]*waiter) *waiter {
	for len(*q) > 0 {
		w := (*q)[0]
		*q = (*q)[1:]
		if w.sel == nil || !w.sel.done {
			return w
		}
	}
	return nil
}

// recvDone completes a waiting receiver with a value or closure.
func (w *waiter) recvDone(val any, ok bool) {
	if w.sel != nil {
		w.sel.done = true
		w.t.RV = []any{w.idx, val, ok}
	} else {
		w.t.RV = []any{val, ok}
	}
	sched.ready(w.t)
}

// sendDone completes a waiting sender; closed makes it panic on resume.
func (w *waiter) sendDone(closed bool) {
	if w.sel != nil {
		w.sel.done = true
		w.t.RV = []any{w.idx, nil, false}
	} else {
		w.t.RV = nil
	}
	if closed {
		w.t.resumePanic = &Panic{Value: PlainError("send on closed channel")}
	}
	sched.ready(w.t)
}
