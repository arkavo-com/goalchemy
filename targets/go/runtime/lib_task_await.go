package rt

func LibTaskAwait(t *Task, f FutureState) {
	if f == nil {
		sched.block(t)
		return
	}
	f.observed = true
	delete(sched.unawaited, f)
	if f.done {
		if f.panicking != nil {
			panic(copyFuturePanic(f.panicking))
		}
		t.RV = []any{f.results}
		return
	}
	f.waiters = append(f.waiters, t)
	sched.block(t)
}
