package rt

// Send implements c <- v as a pause primitive.
func (c Chan[T]) Send(t *Task, v T) {
	ch := c.c
	s := sched
	if ch == nil {
		s.block(t)
		return
	}
	if ch.closed {
		panic(PlainError("send on closed channel"))
	}
	if w := dequeue(&ch.recvq); w != nil {
		w.recvDone(v, true)
		return
	}
	if len(ch.buf) < ch.size {
		ch.buf = append(ch.buf, v)
		return
	}
	ch.sendq = append(ch.sendq, &waiter{t: t, val: v})
	s.block(t)
}
