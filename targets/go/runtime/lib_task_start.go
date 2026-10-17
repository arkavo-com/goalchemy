package rt

type FutureState = *taskFuture

type taskFuture struct {
	done      bool
	observed  bool
	results   []any
	panicking *Panic
	waiters   []*Task
}

type futureChild struct {
	FrameBase
	start   func() Frame
	results []any
}

func (f *futureChild) Results() []any { return f.results }
func (f *futureChild) Step(t *Task) {
	if f.PC == 0 {
		f.PC = 1
		Call(t, f.start())
		return
	}
	f.results = t.RV
	Ret(t, f)
}

func LibTaskStart(fn any) FutureState {
	future := &taskFuture{}
	child := &futureChild{}
	switch fn := fn.(type) {
	case func() Frame:
		child.start = fn
	case func(*Task) []any:
		child.start = func() Frame { return NativeTask(func(t *Task) []any { return []any{fn(t)} }) }
	case func() []any:
		child.start = func() Frame { return Sync(func() []any { return []any{fn()} }) }
	default:
		panic(HostFault{Value: "invalid future function"})
	}
	child.complete = func(_ *Task, f Frame, p *Panic) {
		future.done, future.panicking = true, p
		if p == nil {
			future.results = As[[]any](f.Results()[0])
		}
		if p != nil && !future.observed {
			if sched.unawaited == nil {
				sched.unawaited = make(map[any]*Panic)
			}
			sched.unawaited[future] = p
			sched.unawaitedOrder = append(sched.unawaitedOrder, future)
		}
		for _, waiter := range future.waiters {
			waiter.RV = []any{future.results}
			waiter.resumePanic = copyFuturePanic(p)
			sched.ready(waiter)
		}
		future.waiters = nil
	}
	Spawn(child)
	return future
}

func copyFuturePanic(p *Panic) *Panic {
	if p == nil {
		return nil
	}
	return &Panic{Value: p.Value, Prev: copyFuturePanic(p.Prev)}
}
