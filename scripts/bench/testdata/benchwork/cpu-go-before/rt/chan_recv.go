package rt

// Recv implements v, ok := <-c as a pause primitive; the results arrive in
// t.RV as {value, ok}.
func (c Chan[T]) Recv(t *Task) {
	ch := c.c
	s := sched
	var zero T
	if ch == nil {
		s.block(t)
		return
	}
	if v, ok, done := ch.tryRecv(); done {
		if !ok {
			t.RV = []any{zero, false}
		} else {
			t.RV = []any{v, true}
		}
		return
	}
	ch.recvq = append(ch.recvq, &waiter{t: t})
	s.block(t)
}

// tryRecv receives without blocking when a value or closure is available.
func (ch *chanCore) tryRecv() (any, bool, bool) {
	if len(ch.buf) > 0 {
		v := ch.buf[0]
		ch.buf = ch.buf[1:]
		if w := dequeue(&ch.sendq); w != nil {
			ch.buf = append(ch.buf, w.val)
			w.sendDone(false)
		}
		return v, true, true
	}
	if w := dequeue(&ch.sendq); w != nil {
		v := w.val
		w.sendDone(false)
		return v, true, true
	}
	if ch.closed {
		return nil, false, true
	}
	return nil, false, false
}
