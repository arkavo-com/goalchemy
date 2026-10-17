package rt

type allState struct {
	n      int
	waiter *Task
}

// allChild runs one function of task.All and counts its completion.
type allChild struct {
	FrameBase
	start func() Frame
	st    *allState
}

func (f *allChild) Results() []any { return nil }

func (f *allChild) Step(t *Task) {
	if f.PC == 0 {
		f.PC = 1
		Call(t, f.start())
		return
	}
	f.st.n--
	if f.st.n == 0 {
		sched.ready(f.st.waiter)
	}
	Ret(t, f)
}

// LibTaskAll implements task.All as a pause primitive: each function runs
// as a task, in order, and the caller resumes when all have finished.
func LibTaskAll(t *Task, fns any) {
	var starts []func() Frame
	switch fs := fns.(type) {
	case []func():
		for _, fn := range fs {
			starts = append(starts, func() Frame { return Sync(func() []any { fn(); return nil }) })
		}
	case []func() Frame:
		starts = fs
	case []func(*Task):
		for _, fn := range fs {
			starts = append(starts, func() Frame { return NativeTask(func(t *Task) []any { fn(t); return nil }) })
		}
	}
	t.RV = nil
	if len(starts) == 0 {
		return
	}
	st := &allState{n: len(starts), waiter: t}
	for _, s := range starts {
		Spawn(&allChild{start: s, st: st})
	}
	sched.block(t)
}
